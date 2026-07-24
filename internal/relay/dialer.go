package relay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

// Dialer represents an upstream relay that can dial connections.
type Dialer interface {
	Name() string
	Type() string
	Addr() string
	SupportUDP() bool
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Close() error
}

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)
type RelayResolveFunc func(ctx context.Context, groupName, host string) ([]netip.Addr, time.Time, error)

// RelayDialer wraps a mihomo relay adapter.
type RelayDialer struct {
	adapter C.Proxy
}

func (d *RelayDialer) Name() string     { return d.adapter.Name() }
func (d *RelayDialer) Type() string     { return d.adapter.Type().String() }
func (d *RelayDialer) Addr() string     { return d.adapter.Addr() }
func (d *RelayDialer) SupportUDP() bool { return d.adapter.SupportUDP() }

func (d *RelayDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}

	metadata := &C.Metadata{
		Host:    host,
		DstPort: uint16(port),
	}

	switch network {
	case "tcp", "tcp4", "tcp6":
		metadata.NetWork = C.TCP
		conn, err := d.adapter.DialContext(ctx, metadata)
		if err != nil {
			return nil, fmt.Errorf("relay %s dial: %w", d.adapter.Name(), err)
		}
		return &connWrapper{Conn: conn}, nil
	case "udp", "udp4", "udp6":
		metadata.NetWork = C.UDP
		pc, err := d.adapter.ListenPacketContext(ctx, metadata)
		if err != nil {
			return nil, fmt.Errorf("relay %s listen packet: %w", d.adapter.Name(), err)
		}
		return &packetConnWrapper{PacketConn: pc, addr: address}, nil
	default:
		return nil, fmt.Errorf("unsupported network: %s", network)
	}
}

