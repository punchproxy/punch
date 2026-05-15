package mihomo

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	mresolver "github.com/metacubex/mihomo/component/resolver"
	mdns "github.com/miekg/dns"
	pdns "github.com/punchproxy/punch/internal/dns"
)

func TestRegisterDNSRestoresPreviousGlobals(t *testing.T) {
	previousService := mresolver.DefaultService
	previousDefaultResolver := mresolver.DefaultResolver
	previousProxyServerHostResolver := mresolver.ProxyServerHostResolver
	previousDirectHostResolver := mresolver.DirectHostResolver
	t.Cleanup(func() {
		mresolver.DefaultService = previousService
		mresolver.DefaultResolver = previousDefaultResolver
		mresolver.ProxyServerHostResolver = previousProxyServerHostResolver
		mresolver.DirectHostResolver = previousDirectHostResolver
	})

	oldService := &stubService{}
	oldResolver := &stubResolver{}
	mresolver.DefaultService = oldService
	mresolver.DefaultResolver = oldResolver
	mresolver.ProxyServerHostResolver = oldResolver
	mresolver.DirectHostResolver = oldResolver

	newService := &stubService{}
	unregister := RegisterDNS(newService, pdns.NewServerResolver(nil))

	if mresolver.DefaultService != newService {
		t.Fatal("DefaultService was not registered")
	}
	if mresolver.DefaultResolver == nil ||
		mresolver.ProxyServerHostResolver != mresolver.DefaultResolver ||
		mresolver.DirectHostResolver != mresolver.DefaultResolver {
		t.Fatal("resolver globals were not registered consistently")
	}

	unregister()

	if mresolver.DefaultService != oldService {
		t.Fatal("DefaultService was not restored")
	}
	if mresolver.DefaultResolver != oldResolver ||
		mresolver.ProxyServerHostResolver != oldResolver ||
		mresolver.DirectHostResolver != oldResolver {
		t.Fatal("resolver globals were not restored")
	}
}

func TestTranslateResolverErrors(t *testing.T) {
	if got := translateResolverError(nil); got != nil {
		t.Fatalf("nil error translated to %v", got)
	}
	if got := translateResolverError(pdns.ErrIPNotFound); !errors.Is(got, mresolver.ErrIPNotFound) {
		t.Fatalf("ErrIPNotFound translated to %v", got)
	}
	if got := translateResolverError(pdns.ErrIPVersion); !errors.Is(got, mresolver.ErrIPVersion) {
		t.Fatalf("ErrIPVersion translated to %v", got)
	}

	other := errors.New("other")
	if got := translateResolverError(other); got != other {
		t.Fatalf("unexpected translation: %v", got)
	}
}

type stubService struct{}

func (s *stubService) ServeMsg(context.Context, *mdns.Msg) (*mdns.Msg, error) {
	return nil, nil
}

type stubResolver struct{}

func (r *stubResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return nil, nil
}

func (r *stubResolver) LookupIPv4(context.Context, string) ([]netip.Addr, error) {
	return nil, nil
}

func (r *stubResolver) LookupIPv6(context.Context, string) ([]netip.Addr, error) {
	return nil, nil
}

func (r *stubResolver) ResolveECH(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r *stubResolver) ExchangeContext(context.Context, *mdns.Msg) (*mdns.Msg, error) {
	return nil, nil
}

func (r *stubResolver) Invalid() bool { return false }

func (r *stubResolver) ClearCache() {}

func (r *stubResolver) ResetConnection() {}
