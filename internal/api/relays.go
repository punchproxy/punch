package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/relay"
)

type relayGroupResponse struct {
	relay.GroupStatus
	Config config.RelayGroup `json:"config"`
}

type relayResponse struct {
	relay.RelayHealth
	Spec map[string]any `json:"spec,omitempty"`
}

type relaysRequest struct {
	Relays []map[string]any `json:"relays"`
}

func (s *Server) handleRelayGroups(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, relayGroupResponses(s.selector.GroupList(), cfg.Relay.Groups))
}

func (s *Server) handleRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	groups := s.selector.GroupList()
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, group := range relayGroupResponses(groups, cfg.Relay.Groups) {
		if group.Name == name {
			writeJSON(w, http.StatusOK, group)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
}

func (s *Server) handleCreateRelayGroup(w http.ResponseWriter, r *http.Request) {
	var req config.RelayGroup
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid relay group: " + err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing name"})
		return
	}
	if req.Type == "" {
		req.Type = "inline"
	}
	if err := validateRelayGroup(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if relayGroupIndex(cfg.Relay.Groups, req.Name) >= 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "relay group already exists"})
		return
	}
	cfg.Relay.Groups = append(cfg.Relay.Groups, req)
	if !saveRelayConfig(w, s, cfg, req.Name) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "name": req.Name})
}

func (s *Server) handleUpdateRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	var req config.RelayGroup
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid relay group: " + err.Error()})
		return
	}
	if req.Name == "" {
		req.Name = name
	}
	if req.Name != name {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relay group name cannot be changed"})
		return
	}
	if req.Type == "" {
		req.Type = "inline"
	}
	if err := validateRelayGroup(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	idx := relayGroupIndex(cfg.Relay.Groups, name)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
		return
	}
	cfg.Relay.Groups[idx] = req
	if !saveRelayConfig(w, s, cfg, name) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

func (s *Server) handleDeleteRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	idx := relayGroupIndex(cfg.Relay.Groups, name)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
		return
	}
	cfg.Relay.Groups = append(cfg.Relay.Groups[:idx], cfg.Relay.Groups[idx+1:]...)
	if !saveRelayConfig(w, s, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": name})
}

func (s *Server) handleSelectRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	selected, err := s.selector.SelectManualGroup(name)
	if err != nil {
		if errors.Is(err, relay.ErrGroupSelectionAutoMode) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "group": selected})
}

func (s *Server) handleRefreshRelayGroups(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") != "true" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing all=true"})
		return
	}
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	refreshed := 0
	for _, group := range cfg.Relay.Groups {
		if group.Type != "remote" {
			continue
		}
		if err := s.selector.RefreshGroup(group.Name); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		refreshed++
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "refreshed": refreshed})
}

func (s *Server) handleRefreshRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	if err := s.selector.RefreshGroup(name); err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "not remote") {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "group": name})
}

func (s *Server) handleCheckRelayGroups(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") != "true" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing all=true"})
		return
	}
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	go s.selector.Benchmark()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started"})
}

func (s *Server) handleCheckRelayGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "group")
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	if !s.selector.HasBenchmarkTarget(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("relay %q not found", name)})
		return
	}
	go func() {
		if err := s.selector.BenchmarkTarget(name); err != nil {
			slog.Warn("relay benchmark failed", "target", name, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "group": name})
}

func (s *Server) handleRelays(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, relayResponses(s.selector.HealthList(), cfg.Relay.Groups))
}

