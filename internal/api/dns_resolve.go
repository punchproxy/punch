package api

import (
	"net/http"
	"strings"

	"github.com/punchproxy/punch/internal/dnsrule"
)

func (s *Server) handleDNSResolve(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dns server unavailable"})
		return
	}

	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing domain"})
		return
	}

	qtypeText := strings.TrimSpace(r.URL.Query().Get("type"))
	if qtypeText == "" {
		qtypeText = strings.TrimSpace(r.URL.Query().Get("qtype"))
	}
	if qtypeText == "" {
		qtypeText = "A"
	}
	qtype, err := dnsrule.ParseQType(qtypeText)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	result, err := s.dns.ResolveQuery(r.Context(), domain, qtype)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
