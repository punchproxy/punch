package api

import (
	"net/http"
	"runtime"
	"time"

	pdns "github.com/punchproxy/punch/internal/dns"
)

type statusResponse struct {
	General statusGeneralResponse `json:"general"`
	DNS     pdns.Stats            `json:"dns"`
	Relay   statusRelayResponse   `json:"relay"`
}

type statusGeneralResponse struct {
	Version       string    `json:"version"`
	Architecture  string    `json:"architecture"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	StartedAt     time.Time `json:"started_at"`
	MemoryBytes   uint64    `json:"memory_bytes"`
	Goroutines    int       `json:"goroutines"`
}

type statusRelayResponse struct {
	ActiveRelay            string    `json:"active_relay"`
	Status                 string    `json:"status,omitempty"`
	LatencyMS              int64     `json:"latency_ms,omitempty"`
	TCPConnectLatencyMS    int64     `json:"tcp_connect_latency_ms,omitempty"`
	URLTestLatencyMS       int64     `json:"url_test_latency_ms,omitempty"`
	LastCheckedAt          time.Time `json:"last_checked_at,omitempty"`
	ActiveSessions         int       `json:"active_sessions"`
	TotalProcessedSessions int64     `json:"total_processed_sessions"`
	UploadBytes            int64     `json:"upload_bytes"`
	DownloadBytes          int64     `json:"download_bytes"`
	UploadBPS              int64     `json:"upload_bps"`
	DownloadBPS            int64     `json:"download_bps"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	now := time.Now()
	resp := statusResponse{
		General: statusGeneralResponse{
			Version:       s.version,
			Architecture:  runtime.GOOS + "/" + runtime.GOARCH,
			UptimeSeconds: int64(now.Sub(s.startedAt).Seconds()),
			StartedAt:     s.startedAt,
			MemoryBytes:   mem.Alloc,
			Goroutines:    runtime.NumGoroutine(),
		},
	}
	if s.dns != nil {
		resp.DNS = s.dns.Stats()
	}
	resp.Relay = s.relayStatus()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) relayStatus() statusRelayResponse {
	status := statusRelayResponse{}
	if s.selector != nil {
		status.ActiveRelay = s.selector.ActiveName()
		for _, h := range s.selector.HealthList() {
			if !h.Selected {
				continue
			}
			status.Status = string(h.Status)
			status.LatencyMS = h.Latency
			status.TCPConnectLatencyMS = h.TCPConnectLatency
			status.URLTestLatencyMS = h.URLTestLatency
			status.LastCheckedAt = h.LastCheckedAt
			break
		}
	}
	if status.ActiveRelay == "" {
		status.ActiveRelay = "DIRECT"
	}
	if s.sessions != nil {
		status.ActiveSessions = s.sessions.ActiveCount()
		status.TotalProcessedSessions = s.sessions.TotalSessions()
		status.UploadBytes, status.DownloadBytes, status.UploadBPS, status.DownloadBPS = s.sessions.TrafficRateSnapshot()
	}
	return status
}
