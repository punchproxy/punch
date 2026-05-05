package dns

import (
	"context"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/fakeip"
)

func BenchmarkCacheGetHit(b *testing.B) {
	cache := NewCache(10, 0, 60)
	cache.Put("example.com", mdns.TypeA, cacheTestAResponse("example.com.", "203.0.113.1", 60), "bench")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		msg, stale := cache.Get("example.com", mdns.TypeA)
		if msg == nil || stale {
			b.Fatalf("Get() = (%v, %v), want live hit", msg, stale)
		}
	}
}

func BenchmarkCacheLookupHitMinTTL(b *testing.B) {
	cache := NewCache(10, 0, 60)
	cache.Put("example.com", mdns.TypeA, cacheTestAResponse("example.com.", "203.0.113.1", 60), "bench")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		hit, ok := cache.lookup("example.com", mdns.TypeA)
		if !ok || hit.answerMinTTL() == 0 {
			b.Fatalf("lookup() = (%v, %v), want live hit with ttl", hit, ok)
		}
	}
}

func BenchmarkServerCachedRelayClassification(b *testing.B) {
	fakePool, err := fakeip.New("198.18.0.0/24", time.Hour)
	if err != nil {
		b.Fatalf("fakeip.New() error = %v", err)
	}

	cache := NewCache(10, 0, 60)
	cache.Put("proxy.example", mdns.TypeA, cacheTestAResponse("proxy.example.", "203.0.113.1", 60), "bench")
	server := &Server{
		fakeIPPool: fakePool,
		cache:      cache,
		directIPs:  NewIPSet(),
		ruleLists:  make(map[string][]RuleListEntry),
	}
	query := new(mdns.Msg)
	query.SetQuestion("proxy.example.", mdns.TypeA)

	resp, decision, _, _ := server.resolveAndClassifyWithResolver(context.Background(), query, "proxy.example", mdns.TypeA, false, nil, false)
	if resp == nil || decision != DecisionRelay {
		b.Fatalf("warmup decision = %s, resp = %v; want relay response", decision, resp)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, decision, _, _ := server.resolveAndClassifyWithResolver(context.Background(), query, "proxy.example", mdns.TypeA, false, nil, false)
		if resp == nil || decision != DecisionRelay {
			b.Fatalf("decision = %s, resp = %v; want relay response", decision, resp)
		}
	}
}
