// Package fakeip provides bidirectional mappings between domains and
// synthetic IPv4/IPv6 addresses drawn from configurable CIDR ranges.
package fakeip

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Family identifies the address family of a fake IP allocation.
type Family int

const (
	FamilyIPv4 Family = 4
	FamilyIPv6 Family = 6
)

const (
	// /24 leaves 252 usable IPv4 addresses after subtracting reserved helper
	// addresses and per-/24 broadcast addresses.
	minIPv4PrefixLen = 24
	// /120 gives IPv6 pools the same minimum 256-address scale as IPv4 /24.
	minIPv6PrefixLen = 120
	reservedHead     = 4
	pruneInterval    = 10 * time.Second
)

// Mapping describes a single domain to fake-IP binding.
type Mapping struct {
	IP         netip.Addr
	Domain     string
	ExpiresAt  time.Time
	SessionIDs []string
}

// Active reports whether any session is currently pinning this mapping.
func (m Mapping) Active() bool { return len(m.SessionIDs) > 0 }

// LookupResult captures the outcome of a Lookup.
type LookupResult struct {
	Mapping   Mapping
	Created   bool
	Refreshed bool
	Evicted   *Mapping
}

type hostKey struct {
	domain string
	family Family
}

type entry struct {
	ip         netip.Addr
	family     Family
	domain     string
	expiresAt  time.Time
	lastLookup time.Time
	sessions   map[string]struct{}
}

func (e *entry) sessionList() []string {
	if len(e.sessions) == 0 {
		return nil
	}
	out := make([]string, 0, len(e.sessions))
	for id := range e.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (e *entry) mapping() Mapping {
	return Mapping{
		IP:         e.ip,
		Domain:     e.domain,
		ExpiresAt:  e.expiresAt,
		SessionIDs: e.sessionList(),
	}
}

type rangeState struct {
	family   Family
	ipNet    netip.Prefix
	baseHi   uint64
	baseLo   uint64
	gateway  netip.Addr
	firstOff uint64
	lastOff  uint64
	nextOff  uint64
	cycled   bool
}

// Pool manages allocation of fake IPs from one IPv4 range and, optionally,
// one IPv6 range.
//
// Read-only paths (LookBack, Contains, LastLookupTime) take only an RLock and
// never prune, so the per-packet hot path in the TUN handler is low-contention.
type Pool struct {
	mu sync.RWMutex

	ranges        map[Family]*rangeState
	defaultFamily Family

	byHost map[hostKey]*entry
	byIP   map[netip.Addr]*entry
	// lastLookupByDomain keeps LastLookupTime off the connect-path linear scan.
	lastLookupByDomain map[string]time.Time
	ttl                time.Duration
	nextPruneAt        time.Time
}

// New constructs a Pool over a single CIDR. IPv4 CIDRs are allocated by
// Lookup/LookupResult; IPv6 CIDRs are supported for tests and specialized
// callers, but normal Punch configuration should use NewDualStack.
func New(cidr string, ttl time.Duration) (*Pool, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, err
	}
	if prefix.Addr().Is6() {
		return NewDualStack("", cidr, ttl)
	}
	return NewDualStack(cidr, "", ttl)
}

// NewDualStack constructs a Pool with IPv4 and/or IPv6 CIDRs. Either CIDR may
// be empty, but at least one family must be enabled.
func NewDualStack(ipv4CIDR, ipv6CIDR string, ttl time.Duration) (*Pool, error) {
	if ttl <= 0 {
		ttl = time.Hour
	}
	p := &Pool{
		ranges:             make(map[Family]*rangeState, 2),
		byHost:             make(map[hostKey]*entry),
		byIP:               make(map[netip.Addr]*entry),
		lastLookupByDomain: make(map[string]time.Time),
		ttl:                ttl,
	}
	if ipv4CIDR != "" {
		r, err := newRangeState(ipv4CIDR, FamilyIPv4)
		if err != nil {
			return nil, err
		}
		p.ranges[FamilyIPv4] = r
		p.defaultFamily = FamilyIPv4
	}
	if ipv6CIDR != "" {
		r, err := newRangeState(ipv6CIDR, FamilyIPv6)
		if err != nil {
			return nil, err
		}
		p.ranges[FamilyIPv6] = r
		if p.defaultFamily == 0 {
			p.defaultFamily = FamilyIPv6
		}
	}
	if len(p.ranges) == 0 {
		return nil, fmt.Errorf("fakeip: at least one fake IP CIDR is required")
	}
	return p, nil
}

