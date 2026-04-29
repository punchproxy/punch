package session

import (
	"testing"

	"github.com/punchproxy/punch/internal/eventbus"
)

func TestHistoryLimitDoesNotCapActiveSessions(t *testing.T) {
	m := NewManager(eventbus.New(), 1)
	first := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "DIRECT", "", SessionOpts{})
	second := m.NewSession("two.example", "127.0.0.1:1001", "198.18.0.2", 443, "TCP", "DIRECT", "", SessionOpts{})

	if got := len(m.RecentSessions()); got != 2 {
		t.Fatalf("active recent sessions = %d, want 2", got)
	}

	m.CloseSession(first.ID, StatusClosed)
	recent := m.RecentSessions()
	if len(recent) != 1 {
		t.Fatalf("recent sessions after close = %d, want 1", len(recent))
	}
	if recent[0].ID != second.ID {
		t.Fatalf("kept session = %s, want active %s", recent[0].ID, second.ID)
	}
}
