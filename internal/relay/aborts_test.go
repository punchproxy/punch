package relay

import (
	"testing"
	"time"
)

func TestStreamAbortStatsWindow(t *testing.T) {
	s := &Selector{aborts: make(map[string]*abortStats)}

	if recent, total := s.StreamAbortStats("US / relay-1"); recent != 0 || total != 0 {
		t.Fatalf("empty stats = (%d, %d), want (0, 0)", recent, total)
	}

	s.ReportStreamAbort("US / relay-1")
	s.ReportStreamAbort("US / relay-1")
	s.ReportStreamAbort("US / relay-2")

	if recent, total := s.StreamAbortStats("US / relay-1"); recent != 2 || total != 2 {
		t.Fatalf("relay-1 stats = (%d, %d), want (2, 2)", recent, total)
	}
	if recent, total := s.StreamAbortStats("US / relay-2"); recent != 1 || total != 1 {
		t.Fatalf("relay-2 stats = (%d, %d), want (1, 1)", recent, total)
	}

	// Aging the window keeps the running total but zeroes the recent count.
	s.aborts["US / relay-1"].windowStart = time.Now().Add(-2 * streamAbortWindow)
	if recent, total := s.StreamAbortStats("US / relay-1"); recent != 0 || total != 2 {
		t.Fatalf("aged stats = (%d, %d), want (0, 2)", recent, total)
	}

	// The next report starts a fresh window on top of the total.
	s.ReportStreamAbort("US / relay-1")
	if recent, total := s.StreamAbortStats("US / relay-1"); recent != 1 || total != 3 {
		t.Fatalf("stats after new window = (%d, %d), want (1, 3)", recent, total)
	}

	// Blank relay names (no active relay) are ignored.
	s.ReportStreamAbort("")
	if _, ok := s.aborts[""]; ok {
		t.Fatal("blank relay name was recorded")
	}
}