func newRangeState(cidr string, family Family) (*rangeState, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, err
	}
	prefix = prefix.Masked()
	if family == FamilyIPv4 && !prefix.Addr().Is4() {
		return nil, fmt.Errorf("fakeip: CIDR %q must be IPv4", cidr)
	}
	if family == FamilyIPv6 && !prefix.Addr().Is6() {
		return nil, fmt.Errorf("fakeip: CIDR %q must be IPv6", cidr)
	}

	bitLen := prefix.Addr().BitLen()
	minPrefixLen := minIPv4PrefixLen
	if family == FamilyIPv6 {
		minPrefixLen = minIPv6PrefixLen
	}
	if prefix.Bits() > minPrefixLen {
		return nil, fmt.Errorf("fakeip: CIDR %q is too small, need at least /%d", cidr, minPrefixLen)
	}

	hostBits := bitLen - prefix.Bits()
	var totalAddrs uint64
	if hostBits >= 64 {
		totalAddrs = math.MaxUint64
	} else {
		totalAddrs = uint64(1) << uint(hostBits)
	}
	if totalAddrs <= reservedHead {
		return nil, fmt.Errorf("fakeip: CIDR %q does not leave allocatable addresses", cidr)
	}

	baseHi, baseLo := addrToParts(prefix.Addr())
	state := &rangeState{
		family:   family,
		ipNet:    prefix,
		baseHi:   baseHi,
		baseLo:   baseLo,
		firstOff: reservedHead,
		lastOff:  totalAddrs - 1,
		nextOff:  reservedHead,
	}
	state.gateway = state.addrAt(1)
	return state, nil
}

// Lookup returns a fake IP for the given host, allocating one if needed.
func (p *Pool) Lookup(host string) netip.Addr {
	return p.LookupResult(host).Mapping.IP
}

// LookupResult is the full-fidelity variant of Lookup that reports whether
// the mapping was newly created and which mapping (if any) was evicted.
func (p *Pool) LookupResult(host string) LookupResult {
	return p.LookupResultForFamily(host, p.defaultFamily)
}

// LookupForFamily returns a fake IP in the requested family.
func (p *Pool) LookupForFamily(host string, family Family) netip.Addr {
	return p.LookupResultForFamily(host, family).Mapping.IP
}

// LookupResultForFamily returns or creates a mapping in the requested family.
func (p *Pool) LookupResultForFamily(host string, family Family) LookupResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()

	key := hostKey{domain: host, family: family}
	if e, ok := p.byHost[key]; ok {
		if p.expiredUnpinnedLocked(e, now) {
			p.deleteEntryLocked(e.ip, e)
		} else {
			e.expiresAt = now.Add(p.ttl)
			e.lastLookup = now
			p.lastLookupByDomain[host] = now
			return LookupResult{Mapping: e.mapping(), Refreshed: true}
		}
	}

	p.maybePruneExpiredLocked(now)

	ip, evicted := p.allocateLocked(family)
	if !ip.IsValid() {
		return LookupResult{}
	}
	e := &entry{
		ip:         ip,
		family:     family,
		domain:     host,
		expiresAt:  now.Add(p.ttl),
		lastLookup: now,
	}
	p.byHost[key] = e
	p.byIP[ip] = e
	p.lastLookupByDomain[host] = now
	return LookupResult{Mapping: e.mapping(), Created: true, Evicted: evicted}
}

// LookBack returns the domain associated with a fake IP. This is on the
// per-packet hot path of the TUN engine, so it takes only an RLock.
func (p *Pool) LookBack(ip netip.Addr) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e, ok := p.byIP[ip.Unmap()]; ok {
		return e.domain, true
	}
	return "", false
}

