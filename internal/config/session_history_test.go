package config

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/session"
)

func testClosedRecord(id string, closedAt time.Time) session.ClosedRecord {
	return session.ClosedRecord{
		ID:          id,
		Status:      session.StatusClosed,
		Domain:      "example.com",
		Source:      "192.168.1.5:52344",
		DstIP:       "93.184.216.34",
		DstPort:     443,
		Protocol:    "TCP",
		Relay:       "US / relay-1",
		Rule:        "gfw",
		Upload:      100,
		Download:    200,
		StartTime:   closedAt.Add(-time.Minute),
		EndTime:     closedAt,
		CloseReason: "done",
		Trace: []session.TraceEntry{
			{At: closedAt.Add(-time.Minute), Message: "TUN connection received"},
			{At: closedAt, Message: "Session closed"},
		},
	}
}

func TestSessionHistoryRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	rec := testClosedRecord("1", now)
	rec.DNSRequestedAt = now.Add(-2 * time.Minute)
	if err := s.AppendClosedSession(rec, 10); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, ok, err := s.GetClosedSession("1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Domain != rec.Domain || got.Relay != rec.Relay || got.Upload != 100 || got.Download != 200 || got.CloseReason != "done" {
		t.Fatalf("got %+v, want persisted fields", got)
	}
	if !got.StartTime.Equal(rec.StartTime) || !got.EndTime.Equal(rec.EndTime) || !got.DNSRequestedAt.Equal(rec.DNSRequestedAt) {
		t.Fatalf("timestamps not preserved: got %v/%v/%v", got.StartTime, got.EndTime, got.DNSRequestedAt)
	}
	if len(got.Trace) != 2 || got.Trace[1].Message != "Session closed" {
		t.Fatalf("trace not preserved: %+v", got.Trace)
	}

	// Zero DNSRequestedAt must survive the round trip as a zero time.
	rec2 := testClosedRecord("2", now.Add(time.Second))
	if err := s.AppendClosedSession(rec2, 10); err != nil {
		t.Fatalf("append second: %v", err)
	}
	got2, ok, err := s.GetClosedSession("2")
	if err != nil || !ok {
		t.Fatalf("get second: ok=%v err=%v", ok, err)
	}
	if !got2.DNSRequestedAt.IsZero() {
		t.Fatalf("zero DNS time became %v", got2.DNSRequestedAt)
	}

	if _, ok, err := s.GetClosedSession("missing"); err != nil || ok {
		t.Fatalf("missing lookup: ok=%v err=%v", ok, err)
	}
}

func TestSessionHistoryPruneAndOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	for i := 1; i <= 5; i++ {
		rec := testClosedRecord(fmt.Sprintf("%d", i), now.Add(time.Duration(i)*time.Second))
		if err := s.AppendClosedSession(rec, 3); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	rows, err := s.ListClosedSessions(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows after prune = %d, want 3", len(rows))
	}
	if rows[0].ID != "5" || rows[2].ID != "3" {
		t.Fatalf("order = [%s..%s], want newest-first [5..3]", rows[0].ID, rows[2].ID)
	}

	limited, err := s.ListClosedSessions(2)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "5" {
		t.Fatalf("limited list = %+v, want 2 newest", limited)
	}
}

func TestSessionHistoryClearAndUpsert(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.AppendClosedSession(testClosedRecord("1", now), 10); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Re-appending the same session id (e.g. rows left over from a previous
	// run that failed to clear) must not error.
	dup := testClosedRecord("1", now.Add(time.Second))
	dup.Upload = 999
	if err := s.AppendClosedSession(dup, 10); err != nil {
		t.Fatalf("append duplicate id: %v", err)
	}
	got, ok, err := s.GetClosedSession("1")
	if err != nil || !ok {
		t.Fatalf("get after upsert: ok=%v err=%v", ok, err)
	}
	if got.Upload != 999 {
		t.Fatalf("upsert kept stale row: upload=%d, want 999", got.Upload)
	}

	cleared, err := s.ClearClosedSessions()
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	rows, err := s.ListClosedSessions(0)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after clear = %d, want 0", len(rows))
	}
}
