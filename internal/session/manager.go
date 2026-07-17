package session

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

type Manager struct {
	mu            sync.RWMutex
	active        map[string]*Session
	activeFakeIPs map[string]map[string]struct{}
	closed        []*Session
	closedMaxSize int
	history       HistoryStore
	bus           *eventbus.Bus
	nextID        atomic.Uint64
	totalUpload   atomic.Int64
	totalDownload atomic.Int64

	rateMu       sync.Mutex
	rateAt       time.Time
	rateUpload   int64
	rateDownload int64
}

// DefaultHistoryLimit caps how many closed sessions are retained. History
// spills to SQLite (per run), so the cap bounds the dashboard payload and
// per-run disk growth rather than daemon memory.
const DefaultHistoryLimit = 10000

func NewManager(bus *eventbus.Bus, closedBufferSize int) *Manager {
	if closedBufferSize <= 0 {
		closedBufferSize = DefaultHistoryLimit
	}
	return &Manager{
		active:        make(map[string]*Session),
		activeFakeIPs: make(map[string]map[string]struct{}),
		closedMaxSize: closedBufferSize,
		bus:           bus,
	}
}

type SessionOpts struct {
	FakeIP         string
	DNSRequestedAt time.Time
}

func (m *Manager) NewSession(domain, source, dstIP string, dstPort int, protocol, relay, rule string, opts SessionOpts) *Session {
	id := m.nextID.Add(1)
	s := &Session{
		ID:             fmt.Sprintf("%d", id),
		Status:         StatusActive,
		Domain:         domain,
		Source:         source,
		DstIP:          dstIP,
		DstPort:        dstPort,
		Protocol:       protocol,
		Relay:          relay,
		Rule:           rule,
		FakeIP:         opts.FakeIP,
		StartTime:      time.Now(),
		DNSRequestedAt: opts.DNSRequestedAt,
	}
	if !opts.DNSRequestedAt.IsZero() {
		message := "DNS requested"
		if opts.FakeIP != "" {
			message = fmt.Sprintf("DNS resolved A → %s", opts.FakeIP)
		}
		s.trace = append(s.trace, TraceEntry{At: opts.DNSRequestedAt, Message: message})
	}
	s.trace = append(s.trace, TraceEntry{At: s.StartTime, Message: "TUN connection received"})

	m.mu.Lock()
	m.active[s.ID] = s
	m.addActiveFakeIPLocked(s)
	m.mu.Unlock()

	s.SetUpdateFunc(func() {
		m.bus.Publish(eventbus.Event{
			Type: eventbus.EventSessionUpdate,
			Data: s,
		})
	})

	m.bus.Publish(eventbus.Event{
		Type: eventbus.EventSessionOpen,
		Data: s,
	})

	return s
}

func (m *Manager) CloseSession(id string, status Status) {
	m.mu.Lock()
	s, ok := m.active[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.active, id)
	m.removeActiveFakeIPLocked(s)

	s.Status = status
	s.EndTime = time.Now()
	if status == StatusError && s.closeReason != "" {
		s.trace = append(s.trace, TraceEntry{At: s.EndTime, Message: "error occurred: " + s.closeReason})
	}
	s.trace = append(s.trace, TraceEntry{At: s.EndTime, Message: "Session closed"})
	m.totalUpload.Add(s.Upload.Load())
	m.totalDownload.Add(s.Download.Load())

	history, limit := m.history, m.closedMaxSize
	if history == nil {
		closedCapacity := m.closedMaxSize - len(m.active)
		if closedCapacity > 0 {
			m.closed = append(m.closed, s)
			if len(m.closed) > closedCapacity {
				m.closed = m.closed[len(m.closed)-closedCapacity:]
			}
		} else {
			m.closed = nil
		}
	}
	m.mu.Unlock()

	s.Close()

	if history != nil {
		if err := history.AppendClosedSession(s.closedRecord(), limit); err != nil {
			slog.Warn("persist closed session", "id", s.ID, "error", err)
		}
	}

	m.bus.Publish(eventbus.Event{
		Type: eventbus.EventSessionClose,
		Data: s,
	})
}

func (m *Manager) KillSession(id string) bool {
	m.mu.RLock()
	s, ok := m.active[id]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	s.Close()
	return true
}

func (m *Manager) KillAllSessions() int {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.active))
	for _, s := range m.active {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	for _, s := range sessions {
		s.Close()
	}
	return len(sessions)
}

