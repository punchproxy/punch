package dns

import (
	"context"
	"slices"
	"testing"

	mdns "github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/config"
)

func TestResolverGroupSkipsDisabledUpstreams(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []config.Upstream
		host      string
		want      []string
	}{
		{
			name: "disabled default",
			upstreams: []config.Upstream{
				{URL: "disabled:53", Disabled: true},
				{URL: "active:53"},
			},
			host: "example.test", want: []string{"active:53"},
		},
		{
			name: "disabled domain match falls back to default",
			upstreams: []config.Upstream{
				{URL: "disabled:53", Domains: []string{"example.test"}, Disabled: true},
				{URL: "active:53"},
			},
			host: "example.test", want: []string{"active:53"},
		},
		{
			name: "unmatched domains use only active scoped upstreams",
			upstreams: []config.Upstream{
				{URL: "disabled:53", Domains: []string{"other.test"}, Disabled: true},
				{URL: "active:53", Domains: []string{"other.test"}},
			},
			host: "example.test", want: []string{"active:53"},
		},
		{
			name: "missing question",
			upstreams: []config.Upstream{
				{URL: "disabled:53", Disabled: true},
				{URL: "active:53", Domains: []string{"example.test"}},
			},
			want: []string{"active:53"},
		},
		{
			name: "all disabled",
			upstreams: []config.Upstream{
				{URL: "disabled:53", Disabled: true},
			},
			host: "example.test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := NewResolverGroup(buildUpstreams(tt.upstreams, nil))
			var msg *mdns.Msg
			if tt.host != "" {
				msg = new(mdns.Msg)
				msg.SetQuestion(mdns.Fqdn(tt.host), mdns.TypeA)
			}
			if got := group.selectedUpstreamURLs(msg); !slices.Equal(got, tt.want) {
				t.Fatalf("selected upstreams = %v, want %v", got, tt.want)
			}
			stats := group.UpstreamStats()
			if len(stats) != len(tt.upstreams) {
				t.Fatalf("stats length = %d, want %d", len(stats), len(tt.upstreams))
			}
			for i, upstream := range tt.upstreams {
				if stats[i].URL != upstream.URL || stats[i].Disabled != upstream.Disabled {
					t.Fatalf("stats[%d] = %+v, want URL %q disabled %v", i, stats[i], upstream.URL, upstream.Disabled)
				}
			}
		})
	}
}

func TestServerDisablesAndEnablesUpstream(t *testing.T) {
	addr, closeUpstream, requests := startSlowDNSUpstream(t, 0)
	defer closeUpstream()
	upstreams := []config.Upstream{{URL: addr, Disabled: true}}
	server := &Server{cache: NewCache(10, 0, 60)}
	server.UpdateUpstreams(upstreams)
	query := new(mdns.Msg)
	query.SetQuestion("example.test.", mdns.TypeA)
	server.cache.Put("example.test", mdns.TypeA, cacheTestAResponse("example.test.", "203.0.113.1", 60), addr)

	assertDisabled := func() {
		t.Helper()
		if _, err := server.currentResolver().Resolve(context.Background(), query); err == nil {
			t.Fatal("Resolve() succeeded with all upstreams disabled")
		}
		for _, respectTTL := range []bool{false, true} {
			resp, upstream, _ := server.resolveAndCacheWithResolver(context.Background(), query, "example.test", mdns.TypeA, nil, respectTTL)
			if resp != nil || upstream != "" {
				t.Fatalf("disabled resolution = (%v, %q), want no cached answer", resp, upstream)
			}
		}
		stats := server.UpstreamStats()
		if len(stats) != 1 || stats[0].URL != addr || !stats[0].Disabled {
			t.Fatalf("disabled upstream stats = %+v", stats)
		}
	}
	assertDisabled()
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests to disabled upstream = %d, want 0", got)
	}

	upstreams[0].Disabled = false
	server.UpdateUpstreams(upstreams)
	result, err := server.currentResolver().Resolve(context.Background(), query)
	if err != nil || result.Upstream != addr {
		t.Fatalf("enabled Resolve() = (%+v, %v), want upstream %q", result, err, addr)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after enabling = %d, want 1", got)
	}
	upstreams[0].Disabled = true
	server.UpdateUpstreams(upstreams)
	assertDisabled()
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after disabling again = %d, want 1", got)
	}
}
