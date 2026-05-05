package dns

import (
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/miekg/dns"
)

type cacheEntry struct {
	name        string
	qtype       string
	msg         *dns.Msg
	queryResult string
	upstream    string
	expireAt    time.Time
	storedAt    time.Time
}

type cacheHit struct {
	msg         *dns.Msg
	queryResult string
	stale       bool
	elapsed     uint32
}

type CacheSnapshotEntry struct {
	Name          string    `json:"name"`
	QType         string    `json:"qtype"`
	Result        string    `json:"result"`
	Upstream      string    `json:"upstream,omitempty"`
	StoredAt      time.Time `json:"stored_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	LazyExpiresAt time.Time `json:"lazy_expires_at"`
	State         string    `json:"state"`
}

// CacheEventOp names a structural change to the cache.
type CacheEventOp string

const (
	CacheEventUpsert CacheEventOp = "upsert"
	CacheEventDelete CacheEventOp = "delete"
	CacheEventFlush  CacheEventOp = "flush"
)

// CacheEvent describes a single cache mutation. Subscribers compose these
// into their own view of the cache without needing periodic full snapshots.
type CacheEvent struct {
	Op    CacheEventOp        `json:"op"`
	Entry *CacheSnapshotEntry `json:"entry,omitempty"`
	Name  string              `json:"name,omitempty"`
	QType string              `json:"qtype,omitempty"`
}

type CacheEventHandler func(CacheEvent)

// Cache is an LRU DNS cache with lazy revalidation support.
type Cache struct {
	mu      sync.Mutex
	entries *lru.Cache
	maxSize int
	minTTL  time.Duration
	lazyTTL time.Duration

	handlers []CacheEventHandler
}

func NewCache(maxSize int, minTTL, lazyTTL int) *Cache {
	if maxSize <= 0 {
		maxSize = 1
	}
	entries, err := lru.New(maxSize)
	if err != nil {
		panic(err)
	}
	return &Cache{
		entries: entries,
		maxSize: maxSize,
		minTTL:  time.Duration(minTTL) * time.Second,
		lazyTTL: time.Duration(lazyTTL) * time.Second,
	}
}

func cacheKey(name string, qtype uint16) string {
	return name + ":" + dns.TypeToString[qtype]
}

// Get retrieves a cached entry. Returns (msg, stale) where stale indicates
// the entry has expired but is within the lazy TTL window.
func (c *Cache) Get(name string, qtype uint16) (msg *dns.Msg, stale bool) {
	hit, ok := c.lookup(name, qtype)
	if !ok {
		return nil, false
	}
	return hit.message(), hit.stale
}

// lookup retrieves a cache entry as an immutable hit view. The returned view
// may share internal cache state, so callers must not mutate h.msg or its RRs.
func (c *Cache) lookup(name string, qtype uint16) (cacheHit, bool) {
	key := cacheKey(name, qtype)
	c.mu.Lock()

	value, ok := c.entries.Get(key)
	if !ok {
		c.mu.Unlock()
		return cacheHit{}, false
	}
	entry := value.(*cacheEntry)

	now := time.Now()

	// Check if completely expired (past lazy TTL)
	lazyDeadline := entry.expireAt.Add(c.lazyTTL)
	if now.After(lazyDeadline) {
		c.entries.Remove(key)
		handlers := append([]CacheEventHandler(nil), c.handlers...)
		c.mu.Unlock()
		fireCacheEvents(handlers, []CacheEvent{{Op: CacheEventDelete, Name: entry.name, QType: entry.qtype}})
		return cacheHit{}, false
	}

	staleEntry := now.After(entry.expireAt)
	hit := cacheHit{
		msg:         entry.msg,
		queryResult: entry.queryResult,
		stale:       staleEntry,
		elapsed:     elapsedSeconds(entry.storedAt, now),
	}
	c.mu.Unlock()
	return hit, true
}

func (h cacheHit) message() *dns.Msg {
	if h.msg == nil {
		return nil
	}
	cloned := h.msg.Copy()
	adjustAnswerTTLs(cloned.Answer, h.elapsed)
	return cloned
}

func (h cacheHit) answerMinTTL() uint32 {
	if h.msg == nil || len(h.msg.Answer) == 0 {
		return 0
	}
	minTTL := uint32(^uint32(0))
	for _, rr := range h.msg.Answer {
		if ttl := adjustedTTL(rr, h.elapsed); ttl < minTTL {
			minTTL = ttl
		}
	}
	if minTTL == uint32(^uint32(0)) {
		return 0
	}
	return minTTL
}

func adjustAnswerTTLs(answer []dns.RR, elapsed uint32) {
	for _, rr := range answer {
		if rr == nil || rr.Header() == nil {
			continue
		}
		rr.Header().Ttl = adjustedTTL(rr, elapsed)
	}
}

func adjustedTTL(rr dns.RR, elapsed uint32) uint32 {
	if rr == nil || rr.Header() == nil {
		return 0
	}
	ttl := rr.Header().Ttl
	if ttl > elapsed {
		return ttl - elapsed
	}
	return 0
}

func elapsedSeconds(storedAt, now time.Time) uint32 {
	if !now.After(storedAt) {
		return 0
	}
	elapsed := now.Sub(storedAt).Seconds()
	maxUint32 := float64(^uint32(0))
	if elapsed >= maxUint32 {
		return ^uint32(0)
	}
	return uint32(elapsed)
}

// Put stores a DNS response and the upstream that produced it in the cache. It
// returns the query-log result string formatted from the stored response.
func (c *Cache) Put(name string, qtype uint16, msg *dns.Msg, upstream string) string {
	if msg == nil || len(msg.Answer) == 0 {
		return ""
	}

	key := cacheKey(name, qtype)
	ttl := c.getMinTTL(msg)
	if ttl < c.minTTL {
		ttl = c.minTTL
	}

	cachedMsg := msg.Copy()
	queryResult := answerToString(cachedMsg)

	c.mu.Lock()

	var events []CacheEvent

	if _, exists := c.entries.Peek(key); !exists && c.entries.Len() >= c.maxSize {
		if _, value, ok := c.entries.RemoveOldest(); ok {
			evicted := value.(*cacheEntry)
			events = append(events, CacheEvent{Op: CacheEventDelete, Name: evicted.name, QType: evicted.qtype})
		}
	}

	now := time.Now()
	entry := &cacheEntry{
		name:        name,
		qtype:       dns.TypeToString[qtype],
		msg:         cachedMsg,
		queryResult: queryResult,
		upstream:    upstream,
		expireAt:    now.Add(ttl),
		storedAt:    now,
	}
	c.entries.Add(key, entry)
	snap := c.entrySnapshotLocked(entry, now)
	events = append(events, CacheEvent{Op: CacheEventUpsert, Entry: &snap})

	handlers := append([]CacheEventHandler(nil), c.handlers...)
	c.mu.Unlock()

	fireCacheEvents(handlers, events)
	return queryResult
}

// Flush clears the entire cache.
func (c *Cache) Flush() {
	c.mu.Lock()
	c.entries.Purge()
	handlers := append([]CacheEventHandler(nil), c.handlers...)
	c.mu.Unlock()
	fireCacheEvents(handlers, []CacheEvent{{Op: CacheEventFlush}})
}

// OnChange registers a handler invoked after each cache mutation. Handlers
// are called outside the cache's internal lock.
func (c *Cache) OnChange(h CacheEventHandler) {
	if h == nil {
		return
	}
	c.mu.Lock()
	c.handlers = append(c.handlers, h)
	c.mu.Unlock()
}

func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries.Len()
}

func (c *Cache) Snapshot() []CacheSnapshotEntry {
	c.mu.Lock()

	now := time.Now()
	var events []CacheEvent
	keys := c.entries.Keys()
	liveKeys := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		value, ok := c.entries.Peek(key)
		if !ok {
			continue
		}
		entry := value.(*cacheEntry)
		if now.After(entry.expireAt.Add(c.lazyTTL)) {
			events = append(events, CacheEvent{Op: CacheEventDelete, Name: entry.name, QType: entry.qtype})
			c.entries.Remove(key)
			continue
		}
		liveKeys = append(liveKeys, key)
	}

	result := make([]CacheSnapshotEntry, 0, len(liveKeys))
	for i := len(liveKeys) - 1; i >= 0; i-- {
		value, ok := c.entries.Peek(liveKeys[i])
		if !ok {
			continue
		}
		entry := value.(*cacheEntry)
		result = append(result, c.entrySnapshotLocked(entry, now))
	}
	handlers := append([]CacheEventHandler(nil), c.handlers...)
	c.mu.Unlock()
	fireCacheEvents(handlers, events)
	return result
}

// entrySnapshotLocked builds a CacheSnapshotEntry for entry. Caller must hold
// c.mu because it reads entry fields and c.lazyTTL.
func (c *Cache) entrySnapshotLocked(entry *cacheEntry, now time.Time) CacheSnapshotEntry {
	state := "live"
	if now.After(entry.expireAt) {
		state = "stale"
	}
	return CacheSnapshotEntry{
		Name:          entry.name,
		QType:         entry.qtype,
		Result:        summarizeCacheResult(entry.msg),
		Upstream:      entry.upstream,
		StoredAt:      entry.storedAt,
		ExpiresAt:     entry.expireAt,
		LazyExpiresAt: entry.expireAt.Add(c.lazyTTL),
		State:         state,
	}
}

func fireCacheEvents(handlers []CacheEventHandler, events []CacheEvent) {
	for _, ev := range events {
		for _, h := range handlers {
			h(ev)
		}
	}
}

func (c *Cache) getMinTTL(msg *dns.Msg) time.Duration {
	var minTTL uint32 = 0xFFFFFFFF
	for _, rr := range msg.Answer {
		if rr.Header().Ttl < minTTL {
			minTTL = rr.Header().Ttl
		}
	}
	if minTTL == 0xFFFFFFFF {
		minTTL = 60
	}
	return time.Duration(minTTL) * time.Second
}

func summarizeCacheResult(msg *dns.Msg) string {
	if msg == nil || len(msg.Answer) == 0 {
		return "EMPTY(NOERROR)"
	}

	parts := make([]string, 0, len(msg.Answer))
	for _, rr := range msg.Answer {
		switch v := rr.(type) {
		case *dns.A:
			parts = append(parts, v.A.String())
		case *dns.AAAA:
			parts = append(parts, v.AAAA.String())
		}
	}

	if len(parts) == 0 {
		return "EMPTY(NOERROR)"
	}
	return strings.Join(parts, ", ")
}
