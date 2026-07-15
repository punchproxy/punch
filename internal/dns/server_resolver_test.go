package dns

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
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
		ruleLists:     make(map[string][]*ruleListEntry),
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

func TestResolveRelayDomainUsesConfiguredUpstreamDomains(t *testing.T) {
	defaultAddr, closeDefault, defaultRequests := startSlowDNSUpstream(t, 10*time.Millisecond)
	defer closeDefault()
	scopedAddr, closeScoped, scopedRequests := startSlowDNSUpstream(t, 10*time.Millisecond)
	defer closeScoped()

	server := &Server{
		cache: NewCache(10, 0, 60),
		resolver: NewResolverGroup([]*UpstreamResolver{
			NewUpstreamResolver(defaultAddr, ""),
			NewUpstreamResolver(scopedAddr, "", "scoped.example"),
		}),
		domainMatcher: dnsrule.NewMatcher(),
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]*ruleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	got, _, err := server.ResolveRelayDomain(context.Background(), "main", "relay.scoped.example")
	if err != nil {
		t.Fatalf("ResolveRelayDomain() error = %v", err)
	}
	want := netip.MustParseAddr("203.0.113.99")
	if len(got) == 0 || got[0] != want {
		t.Fatalf("ResolveRelayDomain() = %v, want first %s", got, want)
	}
	if scopedRequests.Load() == 0 {
		t.Fatal("domain-specific upstream was not queried")
	}
	if defaultRequests.Load() != 0 {
		t.Fatalf("default upstream queries = %d, want 0", defaultRequests.Load())
	}
}

func TestResolveRelayDomainReturnsRealIPsAndUsesCache(t *testing.T) {
	upstreamAddr, closeUpstream, counts := startRelayDNSUpstream(t, map[uint16][]netip.Addr{
		mdns.TypeA:    {netip.MustParseAddr("203.0.113.40")},
		mdns.TypeAAAA: {netip.MustParseAddr("2001:db8::40")},
	})
	defer closeUpstream()

	fakePool, err := fakeip.NewDualStack("198.18.0.0/16", "fdfe:dcba:9876::/64", time.Hour)
	if err != nil {
		t.Fatalf("fakeip.NewDualStack() error = %v", err)
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
		ruleLists:     make(map[string][]*ruleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	first, _, err := server.ResolveRelayDomain(context.Background(), "main", "host.relay.example")
	if err != nil {
		t.Fatalf("first ResolveRelayDomain() error = %v", err)
	}
	want := map[netip.Addr]bool{
		netip.MustParseAddr("203.0.113.40"): true,
		netip.MustParseAddr("2001:db8::40"): true,
	}
	for _, ip := range first {
		delete(want, ip)
	}
	if len(want) != 0 {
		t.Fatalf("first ResolveRelayDomain() = %v, missing %v", first, want)
	}
	if fakePool.Size() != 0 {
		t.Fatalf("fake IP pool size = %d, want 0", fakePool.Size())
	}
	if counts.a.Load() != 1 || counts.aaaa.Load() != 1 {
		t.Fatalf("upstream queries after first resolve: A=%d AAAA=%d, want 1 each", counts.a.Load(), counts.aaaa.Load())
	}

	second, _, err := server.ResolveRelayDomain(context.Background(), "main", "host.relay.example")
	if err != nil {
		t.Fatalf("second ResolveRelayDomain() error = %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("second ResolveRelayDomain() = %v, want same length as %v", second, first)
	}
	if counts.a.Load() != 1 || counts.aaaa.Load() != 1 {
		t.Fatalf("upstream queries after cached resolve: A=%d AAAA=%d, want still 1 each", counts.a.Load(), counts.aaaa.Load())
	}
}

func TestResolveRelayDomainUsesStaleCacheWhenUpstreamFails(t *testing.T) {
	upstreamAddr, closeUpstream, _ := startRelayDNSUpstream(t, map[uint16][]netip.Addr{
		mdns.TypeA: {netip.MustParseAddr("203.0.113.40")},
	})

	cache := NewCache(10, 0, 60)
	cache.Put("host.relay.example", mdns.TypeA, cacheTestAResponse("host.relay.example.", "203.0.113.40", 60), upstreamAddr)
	setCacheEntryTimes(t, cache, "host.relay.example", mdns.TypeA, time.Now().Add(-2*time.Second), time.Now().Add(-1*time.Second))
	closeUpstream()

	server := &Server{
		cache:         cache,
		resolver:      NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")}),
		domainMatcher: dnsrule.NewMatcher(),
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]*ruleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, _, err := server.ResolveRelayDomain(ctx, "main", "host.relay.example")
	if err != nil {
		t.Fatalf("ResolveRelayDomain() error = %v", err)
	}
	want := netip.MustParseAddr("203.0.113.40")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ResolveRelayDomain() = %v, want [%s]", got, want)
	}
}

func TestResolveRelayDomainFloorsExpiryAtCacheMinTTL(t *testing.T) {
	upstreamAddr, closeUpstream, _ := startRelayDNSUpstreamWithTTL(t, map[uint16][]netip.Addr{
		mdns.TypeA: {netip.MustParseAddr("203.0.113.40")},
	}, 0)
	defer closeUpstream()

	server := &Server{
		cache:         NewCache(10, 60, 60),
		resolver:      NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")}),
		domainMatcher: dnsrule.NewMatcher(),
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]*ruleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	before := time.Now()
	got, expiresAt, err := server.ResolveRelayDomain(context.Background(), "main", "host.relay.example")
	if err != nil {
		t.Fatalf("ResolveRelayDomain() error = %v", err)
	}
	want := netip.MustParseAddr("203.0.113.40")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ResolveRelayDomain() = %v, want [%s]", got, want)
	}
	if expiresAt.IsZero() {
		t.Fatal("ResolveRelayDomain() expiry is zero, want floor at cache min TTL")
	}
	if min, max := before.Add(59*time.Second), time.Now().Add(61*time.Second); expiresAt.Before(min) || expiresAt.After(max) {
		t.Fatalf("ResolveRelayDomain() expiry = %v, want within [%v, %v]", expiresAt, min, max)
	}
}

func TestResolveRelayDomainQueriesIPv4AndIPv6BeforeFailing(t *testing.T) {
	upstreamAddr, closeUpstream, counts := startRelayDNSUpstream(t, nil)
	defer closeUpstream()

	server := &Server{
		cache:         NewCache(10, 0, 60),
		resolver:      NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")}),
		domainMatcher: dnsrule.NewMatcher(),
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]*ruleListEntry),
		refreshing:    make(map[string]struct{}),
	}

	_, _, err := server.ResolveRelayDomain(context.Background(), "main", "missing.example")
	if err == nil {
		t.Fatal("ResolveRelayDomain() error = nil, want failure")
	}
	if counts.a.Load() != 1 || counts.aaaa.Load() != 1 {
		t.Fatalf("upstream queries: A=%d AAAA=%d, want 1 each", counts.a.Load(), counts.aaaa.Load())
	}
}

