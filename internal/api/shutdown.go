package api

import "net/http"

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.shutdown == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shutdown is not configured"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutting_down"})
	go s.shutdown()
}
