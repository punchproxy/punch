package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/session"
)

func TestSessionHandlersListGetAndTerminate(t *testing.T) {
	mgr := session.NewManager(eventbus.New(), 1000)
	active := mgr.NewSession("active.example", "198.18.0.1:1111", "198.18.1.2", 443, "TCP", "main / hk-1", "fake-ip", session.SessionOpts{
		FakeIP:         "198.18.1.2",
		DNSRequestedAt: time.Now().Add(-time.Second),
	})
	active.RecordUpload(2048)
	active.RecordDownload(1024)

	closed := mgr.NewSession("closed.example", "198.18.0.1:2222", "198.18.1.3", 443, "TCP", "main / hk-2", "fake-ip", session.SessionOpts{})
	mgr.CloseSession(closed.ID, session.StatusClosed)

	s := &Server{sessions: mgr}
	rec := runRelayHandler(t, s.handleSessions, http.MethodGet, "/api/sessions", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var list []sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}

	rec = runRelayHandler(t, s.handleSession, http.MethodGet, "/api/sessions/"+active.ID, map[string]string{"id": active.ID}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var detail sessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != active.ID || detail.Destination != "active.example:443" || len(detail.Trace) == 0 {
		t.Fatalf("detail = %#v", detail)
	}

	killed := false
	active.SetCloseFunc(func() { killed = true })
	rec = runRelayHandler(t, s.handleTerminateSession, http.MethodDelete, "/api/sessions/"+active.ID, map[string]string{"id": active.ID}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !killed {
		t.Fatal("terminate did not invoke close function")
	}
}
