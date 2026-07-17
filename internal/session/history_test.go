package session

import (
	"sync"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

type fakeHistoryStore struct {
	mu      sync.Mutex
	records []ClosedRecord
	limit   int
	cleared int
}

func (f *fakeHistoryStore) AppendClosedSession(rec ClosedRecord, limit int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	f.limit = limit
	if limit > 0 && len(f.records) > limit {
		f.records = f.records[len(f.records)-limit:]
	}
	return nil
}

func (f *fakeHistoryStore) ListClosedSessions(limit int) ([]ClosedRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ClosedRecord, 0, len(f.records))
	for i := len(f.records) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		out = append(out, f.records[i])
	}
	return out, nil
}

func (f *fakeHistoryStore) GetClosedSession(id string) (ClosedRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rec := range f.records {
		if rec.ID == id {
			return rec, true, nil
		}
	}
	return ClosedRecord{}, false, nil
}

func (f *fakeHistoryStore) ClearClosedSessions() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.records)
	f.records = nil
	f.cleared += n
	return n, nil
}

func TestCloseSessionSpillsToHistoryStore(t *testing.T) {
	store := &fakeHistoryStore{}
	m := NewManager(eventbus.New(), 5)
	m.SetHistoryStore(store)

	s := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "US / relay-1", "gfw", SessionOpts{})
	s.RecordUpload(100)
	s.RecordDownload(200)
	s.SetCloseReason("peer reset")
	m.CloseSession(s.ID, StatusError)

	if len(m.ClosedSessions()) != 1 {
		t.Fatalf("closed sessions = %d, want 1", len(m.ClosedSessions()))
	}
	if len(m.closed) != 0 {
		t.Fatalf("in-memory closed buffer holds %d sessions, want 0", len(m.closed))
	}
	rec := store.records[0]
	if rec.ID != s.ID || rec.Status != StatusError || rec.Upload != 100 || rec.Download != 200 {
		t.Fatalf("persisted record = %+v, want fields from closed session", rec)
	}
	if rec.CloseReason != "peer reset" {
		t.Fatalf("persisted close reason = %q, want %q", rec.CloseReason, "peer reset")
	}
	if len(rec.Trace) == 0 {
		t.Fatal("persisted record has no trace")
	}
	if store.limit != 5 {
		t.Fatalf("append limit = %d, want 5", store.limit)
	}
}

func TestRecentSessionsMergesActiveAndHistory(t *testing.T) {
	store := &fakeHistoryStore{}
	m := NewManager(eventbus.New(), 5)
	m.SetHistoryStore(store)

	first := m.NewSession("old.example", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "", "", SessionOpts{})
	m.CloseSession(first.ID, StatusClosed)
	second := m.NewSession("live.example", "127.0.0.1:1001", "198.18.0.2", 443, "TCP", "", "", SessionOpts{})

	recent := m.RecentSessions()
	if len(recent) != 2 {
		t.Fatalf("recent sessions = %d, want 2", len(recent))
	}
	if recent[0].ID != second.ID {
		t.Fatalf("newest session = %s, want active %s", recent[0].ID, second.ID)
	}
	if recent[1].ID != first.ID || recent[1].Status != StatusClosed {
		t.Fatalf("restored session = %s (%s), want %s (CLOSED)", recent[1].ID, recent[1].Status, first.ID)
	}
}

func TestSessionLookupReadsHistoryStore(t *testing.T) {
	store := &fakeHistoryStore{}
	m := NewManager(eventbus.New(), 5)
	m.SetHistoryStore(store)

	s := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "", "", SessionOpts{})
	s.AppendTraceAt(time.Now(), "custom event")
	m.CloseSession(s.ID, StatusClosed)

	got, ok := m.Session(s.ID)
	if !ok {
		t.Fatal("closed session not found via history store")
	}
	if got.ID != s.ID || got.Domain != "one.example" {
		t.Fatalf("restored session = %+v, want id %s domain one.example", got, s.ID)
	}
	trace := got.Trace()
	found := false
	for _, entry := range trace {
		if entry.Message == "custom event" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored trace %v missing custom event", trace)
	}

	if _, ok := m.Session("missing"); ok {
		t.Fatal("unexpected hit for unknown session id")
	}
}

func TestClearNonActiveClearsHistoryStore(t *testing.T) {
	store := &fakeHistoryStore{}
	m := NewManager(eventbus.New(), 5)
	m.SetHistoryStore(store)

	s := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "", "", SessionOpts{})
	m.CloseSession(s.ID, StatusClosed)

	if cleared := m.ClearNonActive(); cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if len(m.ClosedSessions()) != 0 {
		t.Fatal("history store still has sessions after clear")
	}
}
