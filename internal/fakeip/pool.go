// Package fakeip provides a bidirectional mapping between domains and
// synthetic IPv4 addresses drawn from a configurable CIDR range.
package fakeip

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// minPrefixLen is the smallest IPv4 prefix length the pool will accept;
// /24 leaves 254 usable addresses after subtracting network/broadcast.
const minPrefixLen = 24

// Mapping describes a single domain→fake-IP binding.
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

type entry struct {
	ip         netip.Addr
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

// Pool manages allocation of fake IPs from a CIDR range.
//
// IP arithmetic is performed on uint32 offsets within the configured prefix
// rather than on netip.Addr values, which lets allocation, broadcast-skip,
// and wrap-around checks stay in registers. Read-only paths (LookBack,
// Contains, LastLookupTime) take only an RLock and never prune, so the
// per-packet hot path in the TUN handler is contention-free.
type Pool struct {
	mu sync.RWMutex

	ipNet    netip.Prefix
	baseIP   uint32 // network address as uint32
	gateway  netip.Addr
	firstOff uint32 // first allocatable offset (relative to baseIP)
	lastOff  uint32 // last allocatable offset, inclusive
	nextOff  uint32 // next offset to try
	cycled   bool   // true once nextOff has wrapped at least once

	byHost map[string]*entry
	byIP   map[netip.Addr]*entry
	ttl    time.Duration
}

// New constructs a Pool over the given CIDR. The prefix must be IPv4 and at
// least /24 wide so that there are enough non-broadcast addresses to allocate.
func New(cidr string, ttl time.Duration) (*Pool, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, err
	}
	if !prefix.Addr().Is4() {
		return nil, fmt.Errorf("fakeip: CIDR %q must be IPv4", cidr)
	}
	if prefix.Bits() > minPrefixLen {
		return nil, fmt.Errorf("fakeip: CIDR %q is too small, need at least /%d", cidr, minPrefixLen)
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	prefix = prefix.Masked()

	base := ipToU32(prefix.Addr())
	hostBits := uint(32 - prefix.Bits())
	totalAddrs := uint32(1) << hostBits
	// Reserve .0 (network), .1 (gateway), .2/.3 (often used by helpers); start at .4.
	const reservedHead = 4
	firstOff := uint32(reservedHead)
	lastOff := totalAddrs - 1 // skip the broadcast at the very end via the per-iteration check

	return &Pool{
		ipNet:    prefix,
		baseIP:   base,
		gateway:  u32ToAddr(base + 1),
		firstOff: firstOff,
		lastOff:  lastOff,
		nextOff:  firstOff,
		byHost:   make(map[string]*entry),
		byIP:     make(map[netip.Addr]*entry),
		ttl:      ttl,
	}, nil
}

// Lookup returns a fake IP for the given host, allocating one if needed.
func (p *Pool) Lookup(host string) netip.Addr {
	return p.LookupResult(host).Mapping.IP
}

// LookupResult is the full-fidelity variant of Lookup that reports whether
// the mapping was newly created and which mapping (if any) was evicted.
func (p *Pool) LookupResult(host string) LookupResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.pruneExpiredLocked(now)

	if e, ok := p.byHost[host]; ok {
		e.expiresAt = now.Add(p.ttl)
		e.lastLookup = now
		return LookupResult{Mapping: e.mapping(), Refreshed: true}
	}

	ip, evicted := p.allocateLocked()
	e := &entry{
		ip:         ip,
		domain:     host,
		expiresAt:  now.Add(p.ttl),
		lastLookup: now,
	}
	p.byHost[host] = e
	p.byIP[ip] = e
	return LookupResult{Mapping: e.mapping(), Created: true, Evicted: evicted}
}

// LookBack returns the domain associated with a fake IP. This is on the
// per-packet hot path of the TUN engine, so it takes only an RLock.
func (p *Pool) LookBack(ip netip.Addr) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e, ok := p.byIP[ip]; ok {
		return e.domain, true
	}
	return "", false
}

// LastLookupTime returns the most recent time the domain was resolved.
func (p *Pool) LastLookupTime(domain string) time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e, ok := p.byHost[domain]; ok {
		return e.lastLookup
	}
	return time.Time{}
}

// Contains reports whether ip is inside the pool's CIDR range.
func (p *Pool) Contains(ip netip.Addr) bool {
	return p.ipNet.Contains(ip)
}

// IPNet returns the pool's CIDR prefix.
func (p *Pool) IPNet() netip.Prefix { return p.ipNet }

// Gateway returns the gateway address (.1 within the prefix).
func (p *Pool) Gateway() netip.Addr { return p.gateway }

// Acquire pins the mapping for ip to a session, preventing TTL eviction
// while the session is alive. Returns false if no mapping exists.
func (p *Pool) Acquire(ip netip.Addr, sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byIP[ip]
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
	e, ok := p.byIP[ip]
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
	if e, ok := p.byIP[ip]; ok {
		return e.sessionList()
	}
	return nil
}

// Size returns the number of live mappings (excluding any that have expired
// since the last allocation).
func (p *Pool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.byHost)
}

// Snapshot returns all live mappings sorted by IP.
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

// allocateLocked picks the next available offset, skipping broadcast (.255)
// addresses and evicting an existing entry if the pool has wrapped.
func (p *Pool) allocateLocked() (netip.Addr, *Mapping) {
	for {
		off := p.nextOff
		if off > p.lastOff {
			p.cycled = true
			off = p.firstOff
		}
		p.nextOff = off + 1

		// Skip broadcast addresses (.255 in any /24 sub-block).
		if (p.baseIP+off)&0xFF == 0xFF {
			continue
		}

		ip := u32ToAddr(p.baseIP + off)

		var evicted *Mapping
		if existing, ok := p.byIP[ip]; ok {
			// Only evict if we've cycled — otherwise the caller is racing
			// with itself, which can't happen because we hold the lock.
			if !p.cycled {
				// Defensive: should be unreachable.
				continue
			}
			ev := existing.mapping()
			evicted = &ev
			delete(p.byHost, existing.domain)
			delete(p.byIP, ip)
		}
		return ip, evicted
	}
}

// pruneExpiredLocked drops mappings whose TTL has lapsed and that have no
// active session pinning them. Called only from write paths.
func (p *Pool) pruneExpiredLocked(now time.Time) {
	for ip, e := range p.byIP {
		if len(e.sessions) > 0 {
			continue
		}
		if now.Before(e.expiresAt) {
			continue
		}
		delete(p.byIP, ip)
		delete(p.byHost, e.domain)
	}
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
