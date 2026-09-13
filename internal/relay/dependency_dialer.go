package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

// dependentDialer is a relay configuration that must be bound to an immutable
// upstream selection before dialing. Binding does not resolve names or dial.
type dependentDialer interface {
	Dialer
	DependencyGroup() string
	BindDependency(upstream Dialer, pathKey string) Dialer
}

// DialerProxyGroup reads a Punch group reference from a Mihomo proxy mapping.
func DialerProxyGroup(mapping map[string]any) (string, error) {
	value, exists := mapping["dialer-proxy"]
	if !exists {
		return "", nil
	}
	name, ok := value.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("relay %v dialer-proxy must be a nonempty group name", mapping["name"])
	}
	if strings.TrimSpace(name) != name {
		return "", fmt.Errorf("relay %v dialer-proxy group name must not have surrounding whitespace", mapping["name"])
	}
	return name, nil
}

type dependencyDialer struct {
	*LazyRelayDialer
	dependency string
	udp        bool
	bindMu     sync.Mutex
	bound      Dialer
	boundKey   string
}

// NewDependencyDialer validates a relay using a Punch group as its transport.
// The returned configuration cannot dial until the selector binds that group.
func NewDependencyDialer(groupName string, mapping map[string]any, resolver RelayResolveFunc) (Dialer, error) {
	dependency, err := DialerProxyGroup(mapping)
	if err != nil {
		return nil, err
	}
	if dependency == "" {
		return nil, fmt.Errorf("relay %v missing dialer-proxy", mapping["name"])
	}
	relayType, _ := mapping["type"].(string)
	// Only adapters whose transport uses BasicOption.NewDialer can honor the
	// injected group dialer. Tailscale also needs destinationless UDP sockets,
	// which cannot be represented by a selected relay's packet association.
	switch relayType {
	case "ss", "ssr", "socks5", "http", "vmess", "vless", "snell", "trojan",
		"hysteria", "hysteria2", "wireguard", "tuic", "shadowquic", "gost-relay",
		"ssh", "mieru", "anytls", "sudoku", "masque", "trusttunnel", "openvpn":
	default:
		return nil, fmt.Errorf("relay %v type %q does not support dialer-proxy", mapping["name"], relayType)
	}
	base, err := newLazyRelayDialer(groupName, mapping, resolver)
	if err != nil {
		return nil, err
	}
	validation, err := newDialerFromMapping(mapping, adapter.WithDialerForAPI(&groupTransportDialer{group: dependency}))
	if err != nil {
		return nil, err
	}
	udp := validation.SupportUDP()
	_ = validation.Close()
	return &dependencyDialer{
		LazyRelayDialer: base.(*LazyRelayDialer),
		dependency:      dependency,
		udp:             udp,
	}, nil
}

func (d *dependencyDialer) DependencyGroup() string { return d.dependency }
func (d *dependencyDialer) SupportUDP() bool        { return d.udp }

func (d *dependencyDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, fmt.Errorf("relay %s requires selected dialer-proxy group %q", d.Name(), d.dependency)
}

func (d *dependencyDialer) ListenPacketContext(context.Context, string, string) (net.PacketConn, error) {
	return nil, fmt.Errorf("relay %s requires selected dialer-proxy group %q", d.Name(), d.dependency)
}

func (d *dependencyDialer) BindDependency(upstream Dialer, pathKey string) Dialer {
	d.bindMu.Lock()
	defer d.bindMu.Unlock()
	if d.bound != nil && d.boundKey == pathKey {
		return d.bound
	}
	next := d.newBoundDependency(upstream)
	// Retain only the current generation here. Existing Mihomo connections keep
	// their adapter and its pinned upstream alive until they are closed.
	d.bound = next
	d.boundKey = pathKey
	return next
}

// BindDependencyForCheck keeps speculative recovery probes out of the active
// adapter cache, so a rejected alternative does not rotate a live pool.
func (d *dependencyDialer) BindDependencyForCheck(upstream Dialer, _ string) Dialer {
	return d.newBoundDependency(upstream)
}

func (d *dependencyDialer) newBoundDependency(upstream Dialer) Dialer {
	bridge := &groupTransportDialer{group: d.dependency, upstream: upstream}
	// The configuration was validated at construction. Adapter creation and DNS
	// lookup happen on first use, outside the selector lock.
	lazy, _ := newLazyRelayDialer(d.groupName, d.mapping, d.resolver, adapter.WithDialerForAPI(bridge))
	return &boundDependencyDialer{Dialer: lazy, udp: d.udp}
}

func (d *dependencyDialer) ResolvedAddr() string {
	d.bindMu.Lock()
	bound := d.bound
	d.bindMu.Unlock()
	if resolved, ok := bound.(interface{ ResolvedAddr() string }); ok {
		return resolved.ResolvedAddr()
	}
	return ""
}

func (d *dependencyDialer) Close() error {
	d.bindMu.Lock()
	bound := d.bound
	d.bound = nil
	d.boundKey = ""
	d.bindMu.Unlock()
	if bound != nil {
		return bound.Close()
	}
	return nil
}

type boundDependencyDialer struct {
	Dialer
	udp bool
}

func (d *boundDependencyDialer) SupportUDP() bool { return d.udp }

func (d *boundDependencyDialer) ResolvedAddr() string {
	if resolved, ok := d.Dialer.(interface{ ResolvedAddr() string }); ok {
		return resolved.ResolvedAddr()
	}
	return ""
}

func (d *boundDependencyDialer) ListenPacketContext(ctx context.Context, network, address string) (net.PacketConn, error) {
	return d.Dialer.(PacketDialer).ListenPacketContext(ctx, network, address)
}

// groupTransportDialer pins the same upstream for every socket opened by an
// adapter, including a SOCKS5 UDP association's TCP and UDP sockets.
type groupTransportDialer struct {
	group    string
	upstream Dialer
}

var _ C.Dialer = (*groupTransportDialer)(nil)

func (d *groupTransportDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.upstream == nil {
		return nil, fmt.Errorf("dialer-proxy group %q has no selected relay", d.group)
	}
	conn, err := d.upstream.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("dialer-proxy group %q: %w", d.group, err)
	}
	return conn, nil
}

func (d *groupTransportDialer) ListenPacket(ctx context.Context, network, _ string, remote netip.AddrPort) (net.PacketConn, error) {
	if d.upstream == nil {
		return nil, fmt.Errorf("dialer-proxy group %q has no selected relay", d.group)
	}
	if !remote.IsValid() || remote.Addr().IsUnspecified() || remote.Port() == 0 {
		return nil, fmt.Errorf("dialer-proxy group %q requires a remote UDP endpoint", d.group)
	}
	packetDialer, ok := d.upstream.(PacketDialer)
	if !ok {
		return nil, fmt.Errorf("dialer-proxy group %q relay %s does not support UDP transport: %w", d.group, d.upstream.Name(), errors.ErrUnsupported)
	}
	pc, err := packetDialer.ListenPacketContext(ctx, network, remote.String())
	if err != nil {
		return nil, fmt.Errorf("dialer-proxy group %q UDP transport: %w", d.group, err)
	}
	return pc, nil
}
