package session

import "time"

// ClosedRecord is an immutable snapshot of a finished session, suitable for
// persistence outside the manager's memory.
type ClosedRecord struct {
	ID             string
	Status         Status
	Domain         string
	Source         string
	DstIP          string
	DstPort        int
	Protocol       string
	Relay          string
	Rule           string
	Process        string
	FakeIP         string
	Upload         int64
	Download       int64
	StartTime      time.Time
	EndTime        time.Time
	DNSRequestedAt time.Time
	CloseReason    string
	Trace          []TraceEntry
}

// HistoryStore persists closed sessions so the manager does not have to keep
// them in memory. Implementations must be safe for concurrent use. History is
// per-run: the daemon clears the store on startup, so session IDs only need to
// be unique within one process lifetime.
type HistoryStore interface {
	// AppendClosedSession stores rec and prunes the history to at most limit
	// records (keeping the newest) when limit is positive.
	AppendClosedSession(rec ClosedRecord, limit int) error
	// ListClosedSessions returns up to limit records, newest first. Traces may
	// be omitted; use GetClosedSession for the full record.
	ListClosedSessions(limit int) ([]ClosedRecord, error)
	// GetClosedSession returns the record for id, including its trace.
	GetClosedSession(id string) (ClosedRecord, bool, error)
	// ClearClosedSessions removes every record and reports how many were removed.
	ClearClosedSessions() (int, error)
}

// closedRecord snapshots a session after it has been closed; the flat fields
// are no longer written at that point.
func (s *Session) closedRecord() ClosedRecord {
	return ClosedRecord{
		ID:             s.ID,
		Status:         s.Status,
		Domain:         s.Domain,
		Source:         s.Source,
		DstIP:          s.DstIP,
		DstPort:        s.DstPort,
		Protocol:       s.Protocol,
		Relay:          s.Relay,
		Rule:           s.Rule,
		Process:        s.Process,
		FakeIP:         s.FakeIP,
		Upload:         s.Upload.Load(),
		Download:       s.Download.Load(),
		StartTime:      s.StartTime,
		EndTime:        s.EndTime,
		DNSRequestedAt: s.DNSRequestedAt,
		CloseReason:    s.CloseReason(),
		Trace:          s.Trace(),
	}
}

// restore rebuilds a read-only Session view from a persisted record so that
// existing consumers (API responses, traces) work unchanged.
func (r ClosedRecord) restore() *Session {
	s := &Session{
		ID:             r.ID,
		Status:         r.Status,
		Domain:         r.Domain,
		Source:         r.Source,
		DstIP:          r.DstIP,
		DstPort:        r.DstPort,
		Protocol:       r.Protocol,
		Relay:          r.Relay,
		Rule:           r.Rule,
		Process:        r.Process,
		FakeIP:         r.FakeIP,
		StartTime:      r.StartTime,
		EndTime:        r.EndTime,
		DNSRequestedAt: r.DNSRequestedAt,
	}
	s.Upload.Store(r.Upload)
	s.Download.Store(r.Download)
	s.closeReason = r.CloseReason
	s.trace = append([]TraceEntry(nil), r.Trace...)
	return s
}