func (m *Manager) Session(id string) (*Session, bool) {
	m.mu.RLock()
	if s, ok := m.active[id]; ok {
		m.mu.RUnlock()
		return s, true
	}
	for i := len(m.closed) - 1; i >= 0; i-- {
		if m.closed[i].ID == id {
			s := m.closed[i]
			m.mu.RUnlock()
			return s, true
		}
	}
	history := m.history
	m.mu.RUnlock()

	if history != nil {
		rec, ok, err := history.GetClosedSession(id)
		if err != nil {
			slog.Warn("load closed session", "id", id, "error", err)
			return nil, false
		}
		if ok {
			return rec.restore(), true
		}
	}
	return nil, false
}

// SetHistoryStore spills closed sessions to h instead of keeping them in
// memory. Call it during wiring, before traffic flows.
func (m *Manager) SetHistoryStore(h HistoryStore) {
	m.mu.Lock()
	m.history = h
	if h != nil {
		m.closed = nil
	}
	m.mu.Unlock()
}

func (m *Manager) RecentSessions() []*Session {
	m.mu.RLock()
	result := make([]*Session, 0, len(m.active)+len(m.closed))
	for _, s := range m.active {
		result = append(result, s)
	}
	result = append(result, m.closed...)
	history, limit := m.history, m.closedMaxSize
	m.mu.RUnlock()

	if history != nil {
		records, err := history.ListClosedSessions(limit)
		if err != nil {
			slog.Warn("load closed sessions", "error", err)
		}
		for _, rec := range records {
			result = append(result, rec.restore())
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.After(result[j].StartTime)
	})
	return result
}

func (m *Manager) ClearNonActive() int {
	m.mu.Lock()
	cleared := len(m.closed)
	m.closed = nil
	history := m.history
	m.mu.Unlock()

	if history != nil {
		n, err := history.ClearClosedSessions()
		if err != nil {
			slog.Warn("clear closed sessions", "error", err)
		}
		cleared += n
	}
	return cleared
}

func (m *Manager) ActiveSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Session, 0, len(m.active))
	for _, s := range m.active {
		result = append(result, s)
	}
	return result
}

func (m *Manager) ActiveSessionIDsByFakeIP() map[string][]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]string, len(m.activeFakeIPs))
	for fakeIP, ids := range m.activeFakeIPs {
		if len(ids) == 0 {
			continue
		}
		list := make([]string, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		sort.Strings(list)
		result[fakeIP] = list
	}
	return result
}

func (m *Manager) ClosedSessions() []*Session {
	m.mu.RLock()
	result := make([]*Session, len(m.closed))
	copy(result, m.closed)
	history, limit := m.history, m.closedMaxSize
	m.mu.RUnlock()

	if history != nil {
		records, err := history.ListClosedSessions(limit)
		if err != nil {
			slog.Warn("load closed sessions", "error", err)
		}
		for _, rec := range records {
			result = append(result, rec.restore())
		}
	}
	return result
}

func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.active)
}

func (m *Manager) TotalUpload() int64   { return m.totalUpload.Load() }
func (m *Manager) TotalDownload() int64 { return m.totalDownload.Load() }
func (m *Manager) TotalSessions() int64 { return int64(m.nextID.Load()) }

// Snapshot returns current upload/download for active sessions too
func (m *Manager) TrafficSnapshot() (upload, download int64) {
	upload = m.totalUpload.Load()
	download = m.totalDownload.Load()
	m.mu.RLock()
	for _, s := range m.active {
		upload += s.Upload.Load()
		download += s.Download.Load()
	}
	m.mu.RUnlock()
	return
}

func (m *Manager) TrafficRateSnapshot() (upload, download, uploadBPS, downloadBPS int64) {
	now := time.Now()
	upload, download = m.TrafficSnapshot()

	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	if !m.rateAt.IsZero() {
		elapsed := now.Sub(m.rateAt).Seconds()
		if elapsed > 0 {
			uploadBPS = int64(float64(upload-m.rateUpload) / elapsed)
			downloadBPS = int64(float64(download-m.rateDownload) / elapsed)
			if uploadBPS < 0 {
				uploadBPS = 0
			}
			if downloadBPS < 0 {
				downloadBPS = 0
			}
		}
	}
	m.rateAt = now
	m.rateUpload = upload
	m.rateDownload = download
	return
}

func (m *Manager) addActiveFakeIPLocked(s *Session) {
	if s.FakeIP == "" {
		return
	}
	ids := m.activeFakeIPs[s.FakeIP]
	if ids == nil {
		ids = make(map[string]struct{})
		m.activeFakeIPs[s.FakeIP] = ids
	}
	ids[s.ID] = struct{}{}
}

func (m *Manager) removeActiveFakeIPLocked(s *Session) {
	if s.FakeIP == "" {
		return
	}
	ids := m.activeFakeIPs[s.FakeIP]
	if ids == nil {
		return
	}
	delete(ids, s.ID)
	if len(ids) == 0 {
		delete(m.activeFakeIPs, s.FakeIP)
	}
}
