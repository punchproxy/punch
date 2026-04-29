package dns

import (
	"net"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func TestCacheGetReturnsCopyWithAdjustedTTL(t *testing.T) {
	cache := NewCache(2, 0, 60)
	msg := cacheTestAResponse("example.com.", "203.0.113.1", 120)

	cache.Put("example.com", mdns.TypeA, msg)
	msg.Answer[0].Header().Ttl = 1

	backdateCacheEntry(t, cache, "example.com", mdns.TypeA, 10*time.Second, 50*time.Second)

	got, stale := cache.Get("example.com", mdns.TypeA)
	if stale {
		t.Fatal("Get() stale = true, want false")
	}
	if got == nil || len(got.Answer) != 1 {
		t.Fatalf("Get() returned %#v, want one answer", got)
	}
	ttl := got.Answer[0].Header().Ttl
	if ttl > 110 || ttl < 108 {
		t.Fatalf("Get() TTL = %d, want about 110", ttl)
	}

	got.Answer[0].Header().Ttl = 7
	again, _ := cache.Get("example.com", mdns.TypeA)
	if again.Answer[0].Header().Ttl == 7 {
		t.Fatal("Get() returned cached message by reference, want a copy")
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := NewCache(2, 0, 60)
	var events []CacheEvent
	cache.OnChange(func(ev CacheEvent) {
		events = append(events, ev)
	})

	cache.Put("a.example", mdns.TypeA, cacheTestAResponse("a.example.", "203.0.113.1", 60))
	cache.Put("b.example", mdns.TypeA, cacheTestAResponse("b.example.", "203.0.113.2", 60))
	if _, stale := cache.Get("a.example", mdns.TypeA); stale {
		t.Fatal("a.example unexpectedly stale")
	}
	cache.Put("c.example", mdns.TypeA, cacheTestAResponse("c.example.", "203.0.113.3", 60))

	if got, _ := cache.Get("b.example", mdns.TypeA); got != nil {
		t.Fatal("b.example remained in cache after LRU eviction")
	}
	if got, _ := cache.Get("a.example", mdns.TypeA); got == nil {
		t.Fatal("a.example was evicted despite recent access")
	}
	if got, _ := cache.Get("c.example", mdns.TypeA); got == nil {
		t.Fatal("c.example was not cached")
	}

	if !hasCacheDeleteEvent(events, "b.example", "A") {
		t.Fatalf("events = %+v, want delete event for b.example A", events)
	}

	snapshot := cache.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("Snapshot() length = %d, want 2", len(snapshot))
	}
	if snapshot[0].Name != "c.example" || snapshot[1].Name != "a.example" {
		t.Fatalf("Snapshot() order = [%s, %s], want [c.example, a.example]", snapshot[0].Name, snapshot[1].Name)
	}
}

func TestCacheStaleAndLazyExpiry(t *testing.T) {
	cache := NewCache(2, 0, 10)
	var events []CacheEvent
	cache.OnChange(func(ev CacheEvent) {
		events = append(events, ev)
	})

	cache.Put("stale.example", mdns.TypeA, cacheTestAResponse("stale.example.", "203.0.113.10", 60))
	setCacheEntryTimes(t, cache, "stale.example", mdns.TypeA, time.Now().Add(-2*time.Second), time.Now().Add(-1*time.Second))

	got, stale := cache.Get("stale.example", mdns.TypeA)
	if got == nil {
		t.Fatal("Get() returned nil for stale entry inside lazy window")
	}
	if !stale {
		t.Fatal("Get() stale = false, want true")
	}

	setCacheEntryTimes(t, cache, "stale.example", mdns.TypeA, time.Now().Add(-12*time.Second), time.Now().Add(-11*time.Second))
	got, stale = cache.Get("stale.example", mdns.TypeA)
	if got != nil || stale {
		t.Fatalf("Get() = (%v, %v), want expired miss", got, stale)
	}
	if cache.Size() != 0 {
		t.Fatalf("Size() = %d, want 0 after lazy expiry removal", cache.Size())
	}
	if !hasCacheDeleteEvent(events, "stale.example", "A") {
		t.Fatalf("events = %+v, want delete event for lazy-expired stale.example A", events)
	}
}

func TestCacheSnapshotPrunesExpiredEntries(t *testing.T) {
	cache := NewCache(2, 0, 1)
	var events []CacheEvent
	cache.OnChange(func(ev CacheEvent) {
		events = append(events, ev)
	})

	cache.Put("expired.example", mdns.TypeA, cacheTestAResponse("expired.example.", "203.0.113.20", 60))
	setCacheEntryTimes(t, cache, "expired.example", mdns.TypeA, time.Now().Add(-3*time.Second), time.Now().Add(-2*time.Second))

	snapshot := cache.Snapshot()
	if len(snapshot) != 0 {
		t.Fatalf("Snapshot() length = %d, want 0", len(snapshot))
	}
	if cache.Size() != 0 {
		t.Fatalf("Size() = %d, want 0", cache.Size())
	}
	if !hasCacheDeleteEvent(events, "expired.example", "A") {
		t.Fatalf("events = %+v, want delete event for pruned expired.example A", events)
	}
}

func TestCacheFlushClearsEntriesAndPublishesFlush(t *testing.T) {
	cache := NewCache(2, 0, 60)
	var events []CacheEvent
	cache.OnChange(func(ev CacheEvent) {
		events = append(events, ev)
	})

	cache.Put("a.example", mdns.TypeA, cacheTestAResponse("a.example.", "203.0.113.1", 60))
	cache.Flush()

	if cache.Size() != 0 {
		t.Fatalf("Size() = %d, want 0 after Flush()", cache.Size())
	}
	if len(events) == 0 || events[len(events)-1].Op != CacheEventFlush {
		t.Fatalf("events = %+v, want final flush event", events)
	}
}

func cacheTestAResponse(name, ip string, ttl uint32) *mdns.Msg {
	msg := new(mdns.Msg)
	msg.SetReply(&mdns.Msg{
		Question: []mdns.Question{{
			Name:   name,
			Qtype:  mdns.TypeA,
			Qclass: mdns.ClassINET,
		}},
	})
	msg.Answer = []mdns.RR{&mdns.A{
		Hdr: mdns.RR_Header{
			Name:   name,
			Rrtype: mdns.TypeA,
			Class:  mdns.ClassINET,
			Ttl:    ttl,
		},
		A: net.ParseIP(ip).To4(),
	}}
	return msg
}

func backdateCacheEntry(t *testing.T, cache *Cache, name string, qtype uint16, storedAgo, expiresIn time.Duration) {
	t.Helper()
	now := time.Now()
	setCacheEntryTimes(t, cache, name, qtype, now.Add(-storedAgo), now.Add(expiresIn))
}

func setCacheEntryTimes(t *testing.T, cache *Cache, name string, qtype uint16, storedAt, expireAt time.Time) {
	t.Helper()
	cache.mu.Lock()
	defer cache.mu.Unlock()

	value, ok := cache.entries.Peek(cacheKey(name, qtype))
	if !ok {
		t.Fatalf("cache entry %s not found", cacheKey(name, qtype))
	}
	entry := value.(*cacheEntry)
	entry.storedAt = storedAt
	entry.expireAt = expireAt
}

func hasCacheDeleteEvent(events []CacheEvent, name, qtype string) bool {
	for _, ev := range events {
		if ev.Op == CacheEventDelete && ev.Name == name && ev.QType == qtype {
			return true
		}
	}
	return false
}
