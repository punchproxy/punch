package session

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Status string

const (
	StatusActive Status = "ACTIVE"
	StatusClosed Status = "CLOSED"
	StatusError  Status = "ERROR"
)

// Session describes one proxied connection. The exported fields are immutable
// once NewSession returns; every mutable field lives below mu and is reached
// through accessors, so readers (the API, the manager) never observe a torn
// write. Sessions become visible to KillSession before the relay dial
// completes, so "only the owning goroutine touches this" is never true here.
type Session struct {
	ID             string
	Domain         string
	Source         string
	DstIP          string
	DstPort        int
	Protocol       string
	Rule           string
	Process        string
	FakeIP         string
	Upload         atomic.Int64
	Download       atomic.Int64
	StartTime      time.Time
	DNSRequestedAt time.Time

	mu            sync.RWMutex
	status        Status
	relay         string
	endTime       time.Time
	connectedAt   time.Time
	requestSentAt time.Time
	firstByteAt   time.Time
	closeReason   string
	trace         []TraceEntry
	closed        bool
	closeFn       func()
	updateFn      func()
}

type TraceEntry struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (s *Session) UploadBytes() int64   { return s.Upload.Load() }
func (s *Session) DownloadBytes() int64 { return s.Download.Load() }

func (s *Session) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Session) EndTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endTime
}

func (s *Session) Relay() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.relay
}

// SetRelay records which relay actually carried the connection. Callers pass
// the name resolved by the dial itself rather than re-reading the selector,
// which may have switched relays in the meantime.
func (s *Session) SetRelay(name string) {
	s.mu.Lock()
	s.relay = name
	s.mu.Unlock()
}

// Close runs the bound closer exactly once. A Close that lands before the
// closer is bound (a kill racing the relay dial) is remembered, and
// SetCloseFunc runs it as soon as the connections exist — otherwise the kill
// would report success while the connection kept running.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	fn := s.closeFn
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (s *Session) SetCloseFunc(fn func()) {
	s.mu.Lock()
	s.closeFn = fn
	pending := s.closed
	s.mu.Unlock()
	if pending && fn != nil {
		fn()
	}
}

func (s *Session) SetUpdateFunc(fn func()) {
	s.mu.Lock()
	s.updateFn = fn
	s.mu.Unlock()
}

func (s *Session) MarkConnected() {
	s.mu.Lock()
	if !s.connectedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.connectedAt = now
	s.trace = append(s.trace, TraceEntry{At: now, Message: "Relay connected"})
	update := s.updateFn
	s.mu.Unlock()
	if update != nil {
		update()
	}
}

func (s *Session) MarkRequestSent() {
	s.mu.Lock()
	if !s.requestSentAt.IsZero() {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.requestSentAt = now
	s.trace = append(s.trace, TraceEntry{At: now, Message: "Request sent"})
	update := s.updateFn
	s.mu.Unlock()
	if update != nil {
		update()
	}
}

func (s *Session) MarkFirstByte() {
	s.mu.Lock()
	if !s.firstByteAt.IsZero() {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	s.firstByteAt = now
	s.trace = append(s.trace, TraceEntry{At: now, Message: "First byte received"})
	update := s.updateFn
	s.mu.Unlock()
	if update != nil {
		update()
	}
}

// markClosed stamps the terminal state and closing trace entries. The manager
// calls it after dropping the session from its active map, so every write to
// the session still happens under the session's own lock.
func (s *Session) markClosed(status Status) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.endTime = time.Now()
	if status == StatusError && s.closeReason != "" {
		s.trace = append(s.trace, TraceEntry{At: s.endTime, Message: "error occurred: " + s.closeReason})
	}
	s.trace = append(s.trace, TraceEntry{At: s.endTime, Message: "Session closed"})
	return s.endTime
}

func (s *Session) ConnectedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connectedAt
}

func (s *Session) FirstByteAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstByteAt
}

func (s *Session) SetCloseReason(reason string) {
	s.mu.Lock()
	s.closeReason = reason
	s.mu.Unlock()
}

func (s *Session) CloseReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closeReason
}

func (s *Session) AppendTraceAt(at time.Time, message string) {
	if message == "" || at.IsZero() {
		return
	}
	s.mu.Lock()
	s.trace = append(s.trace, TraceEntry{At: at, Message: message})
	update := s.updateFn
	s.mu.Unlock()
	if update != nil {
		update()
	}
}

func (s *Session) Trace() []TraceEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	trace := make([]TraceEntry, len(s.trace))
	copy(trace, s.trace)
	return trace
}

func (s *Session) RecordUpload(n int) {
	if n <= 0 {
		return
	}
	s.Upload.Add(int64(n))
	s.MarkRequestSent()
}

func (s *Session) RecordDownload(n int) {
	if n <= 0 {
		return
	}
	s.Download.Add(int64(n))
	s.MarkFirstByte()
}

// TrackedConn wraps a net.Conn to track bytes transferred.
type TrackedConn struct {
	net.Conn
	session *Session
	upload  bool // true = upload direction
}

func NewTrackedConn(conn net.Conn, s *Session, upload bool) *TrackedConn {
	return &TrackedConn{Conn: conn, session: s, upload: upload}
}

func (t *TrackedConn) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)
	if n > 0 {
		if t.upload {
			t.session.RecordUpload(n)
		} else {
			t.session.RecordDownload(n)
		}
	}
	return n, err
}

func (t *TrackedConn) Write(b []byte) (int, error) {
	n, err := t.Conn.Write(b)
	if n > 0 {
		if t.upload {
			t.session.RecordUpload(n)
		} else {
			t.session.RecordDownload(n)
		}
	}
	return n, err
}
