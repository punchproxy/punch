package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/session"
)

func TestHandleStatus(t *testing.T) {
	mgr := session.NewManager(eventbus.New(), 1000)
	sess := mgr.NewSession("example.com", "127.0.0.1:12345", "93.184.216.34", 443, "tcp", "DIRECT", "direct-domain", session.SessionOpts{})
	sess.RecordDownload(2048)
	sess.RecordUpload(1024)

	started := time.Now().Add(-2 * time.Minute)
	s := &Server{
		sessions:  mgr,
		startedAt: started,
		version:   "test-version",
	}

	rec := runRelayHandler(t, s.handleStatus, http.MethodGet, "/api/status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got statusResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.General.Version != "test-version" {
		t.Fatalf("version = %q, want test-version", got.General.Version)
	}
	if got.General.Architecture == "" {
		t.Fatalf("architecture is empty")
	}
	if got.General.UptimeSeconds < 100 {
		t.Fatalf("uptime = %d, want at least 100", got.General.UptimeSeconds)
	}
	if got.Relay.ActiveRelay != "DIRECT" {
		t.Fatalf("active relay = %q, want DIRECT", got.Relay.ActiveRelay)
	}
	if got.Relay.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d, want 1", got.Relay.ActiveSessions)
	}
	if got.Relay.TotalProcessedSessions != 1 {
		t.Fatalf("total processed sessions = %d, want 1", got.Relay.TotalProcessedSessions)
	}
	if got.Relay.DownloadBytes != 2048 || got.Relay.UploadBytes != 1024 {
		t.Fatalf("traffic = down %d up %d, want down 2048 up 1024", got.Relay.DownloadBytes, got.Relay.UploadBytes)
	}
}
