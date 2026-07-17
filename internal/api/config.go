package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/logging"
)

type configEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type configValueRequest struct {
	Value string `json:"value"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key != "" {
		value, err := config.Get(key)
		if err != nil {
			writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, configEntry{Key: key, Value: value})
		return
	}

	entries := make([]configEntry, 0, len(config.Keys()))
	for _, key := range config.Keys() {
		value, err := config.Get(key)
		if err != nil {
			writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
			return
		}
		entries = append(entries, configEntry{Key: key, Value: value})
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleSetConfigValue(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req configValueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid config value: " + err.Error()})
		return
	}
	if err := config.Set(key, req.Value); err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	if key == "system.log_level" {
		logging.SetLevel(req.Value)
	}
	if s.selector != nil && isLiveRelayConfigKey(key) {
		cfg, err := config.Snapshot()
		if err != nil {
			writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
			return
		}
		if err := s.selector.ApplyConfig(cfg.Relay, cfg.Check); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	value, err := config.Get(key)
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, configEntry{Key: key, Value: value})
}

func isLiveRelayConfigKey(key string) bool {
	return key == "relay.select" || strings.HasPrefix(key, "check.")
}

func configErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, config.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, config.ErrNotInitialized) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}
