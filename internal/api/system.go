package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/punchproxy/punch/internal/config"
	src "github.com/punchproxy/punch/internal/source"
)

var (
	errSystemRouteDuplicate = errors.New("system route already exists")
	errSystemRouteNotFound  = errors.New("system route not found")
)

type systemRouteEntry struct {
	Index       int       `json:"index"`
	Route       string    `json:"route"`
	LastUpdated time.Time `json:"last_updated,omitempty"`
	NextUpdate  time.Time `json:"next_update,omitempty"`
}

type systemRouteDetail struct {
	systemRouteEntry
	Type     string   `json:"type"`
	Prefixes []string `json:"prefixes"`
	Applied  bool     `json:"applied"`
	Error    string   `json:"error,omitempty"`
}

type systemRouteRequest struct {
	Route string `json:"route"`
	Index *int   `json:"index,omitempty"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if s.tun == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"tun_interface_name":     "",
			"tun_address":            "",
			"tun_ipv6_address":       "",
			"extra_tun_routes_count": 0,
			"system_dns":             []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.tun.SystemInfo())
}

func (s *Server) handleSystemRoutes(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.systemRouteEntries(cfg.TUN.Routes, systemRouteRefreshInterval(s, cfg)))
}

func (s *Server) handleGetSystemRoute(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("route"))
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing route"})
		return
	}
	route, err := normalizeSystemRoute(target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	idx := systemRouteIndex(cfg.TUN.Routes, route)
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errSystemRouteNotFound.Error()})
		return
	}

	detail := systemRouteDetail{
		systemRouteEntry: systemRouteEntry{Index: idx, Route: route},
		Type:             "cidr",
		Prefixes:         []string{},
	}
	if isRemoteSource(route) || isSystemRouteSource(route) {
		detail.Type = "source"
	}
	if isRemoteSource(route) {
		detail.LastUpdated = s.systemRouteLastUpdated(route)
		if interval := systemRouteRefreshInterval(s, cfg); interval > 0 && !detail.LastUpdated.IsZero() {
			detail.NextUpdate = detail.LastUpdated.Add(interval)
		}
	}
	if s.tun != nil {
		resolution := s.tun.ResolveRoute(route)
		if resolution.Err != nil {
			detail.Error = resolution.Err.Error()
		}
		prefixes := make([]string, 0, len(resolution.Prefixes))
		for _, p := range resolution.Prefixes {
			prefixes = append(prefixes, p.String())
		}
		detail.Prefixes = prefixes
		detail.Applied = resolution.Applied
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleCreateSystemRoute(w http.ResponseWriter, r *http.Request) {
	var req systemRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid route: " + err.Error()})
		return
	}
	route, err := normalizeSystemRoute(req.Route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cfg, err := config.Update(func(cfg *config.Config) error {
		if systemRouteIndex(cfg.TUN.Routes, route) >= 0 {
			return errSystemRouteDuplicate
		}
		index := len(cfg.TUN.Routes)
		if req.Index != nil {
			index = *req.Index
			if index < 0 || index > len(cfg.TUN.Routes) {
				return fmt.Errorf("index %d out of range", index)
			}
		}
		cfg.TUN.Routes = append(cfg.TUN.Routes, "")
		copy(cfg.TUN.Routes[index+1:], cfg.TUN.Routes[index:])
		cfg.TUN.Routes[index] = route
		return nil
	})
	if errors.Is(err, errSystemRouteDuplicate) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	if !s.applyTUNConfig(w, cfg) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "route": route})
}

func (s *Server) handleDeleteSystemRoute(w http.ResponseWriter, r *http.Request) {
	indexValue := strings.TrimSpace(r.URL.Query().Get("index"))
	routeValue := strings.TrimSpace(r.URL.Query().Get("route"))
	if indexValue == "" && routeValue == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing index or route"})
		return
	}

	var route string
	var index int
	var byIndex bool
	if indexValue != "" {
		parsed, err := strconv.Atoi(indexValue)
		if err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid index %q", indexValue)})
			return
		}
		index = parsed
		byIndex = true
	} else {
		var err error
		route, err = normalizeSystemRoute(routeValue)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	cfg, err := config.Update(func(cfg *config.Config) error {
		removeAt := index
		if byIndex {
			if removeAt < 0 || removeAt >= len(cfg.TUN.Routes) {
				return errSystemRouteNotFound
			}
		} else {
			removeAt = systemRouteIndex(cfg.TUN.Routes, route)
			if removeAt < 0 {
				return errSystemRouteNotFound
			}
		}
		cfg.TUN.Routes = append(cfg.TUN.Routes[:removeAt], cfg.TUN.Routes[removeAt+1:]...)
		return nil
	})
	if errors.Is(err, errSystemRouteNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	if !s.applyTUNConfig(w, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleRefreshSystemRoute(w http.ResponseWriter, r *http.Request) {
	var req systemRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid route: " + err.Error()})
		return
	}
	route, err := normalizeSystemRoute(req.Route)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !isRemoteSource(route) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "route has no remote source to refresh"})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, configErrorStatus(err), map[string]string{"error": err.Error()})
		return
	}
	if systemRouteIndex(cfg.TUN.Routes, route) < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errSystemRouteNotFound.Error()})
		return
	}
	if s.dns == nil || s.dns.Assets() == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "asset manager unavailable"})
		return
	}
	if err := s.dns.Assets().Refresh(route, false); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if !s.applyTUNConfig(w, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "route": route})
}

func (s *Server) applyTUNConfig(w http.ResponseWriter, cfg *config.Config) bool {
	if s.tun == nil {
		return true
	}
	if err := s.tun.ApplyConfig(cfg.TUN); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func (s *Server) systemRouteEntries(routes []string, refreshInterval time.Duration) []systemRouteEntry {
	entries := make([]systemRouteEntry, 0, len(routes))
	for i, route := range routes {
		entry := systemRouteEntry{Index: i, Route: route}
		if isRemoteSource(route) {
			entry.LastUpdated = s.systemRouteLastUpdated(route)
			if refreshInterval > 0 && !entry.LastUpdated.IsZero() {
				entry.NextUpdate = entry.LastUpdated.Add(refreshInterval)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func (s *Server) systemRouteLastUpdated(route string) time.Time {
	if s.dns != nil && s.dns.Assets() != nil {
		if status, ok := s.dns.Assets().Status(route); ok {
			return status.LastUpdated
		}
	}
	if s.store != nil {
		if asset, err := s.store.GetAsset(route); err == nil && asset != nil {
			return asset.UpdatedAt
		}
	}
	return time.Time{}
}

func systemRouteRefreshInterval(s *Server, cfg *config.Config) time.Duration {
	if s != nil && s.dns != nil && s.dns.Assets() != nil {
		return s.dns.Assets().RefreshInterval()
	}
	if cfg == nil || cfg.AssetRefreshInterval <= 0 {
		return 0
	}
	return time.Duration(cfg.AssetRefreshInterval) * time.Second
}

func systemRouteIndex(routes []string, route string) int {
	for i, existing := range routes {
		if normalized, err := normalizeSystemRoute(existing); err == nil {
			existing = normalized
		}
		if existing == route {
			return i
		}
	}
	return -1
}

func normalizeSystemRoute(route string) (string, error) {
	route = strings.TrimSpace(route)
	if route == "" {
		return "", fmt.Errorf("missing route")
	}
	if isSystemRouteSource(route) {
		return route, nil
	}
	prefix, err := netip.ParsePrefix(route)
	if err != nil {
		return "", fmt.Errorf("invalid route %q: expected CIDR or URL/file source", route)
	}
	return prefix.Masked().String(), nil
}

func isSystemRouteSource(route string) bool {
	return src.IsSource(route)
}
