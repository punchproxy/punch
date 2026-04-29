package dns

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/dnsrule"
	"github.com/punchproxy/punch/internal/fakeip"
)

func TestServerResolverUsesServerPathWithoutFakeIP(t *testing.T) {
	upstreamAddr, closeUpstream, _ := startSlowDNSUpstream(t, 10*time.Millisecond)
	defer closeUpstream()

	fakePool, err := fakeip.New("198.18.0.0/16", time.Hour)
	if err != nil {
		t.Fatalf("fakeip.New() error = %v", err)
	}
	domainMatcher := dnsrule.NewMatcher()
	if err := domainMatcher.AddRule("domain:relay.example", config.DecisionRelay, 0); err != nil {
		t.Fatalf("AddRule() error = %v", err)
	}

	server := &Server{
		fakeIPPool:    fakePool,
		cache:         NewCache(10, 0, 60),
		resolver:      NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")}),
		domainMatcher: domainMatcher,
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]RuleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	resolver := NewServerResolver(server)
	got, err := resolver.LookupIPv4(context.Background(), "www.relay.example")
	if err != nil {
		t.Fatalf("LookupIPv4() error = %v", err)
	}
	want := netip.MustParseAddr("203.0.113.99")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("LookupIPv4() = %v, want [%s]", got, want)
	}
	if fakePool.Size() != 0 {
		t.Fatalf("fake IP pool size = %d, want 0", fakePool.Size())
	}
}
