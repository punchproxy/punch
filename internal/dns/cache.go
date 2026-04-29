package dns

import (
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/miekg/dns"
)

type cacheEntry struct {
	name     string
	qtype    string
	msg      *dns.Msg
	expireAt time.Time
	storedAt time.Time
}

type CacheSnapshotEntry struct {
	Name          string    `json:"name"`
	QType         string    `json:"qtype"`
	Result        string    `json:"result"`
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
	key := cacheKey(name, qtype)
	c.mu.Lock()

	value, ok := c.entries.Get(key)
	if !ok {
		c.mu.Unlock()
		return nil, false
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
		return nil, false
	}

	staleEntry := now.After(entry.expireAt)
	c.mu.Unlock()

	// Clone the message and adjust TTLs
	cloned := entry.msg.Copy()
	elapsed := uint32(now.Sub(entry.storedAt).Seconds())
	for _, rr := range cloned.Answer {
		if rr.Header().Ttl > elapsed {
			rr.Header().Ttl -= elapsed
		} else {
			rr.Header().Ttl = 0
		}
	}

	if staleEntry {
		return cloned, true // stale but within lazy window
	}
	return cloned, false
}

// Put stores a DNS response in the cache.
func (c *Cache) Put(name string, qtype uint16, msg *dns.Msg) {
	if msg == nil || len(msg.Answer) == 0 {
		return
	}

	key := cacheKey(name, qtype)
	ttl := c.getMinTTL(msg)
	if ttl < c.minTTL {
		ttl = c.minTTL
	}

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
		name:     name,
		qtype:    dns.TypeToString[qtype],
		msg:      msg.Copy(),
		expireAt: now.Add(ttl),
		storedAt: now,
	}
	c.entries.Add(key, entry)
	snap := c.entrySnapshotLocked(entry, now)
	events = append(events, CacheEvent{Op: CacheEventUpsert, Entry: &snap})

	handlers := append([]CacheEventHandler(nil), c.handlers...)
	c.mu.Unlock()

	fireCacheEvents(handlers, events)
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
