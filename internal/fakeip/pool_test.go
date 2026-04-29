package fakeip

import (
	"net/netip"
	"reflect"
	"testing"
	"time"
)

func TestNewRejectsSmallPrefix(t *testing.T) {
	if _, err := New("198.18.0.0/29", time.Hour); err == nil {
		t.Fatal("New(/29) error = nil, want error")
	}
	if _, err := New("198.18.0.0/24", time.Hour); err != nil {
		t.Fatalf("New(/24) error = %v, want nil", err)
	}
}

func TestSnapshotSortedByIP(t *testing.T) {
	pool, err := New("198.18.0.0/15", time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ipB := pool.Lookup("b.example")
	ipA := pool.Lookup("a.example")

	snapshot := pool.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("Snapshot() length = %d, want 2", len(snapshot))
	}
	if snapshot[0].IP != ipB || snapshot[0].Domain != "b.example" {
		t.Fatalf("Snapshot()[0] = %+v, want %s -> b.example", snapshot[0], ipB)
	}
	if snapshot[1].IP != ipA || snapshot[1].Domain != "a.example" {
		t.Fatalf("Snapshot()[1] = %+v, want %s -> a.example", snapshot[1], ipA)
	}
}

func TestLookupResultReportsCreationAndEviction(t *testing.T) {
	pool, err := New("198.18.0.0/24", time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first := pool.LookupResult("a.example")
	if !first.Created || first.Evicted != nil {
		t.Fatalf("first lookup = %+v, want Created=true Evicted=nil", first)
	}
	reused := pool.LookupResult("a.example")
	if reused.Created {
		t.Fatalf("repeat lookup Created = true, want false")
	}
	// Force the next allocation to wrap back to the slot a.example occupies.
	pool.mu.Lock()
	pool.nextOff = pool.firstOff
	pool.cycled = true
	pool.mu.Unlock()

	second := pool.LookupResult("b.example")
	if !second.Created || second.Evicted == nil || second.Evicted.Domain != "a.example" {
		t.Fatalf("second lookup = %+v, want eviction of a.example", second)
	}
}

func TestUsesConfiguredTTL(t *testing.T) {
	pool, err := New("198.18.0.0/24", 2*time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	before := time.Now()
	result := pool.LookupResult("ttl.example")
	ttl := result.Mapping.ExpiresAt.Sub(before)
	if ttl < 2*time.Hour-time.Second || ttl > 2*time.Hour+time.Second {
		t.Fatalf("ttl = %s, want about 2h", ttl)
	}
}

func TestAllocateSkipsBroadcastAddresses(t *testing.T) {
	pool, err := New("198.18.0.0/16", time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// Position the cursor right at the first broadcast slot (offset 255 → 198.18.0.255).
	pool.mu.Lock()
	pool.nextOff = 255
	pool.mu.Unlock()
	ip := pool.Lookup("first.example")
	if ip.As4()[3] == 255 {
		t.Fatalf("allocated broadcast address %s", ip)
	}
	// The broadcast slot is skipped; next allocation lands at 198.18.1.0.
	want := netip.MustParseAddr("198.18.1.0")
	if ip != want {
		t.Fatalf("Lookup() = %s, want %s", ip, want)
	}
}

func TestAcquireReleaseTracksSessionIDs(t *testing.T) {
	pool, err := New("198.18.0.0/24", time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := pool.Lookup("session.example")

	if !pool.Acquire(ip, "sess-A") || !pool.Acquire(ip, "sess-B") {
		t.Fatal("Acquire() returned false on valid mapping")
	}
	if got := pool.Sessions(ip); !reflect.DeepEqual(got, []string{"sess-A", "sess-B"}) {
		t.Fatalf("Sessions() = %v, want [sess-A sess-B]", got)
	}
	snap := pool.Snapshot()
	if len(snap) != 1 || !snap[0].Active() {
		t.Fatalf("Snapshot() = %+v, want one active mapping", snap)
	}
	if !reflect.DeepEqual(snap[0].SessionIDs, []string{"sess-A", "sess-B"}) {
		t.Fatalf("Snapshot SessionIDs = %v, want [sess-A sess-B]", snap[0].SessionIDs)
	}

	pool.Release(ip, "sess-A")
	if got := pool.Sessions(ip); !reflect.DeepEqual(got, []string{"sess-B"}) {
		t.Fatalf("Sessions() after release = %v, want [sess-B]", got)
	}
	pool.Release(ip, "sess-B")
	if got := pool.Sessions(ip); got != nil {
		t.Fatalf("Sessions() after final release = %v, want nil", got)
	}
}

func TestActiveMappingNotPruned(t *testing.T) {
	pool, err := New("198.18.0.0/24", time.Millisecond)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := pool.Lookup("pin.example")
	pool.Acquire(ip, "sess-1")

	time.Sleep(5 * time.Millisecond)
	// Trigger a prune via a write path.
	pool.Lookup("other.example")

	if _, ok := pool.LookBack(ip); !ok {
		t.Fatal("active mapping was pruned despite session pin")
	}
}