func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	groupName := chi.URLParam(r, "group")
	relayName := chi.URLParam(r, "relay")
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, item := range relayResponses(s.selector.HealthList(), cfg.Relay.Groups) {
		if item.Group == groupName && item.RelayHealth.Name == s.displayRelayName(groupName, relayName) {
			writeJSON(w, http.StatusOK, item)
			return
		}
		if item.Group == groupName && relayShortName(item.RelayHealth.Name, groupName) == relayName {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay not found"})
}

func (s *Server) handleCreateRelays(w http.ResponseWriter, r *http.Request) {
	groupName := chi.URLParam(r, "group")
	var req relaysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid relays: " + err.Error()})
		return
	}
	if len(req.Relays) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing relays"})
		return
	}
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	group, idx, ok := relayGroupByName(cfg.Relay.Groups, groupName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
		return
	}
	if group.Type != "inline" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relays can only be changed inside inline relay groups"})
		return
	}
	existing := make(map[string]struct{}, len(group.Proxies))
	for _, proxy := range group.Proxies {
		if name, _ := proxy["name"].(string); name != "" {
			existing[name] = struct{}{}
		}
	}
	for _, proxy := range req.Relays {
		name, _ := proxy["name"].(string)
		if strings.TrimSpace(name) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relay missing name"})
			return
		}
		if _, ok := existing[name]; ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("relay %q already exists", name)})
			return
		}
		existing[name] = struct{}{}
		group.Proxies = append(group.Proxies, proxy)
	}
	cfg.Relay.Groups[idx] = group
	checkTargets := make([]string, 0, len(req.Relays))
	for _, proxy := range req.Relays {
		name, _ := proxy["name"].(string)
		checkTargets = append(checkTargets, s.displayRelayName(groupName, name))
	}
	if !saveRelayConfig(w, s, cfg, checkTargets...) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "group": groupName, "created": len(req.Relays)})
}

func (s *Server) handleUpdateRelay(w http.ResponseWriter, r *http.Request) {
	groupName := chi.URLParam(r, "group")
	relayName := chi.URLParam(r, "relay")
	var relaySpec map[string]any
	if err := json.NewDecoder(r.Body).Decode(&relaySpec); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid relay: " + err.Error()})
		return
	}
	name, _ := relaySpec["name"].(string)
	if strings.TrimSpace(name) == "" {
		relaySpec["name"] = relayName
	} else if name != relayName {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relay name cannot be changed"})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	group, groupIdx, ok := relayGroupByName(cfg.Relay.Groups, groupName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
		return
	}
	if group.Type != "inline" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relays can only be changed inside inline relay groups"})
		return
	}
	relayIdx := proxyIndex(group.Proxies, relayName)
	if relayIdx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay not found"})
		return
	}
	group.Proxies[relayIdx] = relaySpec
	cfg.Relay.Groups[groupIdx] = group
	if !saveRelayConfig(w, s, cfg, s.displayRelayName(groupName, relayName)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "group": groupName, "relay": relayName})
}

func (s *Server) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	groupName := chi.URLParam(r, "group")
	relayName := chi.URLParam(r, "relay")
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	group, groupIdx, ok := relayGroupByName(cfg.Relay.Groups, groupName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay group not found"})
		return
	}
	if group.Type != "inline" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relays can only be changed inside inline relay groups"})
		return
	}
	relayIdx := proxyIndex(group.Proxies, relayName)
	if relayIdx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "relay not found"})
		return
	}
	group.Proxies = append(group.Proxies[:relayIdx], group.Proxies[relayIdx+1:]...)
	cfg.Relay.Groups[groupIdx] = group
	if !saveRelayConfig(w, s, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "group": groupName, "relay": relayName})
}

func (s *Server) handleSelectRelay(w http.ResponseWriter, r *http.Request) {
	relayName := chi.URLParam(r, "relay")
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	if groupName := r.URL.Query().Get("group"); groupName != "" {
		relayName = s.displayRelayName(groupName, relayName)
	}
	selected, err := s.selector.SelectManualRelay(relayName)
	if err != nil {
		switch {
		case errors.Is(err, relay.ErrRelaySelectionAutoGroup):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, relay.ErrRelaySelectionAmbiguous):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "relay": selected})
}

