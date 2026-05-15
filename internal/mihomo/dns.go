package mihomo

import (
	"context"
	"errors"
	"net/netip"

	mresolver "github.com/metacubex/mihomo/component/resolver"
	mdns "github.com/miekg/dns"
	pdns "github.com/punchproxy/punch/internal/dns"
)

type dnsService interface {
	ServeMsg(ctx context.Context, msg *mdns.Msg) (*mdns.Msg, error)
}

// RegisterDNS wires Punch's DNS server into Mihomo's resolver globals.
// Keeping this in a bridge package prevents internal/dns from importing Mihomo.
func RegisterDNS(service dnsService, resolver *pdns.ServerResolver) func() {
	previousService := mresolver.DefaultService
	previousDefaultResolver := mresolver.DefaultResolver
	previousProxyServerHostResolver := mresolver.ProxyServerHostResolver
	previousDirectHostResolver := mresolver.DirectHostResolver

	if service != nil {
		mresolver.DefaultService = service
	}

	var adapter *resolverAdapter
	if resolver != nil {
		adapter = &resolverAdapter{resolver: resolver}
		mresolver.DefaultResolver = adapter
		mresolver.ProxyServerHostResolver = adapter
		mresolver.DirectHostResolver = adapter
	}

	return func() {
		if service != nil && mresolver.DefaultService == service {
			mresolver.DefaultService = previousService
		}
		if adapter != nil {
			if mresolver.DefaultResolver == adapter {
				mresolver.DefaultResolver = previousDefaultResolver
			}
			if mresolver.ProxyServerHostResolver == adapter {
				mresolver.ProxyServerHostResolver = previousProxyServerHostResolver
			}
			if mresolver.DirectHostResolver == adapter {
				mresolver.DirectHostResolver = previousDirectHostResolver
			}
		}
	}
}

type resolverAdapter struct {
	resolver *pdns.ServerResolver
}

func (r *resolverAdapter) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := r.resolver.LookupIP(ctx, host)
	return ips, translateResolverError(err)
}

func (r *resolverAdapter) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := r.resolver.LookupIPv4(ctx, host)
	return ips, translateResolverError(err)
}

func (r *resolverAdapter) LookupIPv6(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := r.resolver.LookupIPv6(ctx, host)
	return ips, translateResolverError(err)
}

func (r *resolverAdapter) ResolveECH(ctx context.Context, host string) ([]byte, error) {
	ech, err := r.resolver.ResolveECH(ctx, host)
	return ech, translateResolverError(err)
}

func (r *resolverAdapter) ExchangeContext(ctx context.Context, msg *mdns.Msg) (*mdns.Msg, error) {
	reply, err := r.resolver.ExchangeContext(ctx, msg)
	return reply, translateResolverError(err)
}

func (r *resolverAdapter) Invalid() bool {
	return r != nil && r.resolver != nil && r.resolver.Invalid()
}

func (r *resolverAdapter) ClearCache() {
	if r != nil && r.resolver != nil {
		r.resolver.ClearCache()
	}
}

func (r *resolverAdapter) ResetConnection() {
	if r != nil && r.resolver != nil {
		r.resolver.ResetConnection()
	}
}

func translateResolverError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pdns.ErrIPNotFound):
		return mresolver.ErrIPNotFound
	case errors.Is(err, pdns.ErrIPVersion):
		return mresolver.ErrIPVersion
	default:
		return err
	}
}
