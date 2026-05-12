package api

import (
	"net/http"
	"time"
)

type fakeIPEntry struct {
	FakeIP     string    `json:"fake_ip"`
	Domain     string    `json:"domain"`
	State      string    `json:"state"`
	ExpiresAt  time.Time `json:"expires_at"`
	SessionIDs []string  `json:"session_ids,omitempty"`
}

func (s *Server) handleDNSFakeIPs(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil || s.dns.FakeIPPool() == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dns server unavailable"})
		return
	}
	mappings := s.dns.FakeIPPool().Snapshot()
	activeSessions := map[string][]string(nil)
	if s.sessions != nil {
		activeSessions = s.sessions.ActiveSessionIDsByFakeIP()
	}
	entries := make([]fakeIPEntry, 0, len(mappings))
	for _, mapping := range mappings {
		sessionIDs := mapping.SessionIDs
		if activeSessions != nil {
			sessionIDs = activeSessions[mapping.IP.String()]
		}
		state := "idle"
		if len(sessionIDs) > 0 {
			state = "active"
		}
		entries = append(entries, fakeIPEntry{
			FakeIP:     mapping.IP.String(),
			Domain:     mapping.Domain,
			State:      state,
			ExpiresAt:  mapping.ExpiresAt,
			SessionIDs: sessionIDs,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}