func (s *Server) handleCheckRelay(w http.ResponseWriter, r *http.Request) {
	relayName := chi.URLParam(r, "relay")
	if s.selector == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "relay selector unavailable"})
		return
	}
	checked, err := s.selector.BenchmarkRelayAsync(relayName, r.URL.Query().Get("group"))
	if err != nil {
		switch {
		case errors.Is(err, relay.ErrRelaySelectionAmbiguous):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "relay": checked})
}

func saveRelayConfig(w http.ResponseWriter, s *Server, cfg *config.Config, checkGroups ...string) bool {
	if err := config.Replace(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	if s.selector != nil {
		if err := s.selector.ApplyConfig(cfg.Relay); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return false
		}
		for _, name := range checkGroups {
			go func(groupName string) {
				if err := s.selector.BenchmarkTarget(groupName); err != nil {
					slog.Warn("relay group health check after config change failed", "group", groupName, "error", err)
				}
			}(name)
		}
	}
	return true
}

func validateRelayGroup(group config.RelayGroup) error {
	switch group.Type {
	case "inline":
	case "remote":
		if strings.TrimSpace(group.URL) == "" {
			return fmt.Errorf("remote relay group requires url")
		}
	default:
		return fmt.Errorf("unsupported relay group type %q", group.Type)
	}
	if group.Keep != "" {
		if _, err := regexp.Compile(group.Keep); err != nil {
			return fmt.Errorf("invalid keep regex: %w", err)
		}
	}
	if group.Remove != "" {
		if _, err := regexp.Compile(group.Remove); err != nil {
			return fmt.Errorf("invalid remove regex: %w", err)
		}
	}
	return nil
}

func relayGroupResponses(statuses []relay.GroupStatus, cfgs []config.RelayGroup) []relayGroupResponse {
	byName := make(map[string]config.RelayGroup, len(cfgs))
	for _, cfg := range cfgs {
		byName[cfg.Name] = cfg
	}
	responses := make([]relayGroupResponse, 0, len(statuses))
	for _, status := range statuses {
		cfg := byName[status.Name]
		if len(status.RelayDomainResolver) > 0 {
			cfg.RelayDomainResolver = status.RelayDomainResolver
		}
		responses = append(responses, relayGroupResponse{
			GroupStatus: status,
			Config:      cfg,
		})
	}
	return responses
}

func relayResponses(health []relay.RelayHealth, groups []config.RelayGroup) []relayResponse {
	specs := make(map[string]map[string]any)
	for _, group := range groups {
		for _, proxy := range group.Proxies {
			name, _ := proxy["name"].(string)
			if name != "" {
				specs[group.Name+"\x00"+name] = proxy
			}
		}
	}
	responses := make([]relayResponse, 0, len(health))
	for _, h := range health {
		shortName := relayShortName(h.Name, h.Group)
		spec := h.Spec
		if len(spec) == 0 {
			spec = specs[h.Group+"\x00"+shortName]
		}
		responses = append(responses, relayResponse{
			RelayHealth: h,
			Spec:        spec,
		})
	}
	return responses
}

func relayGroupIndex(groups []config.RelayGroup, name string) int {
	for i, group := range groups {
		if group.Name == name {
			return i
		}
	}
	return -1
}

func relayGroupByName(groups []config.RelayGroup, name string) (config.RelayGroup, int, bool) {
	idx := relayGroupIndex(groups, name)
	if idx < 0 {
		return config.RelayGroup{}, -1, false
	}
	return groups[idx], idx, true
}

func proxyIndex(proxies []map[string]any, name string) int {
	for i, proxy := range proxies {
		if proxyName, _ := proxy["name"].(string); proxyName == name {
			return i
		}
	}
	return -1
}

func relayShortName(name, group string) string {
	prefix := group + " / "
	return strings.TrimPrefix(name, prefix)
}

func (s *Server) displayRelayName(group, relayName string) string {
	return group + " / " + relayName
}
