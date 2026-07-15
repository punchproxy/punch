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
	if len(got.Relay.ThroughputHistory) != 1 {
		t.Fatalf("throughput history length = %d, want 1", len(got.Relay.ThroughputHistory))
	}
	if got.Relay.ThroughputHistory[0].Time.IsZero() {
		t.Fatal("throughput history timestamp is empty")
	}
}

func TestThroughputHistoryKeepsLast120Seconds(t *testing.T) {
	s := &Server{}
	start := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)

	s.throughputMu.Lock()
	s.recordThroughputLocked(start, 100, 200)
	s.recordThroughputLocked(start.Add(2*time.Second), 300, 800)
	s.recordThroughputLocked(start.Add(122*time.Second), 500, 1200)
	history := append([]throughputSample(nil), s.throughputHistory...)
	s.throughputMu.Unlock()

	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if !history[0].Time.Equal(start.Add(2 * time.Second)) {
		t.Fatalf("oldest sample = %s, want %s", history[0].Time, start.Add(2*time.Second))
	}
	if history[0].UploadBPS != 100 || history[0].DownloadBPS != 300 {
		t.Fatalf("rates = up %d down %d, want up 100 down 300", history[0].UploadBPS, history[0].DownloadBPS)
	}
}