// LastLookupTime returns the most recent time the domain was resolved.
func (p *Pool) LastLookupTime(domain string) time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastLookupByDomain[domain]
}

// Contains reports whether ip is inside any configured fake IP range.
func (p *Pool) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, r := range p.ranges {
		if r.ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// HasFamily reports whether the pool can allocate fake IPs for family.
func (p *Pool) HasFamily(family Family) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.ranges[family]
	return ok
}

// IPNet returns the IPv4 pool CIDR when present, otherwise the default CIDR.
func (p *Pool) IPNet() netip.Prefix {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.ranges[FamilyIPv4]; ok {
		return r.ipNet
	}
	return p.ranges[p.defaultFamily].ipNet
}

// IPNet6 returns the IPv6 pool CIDR.
func (p *Pool) IPNet6() (netip.Prefix, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.ranges[FamilyIPv6]
	if !ok {
		return netip.Prefix{}, false
	}
	return r.ipNet, true
}

// Gateway returns the IPv4 gateway address when present, otherwise the
// default-family gateway address.
func (p *Pool) Gateway() netip.Addr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.ranges[FamilyIPv4]; ok {
		return r.gateway
	}
	return p.ranges[p.defaultFamily].gateway
}

// Gateway6 returns the IPv6 gateway address.
func (p *Pool) Gateway6() (netip.Addr, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.ranges[FamilyIPv6]
	if !ok {
		return netip.Addr{}, false
	}
	return r.gateway, true
}

// Acquire pins the mapping for ip to a session, preventing TTL eviction
// while the session is alive. Returns false if no mapping exists.
func (p *Pool) Acquire(ip netip.Addr, sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byIP[ip.Unmap()]
	if !ok {
		return false
	}
	if e.sessions == nil {
		e.sessions = make(map[string]struct{})
	}
	e.sessions[sessionID] = struct{}{}
	e.expiresAt = time.Now().Add(p.ttl)
	return true
}

// Release removes a session pin. Returns false if the mapping is gone.
func (p *Pool) Release(ip netip.Addr, sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byIP[ip.Unmap()]
	if !ok {
		return false
	}
	if e.sessions != nil {
		delete(e.sessions, sessionID)
		if len(e.sessions) == 0 {
			e.sessions = nil
		}
	}
	e.expiresAt = time.Now().Add(p.ttl)
	return true
}

// Sessions returns the active session IDs pinning ip, sorted.
func (p *Pool) Sessions(ip netip.Addr) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e, ok := p.byIP[ip.Unmap()]; ok {
		return e.sessionList()
	}
	return nil
}

// Size returns the number of mappings currently retained by the pool. Expired,
// unpinned mappings may remain until the next scheduled prune.
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.byHost)
}

// Snapshot returns all mappings currently retained by the pool, sorted by IP.
func (p *Pool) Snapshot() []Mapping {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]Mapping, 0, len(p.byIP))
	for _, e := range p.byIP {
		out = append(out, e.mapping())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].IP.Compare(out[j].IP) < 0
	})
	return out
}

// Restore seeds the pool with previously-persisted mappings. Mappings whose
// IP is outside the pool's configured ranges, or that collide with existing
// in-memory entries, are skipped. Expired entries are revived with a fresh
// TTL so they survive at least one prune cycle after startup. Returns the
// number of mappings successfully restored.
func (p *Pool) Restore(mappings []Mapping) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	restored := 0
	for _, m := range mappings {
		ip := m.IP.Unmap()
		if !ip.IsValid() || m.Domain == "" {
			continue
		}
		family := FamilyIPv4
		if ip.Is6() {
			family = FamilyIPv6
		}
		r, ok := p.ranges[family]
		if !ok || !r.ipNet.Contains(ip) {
			continue
		}
		key := hostKey{domain: m.Domain, family: family}
		if _, ok := p.byHost[key]; ok {
			continue
		}
		if _, ok := p.byIP[ip]; ok {
			continue
		}
		expiresAt := m.ExpiresAt
		if expiresAt.Before(now.Add(p.ttl)) {
			expiresAt = now.Add(p.ttl)
		}
		e := &entry{
			ip:         ip,
			family:     family,
			domain:     m.Domain,
			expiresAt:  expiresAt,
			lastLookup: now,
		}
		p.byHost[key] = e
		p.byIP[ip] = e
		if cur, ok := p.lastLookupByDomain[m.Domain]; !ok || now.After(cur) {
			p.lastLookupByDomain[m.Domain] = now
		}
		restored++
	}
	return restored
}