func (d *RelayDialer) Close() error {
	if closer, ok := d.adapter.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// DirectDialer connects directly without a relay.
type DirectDialer struct {
	dialContext DialContextFunc
}

func (d *DirectDialer) Name() string     { return "DIRECT" }
func (d *DirectDialer) Type() string     { return "direct" }
func (d *DirectDialer) Addr() string     { return "" }
func (d *DirectDialer) SupportUDP() bool { return true }
func (d *DirectDialer) Close() error     { return nil }

func (d *DirectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.dialContext != nil {
		return d.dialContext(ctx, network, address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func NewDirectDialer(dialContext DialContextFunc) *DirectDialer {
	return &DirectDialer{dialContext: dialContext}
}

type LazyRelayDialer struct {
	mu        sync.Mutex
	groupName string
	name      string
	relayType string
	addr      string

	mapping  map[string]any
	resolver RelayResolveFunc

	resolved  Dialer
	expiresAt time.Time
}

func (d *LazyRelayDialer) Name() string { return d.name }
func (d *LazyRelayDialer) Type() string { return d.relayType }
func (d *LazyRelayDialer) Addr() string { return d.addr }

func (d *LazyRelayDialer) SupportUDP() bool {
	dialer, err := d.getDialer(context.Background(), false)
	if err != nil {
		return false
	}
	return dialer.SupportUDP()
}

func (d *LazyRelayDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer, err := d.getDialer(ctx, true)
	if err != nil {
		return nil, err
	}
	return dialer.DialContext(ctx, network, address)
}

func (d *LazyRelayDialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.resolved == nil {
		return nil
	}
	err := d.resolved.Close()
	d.resolved = nil
	d.expiresAt = time.Time{}
	return err
}

func (d *LazyRelayDialer) resolvedRelayAddr(ctx context.Context) (string, error) {
	mapping := cloneRelayMapping(d.mapping)
	server, _ := mapping["server"].(string)
	if server == "" {
		return "", fmt.Errorf("relay %s missing server", d.name)
	}
	port := fmt.Sprint(mapping["port"])
	if port == "" || port == "<nil>" {
		return "", fmt.Errorf("relay %s missing port", d.name)
	}
	if net.ParseIP(server) == nil && d.resolver != nil {
		ips, _, err := d.resolver(ctx, d.groupName, server)
		if err != nil {
			return "", fmt.Errorf("resolve relay server %q: %w", server, err)
		}
		if len(ips) == 0 {
			return "", fmt.Errorf("resolve relay server %q: no addresses returned", server)
		}
		server = ips[0].String()
	}
	return net.JoinHostPort(server, port), nil
}

func (d *LazyRelayDialer) getDialer(ctx context.Context, allowResolve bool) (Dialer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.resolved != nil && (d.expiresAt.IsZero() || time.Now().Before(d.expiresAt)) {
		return d.resolved, nil
	}
	if !allowResolve {
		if d.resolved != nil {
			return d.resolved, nil
		}
		return nil, fmt.Errorf("relay %s has not been resolved yet", d.name)
	}

	mapping := cloneRelayMapping(d.mapping)
	expiresAt := time.Time{}
	if d.resolver != nil {
		server, _ := mapping["server"].(string)
		if server != "" && net.ParseIP(server) == nil {
			ips, ttlExpiry, err := d.resolver(ctx, d.groupName, server)
			if err != nil {
				return nil, fmt.Errorf("resolve relay server %q: %w", server, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("resolve relay server %q: no addresses returned", server)
			}
			mapping["server"] = ips[0].String()
			if ips[0].Is6() {
				mapping["ip-version"] = "ipv6"
			} else {
				mapping["ip-version"] = "ipv4"
			}
			expiresAt = ttlExpiry
			slog.Debug("resolved relay hostname", "group", d.groupName, "relay", d.name, "host", server, "ip", ips[0].String(), "expires_at", ttlExpiry)
		}
	}

	next, err := NewDialerFromMapping(mapping)
	if err != nil {
		return nil, err
	}
	// Do not explicitly close the expired adapter here. Mihomo connections
	// retain a reference to their adapter, and its auto-close finalizer releases
	// pooled resources once the last live connection is gone. Closing it during
	// DNS rotation would tear down active multiplexed sessions such as AnyTLS.
	d.resolved = next
	d.expiresAt = expiresAt
	return d.resolved, nil
}

func NewDialerFromMapping(mapping map[string]any) (Dialer, error) {
	relay, err := adapter.ParseProxy(mapping)
	if err != nil {
		return nil, err
	}
	return &RelayDialer{adapter: relay}, nil
}

func NewLazyRelayDialer(groupName string, mapping map[string]any, resolver RelayResolveFunc) (Dialer, error) {
	name, _ := mapping["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("relay missing name")
	}
	relayType, _ := mapping["type"].(string)
	if relayType == "" {
		return nil, fmt.Errorf("relay %q missing type", name)
	}
	server, _ := mapping["server"].(string)
	addr := server
	if port, ok := mapping["port"]; ok {
		addr = fmt.Sprintf("%s:%v", server, port)
	}
	return &LazyRelayDialer{
		groupName: groupName,
		name:      name,
		relayType: relayType,
		addr:      addr,
		mapping:   cloneRelayMapping(mapping),
		resolver:  resolver,
	}, nil
}

func cloneRelayMapping(mapping map[string]any) map[string]any {
	cloned := make(map[string]any, len(mapping))
	for key, value := range mapping {
		cloned[key] = value
	}
	return cloned
}

// connWrapper wraps mihomo's C.Conn to implement net.Conn
type connWrapper struct {
	net.Conn
}

// packetConnWrapper wraps a PacketConn to behave as a net.Conn for UDP
type packetConnWrapper struct {
	net.PacketConn
	addr string
}

func (p *packetConnWrapper) Read(b []byte) (int, error) {
	n, _, err := p.PacketConn.ReadFrom(b)
	return n, err
}

func (p *packetConnWrapper) Write(b []byte) (int, error) {
	addr, err := net.ResolveUDPAddr("udp", p.addr)
	if err != nil {
		return 0, err
	}
	return p.PacketConn.WriteTo(b, addr)
}

func (p *packetConnWrapper) RemoteAddr() net.Addr {
	addr, _ := net.ResolveUDPAddr("udp", p.addr)
	return addr
}
