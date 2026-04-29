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
	entries := make([]fakeIPEntry, 0, len(mappings))
	for _, mapping := range mappings {
		state := "idle"
		if mapping.Active() {
			state = "active"
		}
		entries = append(entries, fakeIPEntry{
			FakeIP:     mapping.IP.String(),
			Domain:     mapping.Domain,
			State:      state,
			ExpiresAt:  mapping.ExpiresAt,
			SessionIDs: mapping.SessionIDs,
		})
	}
	writeJSON(w, http.StatusOK, entries)
}