func (p *Pool) allocateLocked(family Family) (netip.Addr, *Mapping) {
	r := p.ranges[family]
	if r == nil {
		return netip.Addr{}, nil
	}
	for {
		off := r.nextOff
		if off > r.lastOff || off < r.firstOff {
			r.cycled = true
			off = r.firstOff
		}
		if off >= r.lastOff {
			r.nextOff = r.firstOff
			r.cycled = true
		} else {
			r.nextOff = off + 1
		}

		// Skip IPv4 broadcast addresses (.255 in any /24 sub-block).
		if r.family == FamilyIPv4 && uint8(r.baseLo+off) == 0xFF {
			continue
		}

		ip := r.addrAt(off)
		if !r.ipNet.Contains(ip) {
			continue
		}

		var evicted *Mapping
		if existing, ok := p.byIP[ip]; ok {
			if !r.cycled {
				continue
			}
			ev := existing.mapping()
			evicted = &ev
			p.deleteEntryLocked(ip, existing)
		}
		return ip, evicted
	}
}

func (r *rangeState) addrAt(off uint64) netip.Addr {
	if r.family == FamilyIPv4 {
		return u32ToAddr(uint32(r.baseLo + off))
	}
	hi := r.baseHi
	lo := r.baseLo + off
	if lo < r.baseLo {
		hi++
	}
	return partsToIPv6(hi, lo)
}

// pruneExpiredLocked drops mappings whose TTL has lapsed and that have no
// active session pinning them. Called only from write paths.
func (p *Pool) pruneExpiredLocked(now time.Time) {
	for ip, e := range p.byIP {
		if p.expiredUnpinnedLocked(e, now) {
			p.deleteEntryLocked(ip, e)
		}
	}
}

func (p *Pool) maybePruneExpiredLocked(now time.Time) {
	if now.Before(p.nextPruneAt) {
		return
	}
	p.pruneExpiredLocked(now)
	p.nextPruneAt = now.Add(pruneInterval)
}

func (p *Pool) expiredUnpinnedLocked(e *entry, now time.Time) bool {
	return len(e.sessions) == 0 && !now.Before(e.expiresAt)
}

func (p *Pool) deleteEntryLocked(ip netip.Addr, e *entry) {
	delete(p.byIP, ip)
	delete(p.byHost, hostKey{domain: e.domain, family: e.family})
	p.refreshLastLookupLocked(e.domain)
}

func (p *Pool) refreshLastLookupLocked(domain string) {
	var latest time.Time
	for _, family := range []Family{FamilyIPv4, FamilyIPv6} {
		e, ok := p.byHost[hostKey{domain: domain, family: family}]
		if !ok {
			continue
		}
		if latest.IsZero() || e.lastLookup.After(latest) {
			latest = e.lastLookup
		}
	}
	if latest.IsZero() {
		delete(p.lastLookupByDomain, domain)
		return
	}
	p.lastLookupByDomain[domain] = latest
}

func addrToParts(addr netip.Addr) (uint64, uint64) {
	if addr.Is4() {
		return 0, uint64(ipToU32(addr))
	}
	a16 := addr.As16()
	return binary.BigEndian.Uint64(a16[:8]), binary.BigEndian.Uint64(a16[8:])
}

func partsToIPv6(hi, lo uint64) netip.Addr {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], hi)
	binary.BigEndian.PutUint64(b[8:], lo)
	return netip.AddrFrom16(b)
}

func ipToU32(addr netip.Addr) uint32 {
	a4 := addr.As4()
	return binary.BigEndian.Uint32(a4[:])
}

func u32ToAddr(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
