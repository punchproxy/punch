package api

import "net/http"

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if s.tun == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"tun_interface_name": "",
			"tun_address":        "",
			"extra_tun_routes":   []string{},
			"system_dns":         []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.tun.SystemInfo())
}
