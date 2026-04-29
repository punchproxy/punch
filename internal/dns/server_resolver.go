package dns

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	mresolver "github.com/metacubex/mihomo/component/resolver"

	mdns "github.com/miekg/dns"
)

// ServerResolver adapts Punch's DNS server to mihomo's resolver interface.
// Internal lookups use the same rule/cache/upstream path as external DNS
// queries, but disable fake-IP substitution so callers receive real addresses.
type ServerResolver struct {
	server *Server
	source string
}

func NewServerResolver(server *Server) *ServerResolver {
	return &ServerResolver{server: server, source: "internal"}
}

func (r *ServerResolver) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ipv4, err4 := r.LookupIPv4(ctx, host)
	ipv6, err6 := r.LookupIPv6(ctx, host)
	ips := append(ipv4, ipv6...)
	if len(ips) > 0 {
		return ips, nil
	}
	if err4 != nil {
		return nil, err4
	}
	if err6 != nil {
		return nil, err6
	}
	return nil, mresolver.ErrIPNotFound
}

func (r *ServerResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, mdns.TypeA)
}

func (r *ServerResolver) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.lookup(ctx, host, mdns.TypeAAAA)
}

func (r *ServerResolver) lookup(ctx context.Context, host string, qtype uint16) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if (qtype == mdns.TypeA && ip.Is4()) || (qtype == mdns.TypeAAAA && ip.Is6()) {
			return []netip.Addr{ip}, nil
		}
		return nil, mresolver.ErrIPVersion
	}

	msg := new(mdns.Msg)
	msg.SetQuestion(mdns.Fqdn(host), qtype)
	reply, err := r.ExchangeContext(ctx, msg)
	if err != nil {
		return nil, err
	}

	var ips []netip.Addr
	for _, rr := range reply.Answer {
		switch ans := rr.(type) {
		case *mdns.A:
			if qtype == mdns.TypeA {
				if ip, ok := netip.AddrFromSlice(ans.A); ok {
					ips = append(ips, ip.Unmap())
				}
			}
		case *mdns.AAAA:
			if qtype == mdns.TypeAAAA {
				if ip, ok := netip.AddrFromSlice(ans.AAAA); ok {
					ips = append(ips, ip.Unmap())
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, mresolver.ErrIPNotFound
	}
	return ips, nil
}

func (r *ServerResolver) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	msg := new(mdns.Msg)
	msg.SetQuestion(mdns.Fqdn(host), mdns.TypeHTTPS)
	reply, err := r.ExchangeContext(ctx, msg)
	if err != nil {
		return nil, err
	}
	for _, rr := range reply.Answer {
		if https, ok := rr.(*mdns.HTTPS); ok {
			for _, v := range https.Value {
				if ech, ok := v.(*mdns.SVCBECHConfig); ok {
					return ech.ECH, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("no ECH config found for %s", host)
}

func (r *ServerResolver) ExchangeContext(ctx context.Context, msg *mdns.Msg) (*mdns.Msg, error) {
	if r == nil || r.server == nil {
		return nil, fmt.Errorf("dns server resolver is not initialized")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, _, _, _, err := r.server.serveMsgWithOptions(timeoutCtx, msg.Copy(), r.source, true, nil, true)
	return reply, err
}

func (r *ServerResolver) Invalid() bool { return r != nil && r.server != nil }

func (r *ServerResolver) ClearCache() {
	if r != nil && r.server != nil {
		r.server.FlushCache()
	}
}

func (r *ServerResolver) ResetConnection() {}

func (r *ServerResolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, networkForAddr(network, ip), net.JoinHostPort(ip.String(), port))
	}

	ipv4, err4 := r.LookupIPv4(ctx, host)
	var dialErr4 error
	if len(ipv4) > 0 {
		conn, dialErr := dialSequential(ctx, network, port, ipv4)
		if dialErr == nil {
			return conn, nil
		}
		dialErr4 = dialErr
	}

	ipv6, err6 := r.LookupIPv6(ctx, host)
	if len(ipv6) > 0 {
		conn, dialErr := dialSequential(ctx, network, port, ipv6)
		if dialErr == nil {
			return conn, nil
		}
		if dialErr4 != nil {
			return nil, fmt.Errorf("ipv4: %w; ipv6: %v", dialErr4, dialErr)
		}
		return nil, dialErr
	}

	if dialErr4 != nil {
		return nil, dialErr4
	}
	if err4 != nil {
		return nil, err4
	}
	if err6 != nil {
		return nil, err6
	}
	return nil, fmt.Errorf("no addresses for %s", strings.TrimSpace(host))
}

func dialSequential(ctx context.Context, network, port string, ips []netip.Addr) (net.Conn, error) {
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, networkForAddr(network, ip), net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no dialable addresses")
}

func networkForAddr(network string, ip netip.Addr) string {
	switch network {
	case "tcp", "tcp4", "tcp6":
		if ip.Is4() {
			return "tcp4"
		}
		return "tcp6"
	case "udp", "udp4", "udp6":
		if ip.Is4() {
			return "udp4"
		}
		return "udp6"
	default:
		return network
	}
}
