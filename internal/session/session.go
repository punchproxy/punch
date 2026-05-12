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

type Session struct {
	ID             string       `json:"id"`
	Status         Status       `json:"status"`
	Domain         string       `json:"domain"`
	Source         string       `json:"source"`
	DstIP          string       `json:"dst_ip"`
	DstPort        int          `json:"dst_port"`
	Protocol       string       `json:"protocol"`
	Relay          string       `json:"relay"`
	Rule           string       `json:"rule"`
	Process        string       `json:"process"`
	FakeIP         string       `json:"fake_ip,omitempty"`
	Upload         atomic.Int64 `json:"-"`
	Download       atomic.Int64 `json:"-"`
	StartTime      time.Time    `json:"start_time"`
	EndTime        time.Time    `json:"end_time,omitempty"`
	DNSRequestedAt time.Time    `json:"dns_requested_at,omitempty"`

	mu            sync.RWMutex
	connectedAt   time.Time
	requestSentAt time.Time
	firstByteAt   time.Time
	closeReason   string
	trace         []TraceEntry
	closeOnce     sync.Once
	closeFn       func()
	updateFn      func()
}

type TraceEntry struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (s *Session) UploadBytes() int64   { return s.Upload.Load() }
func (s *Session) DownloadBytes() int64 { return s.Download.Load() }

func (s *Session) Close() {
	fn := s.closeFn
	if fn != nil {
		s.closeOnce.Do(fn)
	}
}

func (s *Session) SetCloseFunc(fn func()) {
	s.closeFn = fn
}

func (s *Session) SetUpdateFunc(fn func()) {
	s.updateFn = fn
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
