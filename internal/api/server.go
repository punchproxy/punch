package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/dnsrule"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
	"github.com/punchproxy/punch/internal/tun"
)

type Server struct {
	httpServer *http.Server
	cfg        config.API
	store      *config.Store
	dns        *pdns.Server
	selector   *relay.Selector
	sessions   *session.Manager
	tun        *tun.Engine
	startedAt  time.Time
	version    string
}

func NewServer(cfg config.API, st *config.Store, dns *pdns.Server, selector *relay.Selector, sessions *session.Manager) *Server {
	return &Server{
		cfg:       cfg,
		store:     st,
		dns:       dns,
		selector:  selector,
		sessions:  sessions,
		startedAt: time.Now(),
		version:   "dev",
	}
}

func (s *Server) SetTUNEngine(engine *tun.Engine) {
	s.tun = engine
}

func (s *Server) SetVersion(version string) {
	if version == "" {
		return
	}
	s.version = version
}

func (s *Server) Start() error {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware)
	if s.cfg.Secret != "" {
		r.Use(s.authMiddleware)
	}

	r.Route("/api", func(r chi.Router) {
		r.Get("/status", s.handleStatus)
		r.Get("/system", s.handleSystem)
		r.Get("/system/routes", s.handleSystemRoutes)
		r.Get("/system/routes/get", s.handleGetSystemRoute)
		r.Post("/system/routes", s.handleCreateSystemRoute)
		r.Post("/system/routes/refresh", s.handleRefreshSystemRoute)
		r.Delete("/system/routes", s.handleDeleteSystemRoute)
		r.Get("/config", s.handleConfig)
		r.Put("/config/{key}", s.handleSetConfigValue)
		r.Get("/dns/queries/stream", s.handleDNSQueryStream)
		r.Get("/dns/upstreams", s.handleDNSUpstreams)
		r.Post("/dns/upstreams", s.handleCreateDNSUpstream)
		r.Put("/dns/upstreams", s.handleUpdateDNSUpstream)
		r.Delete("/dns/upstreams", s.handleDeleteDNSUpstream)
		r.Get("/dns/rules", s.handleDNSRules)
		r.Post("/dns/rules", s.handleCreateDNSRule)
		r.Put("/dns/rules", s.handleUpdateDNSRule)
		r.Delete("/dns/rules", s.handleDeleteDNSRule)
		r.Post("/dns/rules/move", s.handleMoveDNSRule)
		r.Post("/dns/rules/refresh", s.handleRefreshDNSRule)
		r.Get("/dns/routes", s.handleDNSRoutes)
		r.Post("/dns/routes", s.handleCreateDNSRoute)
		r.Put("/dns/routes", s.handleUpdateDNSRoute)
		r.Delete("/dns/routes", s.handleDeleteDNSRoute)
		r.Post("/dns/routes/move", s.handleMoveDNSRoute)
		r.Post("/dns/routes/refresh", s.handleRefreshDNSRoute)
		r.Get("/dns/cache", s.handleDNSCache)
		r.Delete("/dns/cache", s.handleFlushCache)
		r.Get("/dns/fakeips", s.handleDNSFakeIPs)
		r.Get("/relaygroups", s.handleRelayGroups)
		r.Post("/relaygroups", s.handleCreateRelayGroup)
		r.Post("/relaygroups/check", s.handleCheckRelayGroups)
		r.Post("/relaygroups/refresh", s.handleRefreshRelayGroups)
		r.Get("/relaygroups/{group}", s.handleRelayGroup)
		r.Put("/relaygroups/{group}", s.handleUpdateRelayGroup)
		r.Delete("/relaygroups/{group}", s.handleDeleteRelayGroup)
		r.Post("/relaygroups/{group}/select", s.handleSelectRelayGroup)
		r.Post("/relaygroups/{group}/check", s.handleCheckRelayGroup)
		r.Post("/relaygroups/{group}/refresh", s.handleRefreshRelayGroup)
		r.Get("/relays", s.handleRelays)
		r.Post("/relays/{relay}/select", s.handleSelectRelay)
		r.Post("/relays/{relay}/check", s.handleCheckRelay)
		r.Get("/relaygroups/{group}/relays/{relay}", s.handleRelay)
		r.Post("/relaygroups/{group}/relays", s.handleCreateRelays)
		r.Put("/relaygroups/{group}/relays/{relay}", s.handleUpdateRelay)
		r.Delete("/relaygroups/{group}/relays/{relay}", s.handleDeleteRelay)
		r.Get("/sessions", s.handleSessions)
		r.Get("/sessions/{id}", s.handleSession)
		r.Delete("/sessions", s.handleTerminateSessions)
		r.Delete("/sessions/{id}", s.handleTerminateSession)
	})

	s.httpServer = &http.Server{
		Addr:    s.cfg.Listen,
		Handler: r,
	}

	go func() {
		slog.Info("API started", "listen", s.cfg.Listen)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) Stop() error {
	if s.httpServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleDNSQueryStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan pdns.QueryLog, 64)
	unsubscribe := s.dns.SubscribeQueryLogs(ch)
	defer unsubscribe()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ql := <-ch:
			if err := enc.Encode(ql); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleDNSUpstreams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.dns.UpstreamStats())
}

