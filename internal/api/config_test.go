package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
)

func TestConfigHandlersGetAndSetScalarValue(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	mgr := session.NewManager(eventbus.New(), 1000)
	s := &Server{store: st, sessions: mgr}

	rec := runRelayHandler(t, s.handleConfig, http.MethodGet, "/api/config?key=dns.cache_size", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entry configEntry
	if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if entry.Value != "100000" {
		t.Fatalf("dns.cache_size = %q, want default 100000", entry.Value)
	}

	rec = runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/dns.cache_size", map[string]string{
		"key": "dns.cache_size",
	}, configValueRequest{Value: "2000"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	got, err := config.Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.DNS.CacheSize != 2000 {
		t.Fatalf("stored dns.cache_size = %d, want 2000", got.DNS.CacheSize)
	}
}

func TestConfigHandlersApplyFullCheckInterval(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Snapshot()
	if err != nil {
		t.Fatalf("snapshot config: %v", err)
	}
	cfg.Relay.Groups = []config.RelayGroup{{
		Type:   "inline",
		Name:   "main",
		Select: "auto",
		Proxies: []map[string]any{{
			"name": "local",
			"type": "direct",
		}},
	}}
	if err := config.Replace(cfg); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	selector, err := relay.NewSelector(cfg.Relay, cfg.Check, nil, func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	s := &Server{store: st, selector: selector}

	rec := runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/check.full_interval", map[string]string{
		"key": "check.full_interval",
	}, configValueRequest{Value: "120"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, group := range selector.GroupList() {
		if group.Name == "main" && group.CheckInterval != 120 {
			t.Fatalf("group check interval = %d, want 120", group.CheckInterval)
		}
		found = found || group.Name == "main"
	}
	if !found {
		t.Fatalf("main group not found: %#v", selector.GroupList())
	}
}

func TestConfigHandlersApplySelectedCheckInterval(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Snapshot()
	if err != nil {
		t.Fatalf("snapshot config: %v", err)
	}
	cfg.Relay.Groups = []config.RelayGroup{{
		Type:   "inline",
		Name:   "main",
		Select: "auto",
		Proxies: []map[string]any{{
			"name": "local",
			"type": "direct",
		}},
	}}
	if err := config.Replace(cfg); err != nil {
		t.Fatalf("replace config: %v", err)
	}
	selector, err := relay.NewSelector(cfg.Relay, cfg.Check, nil, func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	s := &Server{store: st, selector: selector}

	rec := runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/check.interval", map[string]string{
		"key": "check.interval",
	}, configValueRequest{Value: "15"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, health := range selector.HealthList() {
		if health.Name == "main / local" {
			found = true
			if health.CheckInterval != 15 {
				t.Fatalf("selected relay check interval = %d, want 15", health.CheckInterval)
			}
		}
	}
	if !found {
		t.Fatalf("main / local relay not found: %#v", selector.HealthList())
	}
}

func TestConfigHandlersUseTopLevelDNSKeys(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := &Server{store: st}
	rec := runRelayHandler(t, s.handleConfig, http.MethodGet, "/api/config?key=dns.fakeip_ttl", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entry configEntry
	if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if entry.Value != "1h" {
		t.Fatalf("fakeip ttl = %q, want 1h", entry.Value)
	}

	rec = runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/dns.fakeip_ttl", map[string]string{
		"key": "dns.fakeip_ttl",
	}, configValueRequest{Value: "30m"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	got, err := config.Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.DNS.FakeIPTTL != "30m" {
		t.Fatalf("stored fakeip ttl = %q, want 30m", got.DNS.FakeIPTTL)
	}

	rec = runRelayHandler(t, s.handleConfig, http.MethodGet, "/api/config?key=dns.options.fake_ip_range", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy key status = %d, want 404", rec.Code)
	}
}

// A daemon started without a secret must still be able to close the hole at
// runtime: the routes are built once, so the middleware has to consult the
// live configuration rather than the value captured in NewServer.
func TestAPISecretSetAtRuntimeTakesEffectImmediately(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Started with no secret, as the vulnerable deployment would be.
	s := NewServer(config.API{}, st, nil, nil, session.NewManager(eventbus.New(), 10))

	guarded := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated request with no secret = %d, want 200", rec.Code)
	}

	if err := config.Set("api.secret", "s3cret"); err != nil {
		t.Fatalf("set api.secret: %v", err)
	}

	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request after setting the secret = %d, want 401", rec.Code)
	}

	for _, token := range []string{"s3cret", "Bearer s3cret"} {
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Header.Set("Authorization", token)
		guarded.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request with token %q = %d, want 200", token, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?token=s3cret", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("query-token request = %d, want 200", rec.Code)
	}

	// Clearing it again must also apply live.
	if err := config.Set("api.secret", ""); err != nil {
		t.Fatalf("clear api.secret: %v", err)
	}
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("request after clearing the secret = %d, want 200", rec.Code)
	}
}
