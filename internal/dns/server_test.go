package dns

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
)

func TestServerQueryTraceDoesNotUseEventBus(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.CacheSize = 10
	cfg.DNS.FakeIPRange = "198.18.0.0/15"
	cfg.DNS.FakeIPTTL = "1h"
	cfg.DNS.Rules.Domains = []config.DomainRule{{Decision: config.DecisionReject, Source: "qtype:aaaa"}}
	useConfig(t, cfg)

	server, err := NewServer(nil)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.LoadInitialRules(); err != nil {
		t.Fatalf("LoadInitialRules() error = %v", err)
	}

	trace := make(chan QueryLog, 1)
	unsubscribe := server.SubscribeQueryLogs(trace)
	defer unsubscribe()

	query := new(mdns.Msg)
	query.SetQuestion("example.com.", mdns.TypeAAAA)
	_, decision, _, _, err := server.serveMsg(context.Background(), query, "test")
	if err != nil {
		t.Fatalf("serveMsg() error = %v", err)
	}
	if decision != DecisionReject {
		t.Fatalf("decision = %s, want %s", decision, DecisionReject)
	}

	select {
	case ql := <-trace:
		if ql.Domain != "example.com" || ql.QType != "AAAA" {
			t.Fatalf("trace query = %+v, want example.com AAAA", ql)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query trace")
	}

}

func TestServerStaleCacheRefreshDoesNotStampede(t *testing.T) {
	upstreamAddr, closeUpstream, requests := startSlowDNSUpstream(t, 250*time.Millisecond)
	defer closeUpstream()

	cache := NewCache(10, 0, 60)
	cache.Put("stale.example", mdns.TypeA, cacheTestAResponse("stale.example.", "203.0.113.1", 60))
	setCacheEntryTimes(t, cache, "stale.example", mdns.TypeA, time.Now().Add(-2*time.Second), time.Now().Add(-1*time.Second))

	server := &Server{
		cache:    cache,
		resolver: NewResolverGroup([]*UpstreamResolver{NewUpstreamResolver(upstreamAddr, "")}),
	}

	query := new(mdns.Msg)
	query.SetQuestion("stale.example.", mdns.TypeA)

	const callers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, upstream := server.resolveAndCacheWithResolver(nil, query.Copy(), "stale.example", mdns.TypeA, nil, false)
			if resp == nil {
				t.Error("resolveAndCacheWithResolver returned nil response")
				return
			}
			if upstream != "Cache (stale)" {
				t.Errorf("upstream = %q, want Cache (stale)", upstream)
			}
		}()
	}
	close(start)
	wg.Wait()

	waitFor(t, time.Second, func() bool {
		return requests.Load() >= 1
	})
	time.Sleep(100 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream refresh requests while first refresh in-flight = %d, want 1", got)
	}

	waitFor(t, time.Second, func() bool {
		got, stale := cache.Get("stale.example", mdns.TypeA)
		return got != nil && !stale && answerToString(got) == "203.0.113.99"
	})
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream refresh requests after refresh completion = %d, want 1", got)
	}
}

func TestServerAssetReadyReloadsOnlyAffectedRuleBucket(t *testing.T) {
	store, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("config.Open() error = %v", err)
	}
	defer store.Close()

	const directURL = "https://example.test/direct.txt"
	const rejectURL = "https://example.test/reject.txt"
	now := time.Now().UTC()
	if err := store.PutAsset(directURL, []byte("domain:direct.example\n"), now); err != nil {
		t.Fatalf("PutAsset(direct) error = %v", err)
	}
	if err := store.PutAsset(rejectURL, []byte("domain:ads.example\n"), now); err != nil {
		t.Fatalf("PutAsset(reject) error = %v", err)
	}
	manager, err := assets.New(store, 0, nil, nil)
	if err != nil {
		t.Fatalf("assets.New() error = %v", err)
	}

	cfg := &config.Config{}
	cfg.DNS.CacheSize = 10
	cfg.DNS.FakeIPRange = "198.18.0.0/15"
	cfg.DNS.FakeIPTTL = "1h"
	cfg.DNS.Rules.Domains = []config.DomainRule{
		{Decision: config.DecisionDirect, Source: directURL},
		{Decision: config.DecisionReject, Source: rejectURL},
	}
	useConfig(t, cfg)

	server, err := NewServer(manager)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.LoadInitialRules(); err != nil {
		t.Fatalf("LoadInitialRules() error = %v", err)
	}

	server.incrementRuleHit("direct-domains", directURL)
	if err := store.PutAsset(rejectURL, []byte("domain:ads.example\ndomain:tracker.example\n"), now.Add(time.Second)); err != nil {
		t.Fatalf("PutAsset(reject update) error = %v", err)
	}
	if err := server.reloadRuleSource(rejectURL); err != nil {
		t.Fatalf("reloadRuleSource() error = %v", err)
	}

	snapshot := server.RuleListSnapshot()
	if got := snapshot["direct-domains"][0].Hits; got != 1 {
		t.Fatalf("direct rule hits = %d, want preserved hit count 1", got)
	}
	if got := snapshot["direct-domains"][0].Count; got != 1 {
		t.Fatalf("direct rule count = %d, want 1", got)
	}
	if got := snapshot["reject-domains"][0].Count; got != 2 {
		t.Fatalf("reject rule count = %d, want updated count 2", got)
	}
	if source := server.domainMatchSource(config.DecisionReject, "www.tracker.example"); source != rejectURL {
		t.Fatalf("reject matcher source = %q, want %q", source, rejectURL)
	}
}

func TestServerDomainRulesUseConfiguredOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rules    []config.DomainRule
		decision Decision
	}{
		{
			name: "relay before reject",
			rules: []config.DomainRule{
				{Decision: config.DecisionRelay, Source: "domain:example.com"},
				{Decision: config.DecisionReject, Source: "domain:example.com"},
			},
			decision: DecisionRelay,
		},
		{
			name: "reject before relay",
			rules: []config.DomainRule{
				{Decision: config.DecisionReject, Source: "domain:example.com"},
				{Decision: config.DecisionRelay, Source: "domain:example.com"},
			},
			decision: DecisionReject,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.DNS.CacheSize = 10
			cfg.DNS.FakeIPRange = "198.18.0.0/15"
			cfg.DNS.FakeIPTTL = "1h"
			cfg.DNS.Rules.Domains = tc.rules
			useConfig(t, cfg)

			server, err := NewServer(nil)
			if err != nil {
				t.Fatalf("NewServer() error = %v", err)
			}
			if err := server.LoadInitialRules(); err != nil {
				t.Fatalf("LoadInitialRules() error = %v", err)
			}

			query := new(mdns.Msg)
			query.SetQuestion("www.example.com.", mdns.TypeA)
			_, decision, _, _, err := server.serveMsgWithOptions(context.Background(), query, "test", false, nil, false)
			if err != nil {
				t.Fatalf("serveMsgWithOptions() error = %v", err)
			}
			if decision != tc.decision {
				t.Fatalf("decision = %s, want %s", decision, tc.decision)
			}
		})
	}
}

func TestServerQTypeRulesUseConfiguredOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rules    []config.DomainRule
		decision Decision
	}{
		{
			name: "relay domain before reject qtype",
			rules: []config.DomainRule{
				{Decision: config.DecisionRelay, Source: "domain:example.com"},
				{Decision: config.DecisionReject, Source: "qtype:aaaa"},
			},
			decision: DecisionRelay,
		},
		{
			name: "reject qtype before relay domain",
			rules: []config.DomainRule{
				{Decision: config.DecisionReject, Source: "qtype:28"},
				{Decision: config.DecisionRelay, Source: "domain:example.com"},
			},
			decision: DecisionReject,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.DNS.CacheSize = 10
			cfg.DNS.FakeIPRange = "198.18.0.0/15"
			cfg.DNS.FakeIPTTL = "1h"
			cfg.DNS.Rules.Domains = tc.rules
			useConfig(t, cfg)

			server, err := NewServer(nil)
			if err != nil {
				t.Fatalf("NewServer() error = %v", err)
			}
			if err := server.LoadInitialRules(); err != nil {
				t.Fatalf("LoadInitialRules() error = %v", err)
			}

			query := new(mdns.Msg)
			query.SetQuestion("www.example.com.", mdns.TypeAAAA)
			_, decision, _, _, err := server.serveMsgWithOptions(context.Background(), query, "test", false, nil, false)
			if err != nil {
				t.Fatalf("serveMsgWithOptions() error = %v", err)
			}
			if decision != tc.decision {
				t.Fatalf("decision = %s, want %s", decision, tc.decision)
			}
		})
	}
}

func useConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open config store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close config store: %v", err)
		}
	})
	if err := config.Init(st); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := config.Replace(cfg); err != nil {
		t.Fatalf("replace config: %v", err)
	}
}

func startSlowDNSUpstream(t *testing.T, delay time.Duration) (addr string, closeFn func(), requests *atomic.Int64) {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}

	var count atomic.Int64
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
			count.Add(1)

			req := new(mdns.Msg)
			if err := req.Unpack(buf[:n]); err != nil {
				continue
			}
			time.Sleep(delay)

			resp := new(mdns.Msg)
			resp.SetReply(req)
			resp.RecursionAvailable = true
			resp.Answer = []mdns.RR{&mdns.A{
				Hdr: mdns.RR_Header{
					Name:   req.Question[0].Name,
					Rrtype: mdns.TypeA,
					Class:  mdns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP("203.0.113.99").To4(),
			}}
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
	}, &count
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