type upstreamRequest struct {
	URL       string   `json:"url"`
	Bootstrap string   `json:"bootstrap,omitempty"`
	Domains   []string `json:"domains,omitempty"`
}

func (s *Server) handleCreateDNSUpstream(w http.ResponseWriter, r *http.Request) {
	var req upstreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upstream: " + err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing url"})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, upstream := range cfg.DNS.Upstream {
		if upstream.URL == req.URL {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "upstream already exists"})
			return
		}
	}

	cfg.DNS.Upstream = append(cfg.DNS.Upstream, config.Upstream{
		URL:       req.URL,
		Bootstrap: strings.TrimSpace(req.Bootstrap),
		Domains:   normalizeUpstreamDomains(req.Domains),
	})
	if err := config.Replace(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.dns.UpdateUpstreams(cfg.DNS.Upstream)
	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "ok",
		"url":    req.URL,
	})
}

func (s *Server) handleUpdateDNSUpstream(w http.ResponseWriter, r *http.Request) {
	var req upstreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upstream: " + err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing url"})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	updated := false
	for i, upstream := range cfg.DNS.Upstream {
		if upstream.URL != req.URL {
			continue
		}
		cfg.DNS.Upstream[i] = config.Upstream{
			URL:       req.URL,
			Bootstrap: strings.TrimSpace(req.Bootstrap),
			Domains:   normalizeUpstreamDomains(req.Domains),
		}
		updated = true
		break
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "upstream not found"})
		return
	}

	if err := config.Replace(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.dns.UpdateUpstreams(cfg.DNS.Upstream)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"url":    req.URL,
	})
}

func (s *Server) handleDeleteDNSUpstream(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("url"))
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing url"})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	next := cfg.DNS.Upstream[:0]
	removed := false
	for _, upstream := range cfg.DNS.Upstream {
		if upstream.URL == target {
			removed = true
			continue
		}
		next = append(next, upstream)
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "upstream not found"})
		return
	}

	cfg.DNS.Upstream = next
	if err := config.Replace(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.dns.UpdateUpstreams(cfg.DNS.Upstream)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"url":    target,
	})
}

func normalizeUpstreamDomains(domains []string) []string {
	result := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		result = append(result, dnsrule.Normalize(domain))
	}
	return result
}

func (s *Server) handleDNSCache(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.dns.Cache().Snapshot())
}

func (s *Server) handleFlushCache(w http.ResponseWriter, r *http.Request) {
	s.dns.FlushCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		expected := "Bearer " + s.cfg.Secret
		if token != expected && token != s.cfg.Secret {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
