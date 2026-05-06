package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/relay"
)

func TestRelayMutationsPersistToStore(t *testing.T) {
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
	createGroup := config.RelayGroup{
		Type:   "inline",
		Name:   "main",
		Select: "manual",
		Proxies: []map[string]any{
			{"name": "hk-1", "type": "direct"},
		},
	}
	rec := runRelayHandler(t, s.handleCreateRelayGroup, http.MethodPost, "/api/relaygroups", nil, createGroup)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = runRelayHandler(t, s.handleUpdateRelay, http.MethodPut, "/api/relaygroups/main/relays/hk-1", map[string]string{
		"group": "main",
		"relay": "hk-1",
	}, map[string]any{"name": "hk-1", "type": "ss", "server": "relay.example", "port": 443})
	if rec.Code != http.StatusOK {
		t.Fatalf("update relay status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = runRelayHandler(t, s.handleCreateRelays, http.MethodPost, "/api/relaygroups/main/relays", map[string]string{
		"group": "main",
	}, relaysRequest{Relays: []map[string]any{{"name": "jp-1", "type": "direct"}}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create relay status = %d body = %s", rec.Code, rec.Body.String())
	}

	got, err := config.Load(st)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	group, _, ok := relayGroupByName(got.Relay.Groups, "main")
	if !ok {
		t.Fatalf("persisted group not found: %#v", got.Relay.Groups)
	}
	if len(group.Proxies) != 2 {
		t.Fatalf("persisted proxy count = %d, want 2: %#v", len(group.Proxies), group.Proxies)
	}
	if group.Proxies[0]["type"] != "ss" || group.Proxies[0]["server"] != "relay.example" {
		t.Fatalf("updated relay was not persisted: %#v", group.Proxies[0])
	}
	if group.Proxies[1]["name"] != "jp-1" {
		t.Fatalf("created relay was not persisted: %#v", group.Proxies[1])
	}
}

func TestRelayGroupCreateSchedulesHealthCheck(t *testing.T) {
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
		t.Fatalf("get config: %v", err)
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	cfg.Check.OutsideURL = target.URL
	if cfg.Check.FullInterval == 0 {
		cfg.Check.FullInterval = 300
	}
	if cfg.Check.Tolerance == 0 {
		cfg.Check.Tolerance = 50
	}
	if err := config.Replace(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	selector, err := relay.NewSelector(cfg.Relay, cfg.Check, nil, func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	t.Cleanup(selector.Stop)

	s := &Server{store: st, selector: selector}
	group := config.RelayGroup{
		Type:   "inline",
		Name:   "main",
		Select: "manual",
		Proxies: []map[string]any{
			{"name": "local", "type": "direct"},
		},
	}
	rec := runRelayHandler(t, s.handleCreateRelayGroup, http.MethodPost, "/api/relaygroups", nil, group)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group status = %d body = %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, h := range selector.HealthList() {
			if h.Group == "main" && h.Status == relay.HealthHealthy && !h.LastCheckedAt.IsZero() {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("created relay group was not health checked: %#v", selector.HealthList())
}

func TestRelayResponsesUseRuntimeSpec(t *testing.T) {
	responses := relayResponses([]relay.RelayHealth{{
		Name:  "guguairport / MKCLOUD-SH-JP-IX-XTLS",
		Group: "guguairport",
		Type:  "vless",
		Spec: map[string]any{
			"name":   "MKCLOUD-SH-JP-IX-XTLS",
			"type":   "vless",
			"server": "relay.example",
		},
	}}, []config.RelayGroup{{
		Type: "remote",
		Name: "guguairport",
		URL:  "https://example.test/provider.yaml",
	}})

	if len(responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(responses))
	}
	if responses[0].Spec["server"] != "relay.example" {
		t.Fatalf("spec = %#v", responses[0].Spec)
	}
}

func runRelayHandler(t *testing.T, handler http.HandlerFunc, method, target string, params map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	if params != nil {
		routeCtx := chi.NewRouteContext()
		for key, value := range params {
			routeCtx.URLParams.Add(key, value)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}
