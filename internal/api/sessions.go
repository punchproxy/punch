package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/punchproxy/punch/internal/session"
)

type sessionResponse struct {
	ID          string                 `json:"id"`
	Status      session.Status         `json:"status"`
	Domain      string                 `json:"domain"`
	Destination string                 `json:"destination"`
	Source      string                 `json:"source"`
	DstIP       string                 `json:"dst_ip"`
	DstPort     int                    `json:"dst_port"`
	Protocol    string                 `json:"protocol"`
	Relay       string                 `json:"relay"`
	Rule        string                 `json:"rule"`
	Process     string                 `json:"process,omitempty"`
	FakeIP      string                 `json:"fake_ip,omitempty"`
	Upload      int64                  `json:"upload_bytes"`
	Download    int64                  `json:"download_bytes"`
	Established time.Time              `json:"established"`
	ClosedAt    time.Time              `json:"closed_at,omitempty"`
	DurationMS  int64                  `json:"duration_ms"`
	Trace       []sessionTraceResponse `json:"trace,omitempty"`
}

type sessionTraceResponse struct {
	At       time.Time `json:"at"`
	OffsetMS int64     `json:"offset_ms"`
	Message  string    `json:"message"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session manager unavailable"})
		return
	}
	sessions := s.sessions.RecentSessions()
	out := make([]sessionResponse, 0, len(sessions))
	for _, item := range sessions {
		out = append(out, buildSessionResponse(item, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session manager unavailable"})
		return
	}
	item, ok := s.sessions.Session(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, buildSessionResponse(item, true))
}

func (s *Server) handleTerminateSession(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session manager unavailable"})
		return
	}
	id := chi.URLParam(r, "id")
	if !s.sessions.KillSession(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "active session not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

func (s *Server) handleTerminateSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session manager unavailable"})
		return
	}
	if r.URL.Query().Get("all") != "true" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing all=true"})
		return
	}
	killed := s.sessions.KillAllSessions()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "terminated": killed})
}

func buildSessionResponse(s *session.Session, includeTrace bool) sessionResponse {
	now := time.Now()
	end := s.EndTime
	if end.IsZero() {
		end = now
	}
	resp := sessionResponse{
		ID:          s.ID,
		Status:      s.Status,
		Domain:      s.Domain,
		Destination: sessionDestination(s),
		Source:      s.Source,
		DstIP:       s.DstIP,
		DstPort:     s.DstPort,
		Protocol:    fmt.Sprintf("%s:%d", s.Protocol, s.DstPort),
		Relay:       s.Relay,
		Rule:        s.Rule,
		Process:     s.Process,
		FakeIP:      s.FakeIP,
		Upload:      s.UploadBytes(),
		Download:    s.DownloadBytes(),
		Established: s.StartTime,
		ClosedAt:    s.EndTime,
		DurationMS:  end.Sub(s.StartTime).Milliseconds(),
	}
	if includeTrace {
		trace := s.Trace()
		resp.Trace = make([]sessionTraceResponse, 0, len(trace))
		for _, entry := range trace {
			resp.Trace = append(resp.Trace, sessionTraceResponse{
				At:       entry.At,
				OffsetMS: entry.At.Sub(s.StartTime).Milliseconds(),
				Message:  entry.Message,
			})
		}
	}
	return resp
}

func sessionDestination(s *session.Session) string {
	host := s.Domain
	if host == "" {
		host = s.DstIP
	}
	if host == "" {
		return ""
	}
	if s.DstPort == 0 {
		return host
	}
	return fmt.Sprintf("%s:%d", host, s.DstPort)
}
