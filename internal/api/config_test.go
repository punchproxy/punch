package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
)

func TestConfigHandlersGetAndSetSessionHistoryLimit(t *testing.T) {
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

	rec := runRelayHandler(t, s.handleConfig, http.MethodGet, "/api/config?key=sessions.history_limit", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entry configEntry
	if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if entry.Value != "1000" {
		t.Fatalf("history limit = %q, want 1000", entry.Value)
	}

	rec = runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/sessions.history_limit", map[string]string{
		"key": "sessions.history_limit",
	}, configValueRequest{Value: "2000"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	if mgr.HistoryLimit() != 2000 {
		t.Fatalf("manager history limit = %d, want 2000", mgr.HistoryLimit())
	}
	got, err := config.Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.Sessions.HistoryLimit != 2000 {
		t.Fatalf("stored history limit = %d, want 2000", got.Sessions.HistoryLimit)
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
