package dns

import (
	"context"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func TestResolverGroupUpstreamStats(t *testing.T) {
	upstreamAddr, closeUpstream, _ := startSlowDNSUpstream(t, 25*time.Millisecond)
	defer closeUpstream()

	resolver := NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")})
	stats := resolver.UpstreamStats()
	if len(stats) != 1 {
		t.Fatalf("UpstreamStats() length = %d, want 1", len(stats))
	}
	if stats[0].URL != upstreamAddr {
		t.Fatalf("UpstreamStats()[0].URL = %q, want %q", stats[0].URL, upstreamAddr)
	}
	if stats[0].Queries != 0 {
		t.Fatalf("UpstreamStats()[0].Queries = %d, want 0 before resolving", stats[0].Queries)
	}

	msg := new(mdns.Msg)
	msg.SetQuestion("stats.example.", mdns.TypeA)
	for i := 0; i < 2; i++ {
		if _, err := resolver.Resolve(context.Background(), msg.Copy()); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}

	stats = resolver.UpstreamStats()
	if stats[0].Queries != 2 {
		t.Fatalf("UpstreamStats()[0].Queries = %d, want 2", stats[0].Queries)
	}
	if stats[0].AverageLatency <= 0 {
		t.Fatalf("UpstreamStats()[0].AverageLatency = %d, want > 0", stats[0].AverageLatency)
	}
	if stats[0].LastLatency <= 0 {
		t.Fatalf("UpstreamStats()[0].LastLatency = %d, want > 0", stats[0].LastLatency)
	}
	if stats[0].LastQueriedAt == "" {
		t.Fatal("UpstreamStats()[0].LastQueriedAt is empty after resolving")
	}
}

func TestResolverGroupQueriesCountAcceptedResponseOnly(t *testing.T) {
	fastAddr, closeFast, _ := startSlowDNSUpstream(t, 5*time.Millisecond)
	defer closeFast()
	slowAddr, closeSlow, _ := startSlowDNSUpstream(t, 200*time.Millisecond)
	defer closeSlow()

	fast := NewUpstreamResolver(fastAddr, "")
	slow := NewUpstreamResolver(slowAddr, "")
	resolver := NewResolverGroup([]*UpstreamResolver{fast, slow})

	msg := new(mdns.Msg)
	msg.SetQuestion("race.example.", mdns.TypeA)
	if _, err := resolver.Resolve(context.Background(), msg.Copy()); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	stats := resolver.UpstreamStats()
	statsByURL := map[string]UpstreamStats{stats[0].URL: stats[0], stats[1].URL: stats[1]}
	if got := statsByURL[fastAddr].Queries; got != 1 {
		t.Fatalf("fast upstream queries = %d, want 1 (the winning response)", got)
	}
	if got := statsByURL[slowAddr].Queries; got != 0 {
		t.Fatalf("slow upstream queries = %d, want 0 (response was discarded)", got)
	}
	if got := statsByURL[fastAddr].LastQueriedDomain; got != "race.example" {
		t.Fatalf("fast upstream LastQueriedDomain = %q, want race.example", got)
	}
	if statsByURL[slowAddr].LastQueriedDomain != "" {
		t.Fatalf("slow upstream LastQueriedDomain = %q, want empty", statsByURL[slowAddr].LastQueriedDomain)
	}
}

func TestResolverGroupSelectsDomainSpecificUpstream(t *testing.T) {
	defaultResolver := NewUpstreamResolver("default:53", "")
	googleResolver := NewUpstreamResolver("google:53", "", "google.com")
	fullResolver := NewUpstreamResolver("full:53", "", "full:exact.example")
	keywordResolver := NewUpstreamResolver("keyword:53", "", "keyword:needle")
	regexpResolver := NewUpstreamResolver("regexp:53", "", `regexp:.+\.regexp\.example$`)

	group := NewResolverGroup([]*UpstreamResolver{defaultResolver, googleResolver, fullResolver, keywordResolver, regexpResolver})

	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "bare domain normalized", host: "www.google.com.", want: "google:53"},
		{name: "full exact", host: "exact.example.", want: "full:53"},
		{name: "full excludes subdomain", host: "www.exact.example.", want: "default:53"},
		{name: "keyword", host: "api.needle.test.", want: "keyword:53"},
		{name: "regexp", host: "www.regexp.example.", want: "regexp:53"},
		{name: "fallback", host: "other.example.", want: "default:53"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := new(mdns.Msg)
			msg.SetQuestion(tt.host, mdns.TypeA)
			selected := group.selectUpstreams(msg)
			if len(selected) != 1 {
				t.Fatalf("selected length = %d, want 1", len(selected))
			}
			if selected[0].url != tt.want {
				t.Fatalf("selected upstream = %q, want %q", selected[0].url, tt.want)
			}
		})
	}
}

func TestUpstreamResolverBootstrapUsesCache(t *testing.T) {
	bootstrapAddr, closeBootstrap, requests := startSlowDNSUpstream(t, 0)
	defer closeBootstrap()

	cache := NewCache(10, 0, 60)
	resolver := NewUpstreamResolverWithCache("https://doh.example/dns-query", bootstrapAddr, cache)

	ips, err := resolver.resolveBootstrapHost(context.Background(), "doh.example")
	if err != nil {
		t.Fatalf("first resolveBootstrapHost() error = %v", err)
	}
	if len(ips) != 1 || ips[0] != "203.0.113.99" {
		t.Fatalf("first resolveBootstrapHost() = %v, want [203.0.113.99]", ips)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("bootstrap requests after first lookup = %d, want 1", got)
	}

	ips, err = resolver.resolveBootstrapHost(context.Background(), "doh.example")
	if err != nil {
		t.Fatalf("second resolveBootstrapHost() error = %v", err)
	}
	if len(ips) != 1 || ips[0] != "203.0.113.99" {
		t.Fatalf("second resolveBootstrapHost() = %v, want [203.0.113.99]", ips)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("bootstrap requests after cached lookup = %d, want 1", got)
	}

	snapshot := cache.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("Snapshot() length = %d, want 1", len(snapshot))
	}
	if snapshot[0].Name != "doh.example" || snapshot[0].QType != "A" || snapshot[0].Upstream != bootstrapAddr || snapshot[0].Result != "203.0.113.99" {
		t.Fatalf("Snapshot()[0] = %+v, want cached bootstrap A lookup", snapshot[0])
	}
}
