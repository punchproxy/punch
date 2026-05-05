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
	if _, err := New("fdfe:dcba:9876::/121", time.Hour); err == nil {
		t.Fatal("New(IPv6 /121) error = nil, want error")
	}
	if _, err := New("fdfe:dcba:9876::/120", time.Hour); err != nil {
		t.Fatalf("New(IPv6 /120) error = %v, want nil", err)
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
	pool.ranges[FamilyIPv4].nextOff = pool.ranges[FamilyIPv4].firstOff
	pool.ranges[FamilyIPv4].cycled = true
	pool.mu.Unlock()

	second := pool.LookupResult("b.example")
	if !second.Created || second.Evicted == nil || second.Evicted.Domain != "a.example" {
		t.Fatalf("second lookup = %+v, want eviction of a.example", second)
	}
	if got := pool.LastLookupTime("a.example"); !got.IsZero() {
		t.Fatalf("LastLookupTime(a.example) after eviction = %s, want zero", got)
	}
}

func TestLastLookupTimeTracksRefresh(t *testing.T) {
	pool, err := New("198.18.0.0/24", time.Hour)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	pool.Lookup("lookup.example")
	first := pool.LastLookupTime("lookup.example")
	if first.IsZero() {
		t.Fatal("LastLookupTime() after lookup = zero, want timestamp")
	}

	time.Sleep(2 * time.Millisecond)
	pool.Lookup("lookup.example")
	second := pool.LastLookupTime("lookup.example")
	if !second.After(first) {
		t.Fatalf("LastLookupTime() after refresh = %s, want after %s", second, first)
	}
}

func TestLastLookupTimeClearedWhenMappingPruned(t *testing.T) {
	pool, err := New("198.18.0.0/24", time.Millisecond)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	pool.Lookup("expire.example")
	if got := pool.LastLookupTime("expire.example"); got.IsZero() {
		t.Fatal("LastLookupTime() after lookup = zero, want timestamp")
	}

	time.Sleep(5 * time.Millisecond)
	pool.Lookup("other.example")

	if got := pool.LastLookupTime("expire.example"); !got.IsZero() {
		t.Fatalf("LastLookupTime() after prune = %s, want zero", got)
	}
}

func TestLastLookupTimeFallsBackToRemainingFamilyAfterEviction(t *testing.T) {
	pool, err := NewDualStack("198.18.0.0/24", "fdfe:dcba:9876::/120", time.Hour)
	if err != nil {
		t.Fatalf("NewDualStack() error = %v", err)
	}

	pool.LookupForFamily("dual.example", FamilyIPv4)
	ipv4Lookup := pool.LastLookupTime("dual.example")
	if ipv4Lookup.IsZero() {
		t.Fatal("LastLookupTime() after IPv4 lookup = zero, want timestamp")
	}

	time.Sleep(2 * time.Millisecond)
	pool.LookupForFamily("dual.example", FamilyIPv6)
	ipv6Lookup := pool.LastLookupTime("dual.example")
	if !ipv6Lookup.After(ipv4Lookup) {
		t.Fatalf("LastLookupTime() after IPv6 lookup = %s, want after %s", ipv6Lookup, ipv4Lookup)
	}

	// Force the next IPv6 allocation to evict the dual.example IPv6 mapping.
	pool.mu.Lock()
	pool.ranges[FamilyIPv6].nextOff = pool.ranges[FamilyIPv6].firstOff
	pool.ranges[FamilyIPv6].cycled = true
	pool.mu.Unlock()

	result := pool.LookupResultForFamily("other.example", FamilyIPv6)
	if result.Evicted == nil || result.Evicted.Domain != "dual.example" {
		t.Fatalf("IPv6 lookup = %+v, want eviction of dual.example", result)
	}
	if got := pool.LastLookupTime("dual.example"); !got.Equal(ipv4Lookup) {
		t.Fatalf("LastLookupTime() after IPv6 eviction = %s, want IPv4 timestamp %s", got, ipv4Lookup)
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
	pool.ranges[FamilyIPv4].nextOff = 255
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

func TestLookupForFamilyAllocatesDistinctIPv4AndIPv6Mappings(t *testing.T) {
	pool, err := NewDualStack("198.18.0.0/24", "fdfe:dcba:9876::/120", time.Hour)
	if err != nil {
		t.Fatalf("NewDualStack() error = %v", err)
	}

	ip4 := pool.LookupForFamily("dual.example", FamilyIPv4)
	ip6 := pool.LookupForFamily("dual.example", FamilyIPv6)

	if !ip4.Is4() {
		t.Fatalf("IPv4 lookup returned %s", ip4)
	}
	if !ip6.Is6() {
		t.Fatalf("IPv6 lookup returned %s", ip6)
	}
	if ip4 != netip.MustParseAddr("198.18.0.4") {
		t.Fatalf("IPv4 lookup = %s, want 198.18.0.4", ip4)
	}
	if ip6 != netip.MustParseAddr("fdfe:dcba:9876::4") {
		t.Fatalf("IPv6 lookup = %s, want fdfe:dcba:9876::4", ip6)
	}
	if domain, ok := pool.LookBack(ip6); !ok || domain != "dual.example" {
		t.Fatalf("LookBack(%s) = %q, %v; want dual.example, true", ip6, domain, ok)
	}
	if !pool.Contains(ip4) || !pool.Contains(ip6) {
		t.Fatalf("Contains failed for allocated addresses %s and %s", ip4, ip6)
	}
	if pool.Size() != 2 {
		t.Fatalf("Size() = %d, want 2", pool.Size())
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
