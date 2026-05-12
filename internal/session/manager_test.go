package session

import (
	"reflect"
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

func TestActiveSessionIDsByFakeIPTracksOnlyActiveSessions(t *testing.T) {
	m := NewManager(eventbus.New(), 10)
	first := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.4", 443, "TCP", "DIRECT", "fake-ip", SessionOpts{FakeIP: "198.18.0.4"})
	second := m.NewSession("two.example", "127.0.0.1:1001", "198.18.0.4", 443, "TCP", "DIRECT", "fake-ip", SessionOpts{FakeIP: "198.18.0.4"})
	m.NewSession("direct.example", "127.0.0.1:1002", "93.184.216.34", 443, "TCP", "DIRECT", "direct", SessionOpts{})

	got := m.ActiveSessionIDsByFakeIP()
	if !reflect.DeepEqual(got["198.18.0.4"], []string{first.ID, second.ID}) {
		t.Fatalf("active fake-ip sessions = %v, want [%s %s]", got["198.18.0.4"], first.ID, second.ID)
	}

	m.CloseSession(first.ID, StatusClosed)
	got = m.ActiveSessionIDsByFakeIP()
	if !reflect.DeepEqual(got["198.18.0.4"], []string{second.ID}) {
		t.Fatalf("active fake-ip sessions after close = %v, want [%s]", got["198.18.0.4"], second.ID)
	}
}

func TestCloseSessionRunsCloseFuncOnce(t *testing.T) {
	m := NewManager(eventbus.New(), 10)
	s := m.NewSession("one.example", "127.0.0.1:1000", "198.18.0.4", 443, "TCP", "DIRECT", "fake-ip", SessionOpts{FakeIP: "198.18.0.4"})
	called := 0
	s.SetCloseFunc(func() {
		called++
	})

	s.Close()
	m.CloseSession(s.ID, StatusClosed)

	if called != 1 {
		t.Fatalf("close func called %d times, want 1", called)
	}
}
