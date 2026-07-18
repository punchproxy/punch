package api

import (
	"context"
	"net/http"
	"runtime"
	"time"

	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/tun"
)

type statusResponse struct {
	General      statusGeneralResponse    `json:"general"`
	DNS          pdns.Stats               `json:"dns"`
	Connectivity relay.ConnectivityStatus `json:"connectivity"`
	RelayGroups  []relay.GroupStatus      `json:"relay_groups,omitempty"`
	Relay        statusRelayResponse      `json:"relay"`
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
	ActiveRelay            string             `json:"active_relay"`
	Status                 string             `json:"status,omitempty"`
	LatencyMS              int64              `json:"latency_ms,omitempty"`
	URLTestLatencyMS       int64              `json:"url_test_latency_ms,omitempty"`
	LastCheckedAt          time.Time          `json:"last_checked_at,omitempty"`
	ActiveSessions         int                `json:"active_sessions"`
	TotalProcessedSessions int64              `json:"total_processed_sessions"`
	UploadBytes            int64              `json:"upload_bytes"`
	DownloadBytes          int64              `json:"download_bytes"`
	UploadBPS              int64              `json:"upload_bps"`
	DownloadBPS            int64              `json:"download_bps"`
	ThroughputHistory      []throughputSample `json:"throughput_history,omitempty"`
	UDP                    tun.UDPStats       `json:"udp"`
}

type throughputSample struct {
	Time        time.Time `json:"time"`
	UploadBPS   int64     `json:"upload_bps"`
	DownloadBPS int64     `json:"download_bps"`
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
	if s.selector != nil {
		resp.Connectivity = s.selector.ConnectivityStatus()
		resp.RelayGroups = s.selector.GroupList()
	}
	resp.Relay = s.relayStatus(now)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) relayStatus(now time.Time) statusRelayResponse {
	status := statusRelayResponse{}
	if s.selector != nil {
		status.ActiveRelay = s.selector.ActiveName()
		for _, h := range s.selector.HealthList() {
			if !h.Selected {
				continue
			}
			status.Status = string(h.Status)
			status.LatencyMS = h.Latency
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
		status.UploadBytes, status.DownloadBytes, status.UploadBPS, status.DownloadBPS, status.ThroughputHistory = s.trafficStatus(now)
	}
	if s.tun != nil {
		status.UDP = s.tun.UDPStats()
	}
	return status
}

const (
	throughputWindow         = 120 * time.Second
	throughputSampleInterval = 2 * time.Second
)

func (s *Server) startStatusSampler() {
	if s.sessions == nil || s.statusSamplerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.statusSamplerCancel = cancel
	s.sampleTraffic(time.Now())
	go func() {
		ticker := time.NewTicker(throughputSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sampleTraffic(time.Now())
			}
		}
	}()
}

func (s *Server) sampleTraffic(now time.Time) {
	if s.sessions == nil {
		return
	}
	upload, download := s.sessions.TrafficSnapshot()
	s.throughputMu.Lock()
	defer s.throughputMu.Unlock()
	s.recordThroughputLocked(now, upload, download)
}

func (s *Server) trafficStatus(now time.Time) (upload, download, uploadBPS, downloadBPS int64, history []throughputSample) {
	upload, download = s.sessions.TrafficSnapshot()
	s.throughputMu.Lock()
	defer s.throughputMu.Unlock()
	if s.throughputLastAt.IsZero() || now.Sub(s.throughputLastAt) >= throughputSampleInterval {
		s.recordThroughputLocked(now, upload, download)
	}
	if len(s.throughputHistory) > 0 {
		latest := s.throughputHistory[len(s.throughputHistory)-1]
		uploadBPS = latest.UploadBPS
		downloadBPS = latest.DownloadBPS
	}
	history = append([]throughputSample(nil), s.throughputHistory...)
	return
}

func (s *Server) recordThroughputLocked(now time.Time, upload, download int64) {
	if !s.throughputLastAt.IsZero() && now.Sub(s.throughputLastAt) < throughputSampleInterval {
		return
	}
	sample := throughputSample{Time: now}
	if !s.throughputLastAt.IsZero() {
		elapsed := now.Sub(s.throughputLastAt).Seconds()
		if elapsed > 0 {
			sample.UploadBPS = nonNegativeRate(upload-s.throughputLastUpload, elapsed)
			sample.DownloadBPS = nonNegativeRate(download-s.throughputLastDownload, elapsed)
		}
	}
	s.throughputLastAt = now
	s.throughputLastUpload = upload
	s.throughputLastDownload = download
	s.throughputHistory = append(s.throughputHistory, sample)
	cutoff := now.Add(-throughputWindow)
	first := 0
	for first < len(s.throughputHistory) && s.throughputHistory[first].Time.Before(cutoff) {
		first++
	}
	if first > 0 {
		s.throughputHistory = append([]throughputSample(nil), s.throughputHistory[first:]...)
	}
}

func nonNegativeRate(delta int64, elapsed float64) int64 {
	if delta <= 0 || elapsed <= 0 {
		return 0
	}
	return int64(float64(delta) / elapsed)
}