type relayDNSCounts struct {
	a    atomic.Int64
	aaaa atomic.Int64
}

func startRelayDNSUpstream(t *testing.T, answers map[uint16][]netip.Addr) (addr string, closeFn func(), counts *relayDNSCounts) {
	t.Helper()
	return startRelayDNSUpstreamWithTTL(t, answers, 60)
}

func startRelayDNSUpstreamWithTTL(t *testing.T, answers map[uint16][]netip.Addr, ttl uint32) (addr string, closeFn func(), counts *relayDNSCounts) {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}

	counts = &relayDNSCounts{}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1500)
		for {
			n, clientAddr, err := conn.ReadFrom(buf)
			if err != nil {
				select {
				case <-done:
					return
				default:
					return
				}
			}

			req := new(mdns.Msg)
			if err := req.Unpack(buf[:n]); err != nil {
				continue
			}

			resp := new(mdns.Msg)
			resp.SetReply(req)
			resp.RecursionAvailable = true
			if len(req.Question) > 0 {
				q := req.Question[0]
				switch q.Qtype {
				case mdns.TypeA:
					counts.a.Add(1)
					for _, addr := range answers[mdns.TypeA] {
						if !addr.Is4() {
							continue
						}
						ip := addr.As4()
						resp.Answer = append(resp.Answer, &mdns.A{
							Hdr: mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: ttl},
							A:   net.IP(ip[:]),
						})
					}
				case mdns.TypeAAAA:
					counts.aaaa.Add(1)
					for _, addr := range answers[mdns.TypeAAAA] {
						if !addr.Is6() {
							continue
						}
						ip := addr.As16()
						resp.Answer = append(resp.Answer, &mdns.AAAA{
							Hdr:  mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeAAAA, Class: mdns.ClassINET, Ttl: ttl},
							AAAA: net.IP(ip[:]),
						})
					}
				}
			}

			packed, err := resp.Pack()
			if err != nil {
				continue
			}
			_, _ = conn.WriteTo(packed, clientAddr)
		}
	}()

	return conn.LocalAddr().String(), func() {
		close(done)
		_ = conn.Close()
	}, counts
}
