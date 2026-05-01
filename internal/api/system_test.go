package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
)

func TestSystemRouteHandlersCreateListDelete(t *testing.T) {
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
	rec := runRelayHandler(t, s.handleCreateSystemRoute, http.MethodPost, "/api/system/routes", nil, systemRouteRequest{Route: "1.1.1.1/24"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", rec.Code, rec.Body.String())
	}
	got, err := config.Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(got.TUN.Routes) != 1 || got.TUN.Routes[0] != "1.1.1.0/24" {
		t.Fatalf("stored routes = %#v, want normalized route", got.TUN.Routes)
	}

	rec = runRelayHandler(t, s.handleSystemRoutes, http.MethodGet, "/api/system/routes", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entries []systemRouteEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode routes: %v", err)
	}
	if len(entries) != 1 || entries[0].Index != 0 || entries[0].Route != "1.1.1.0/24" {
		t.Fatalf("entries = %#v", entries)
	}

	rec = runRelayHandler(t, s.handleDeleteSystemRoute, http.MethodDelete, "/api/system/routes?route=1.1.1.1/24", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", rec.Code, rec.Body.String())
	}
	got, err = config.Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(got.TUN.Routes) != 0 {
		t.Fatalf("stored routes after delete = %#v, want empty", got.TUN.Routes)
	}
}

func TestSystemRouteHandlersPersistSourceRoutes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "punch.db")
	st, err := config.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := config.Init(st); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	source := "https://core.telegram.org/resources/cidr.txt"
	s := &Server{store: st}
	rec := runRelayHandler(t, s.handleCreateSystemRoute, http.MethodPost, "/api/system/routes", nil, systemRouteRequest{Route: source})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := config.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("close reopened store: %v", err)
		}
	})
	got, err := config.Load(reopened)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if len(got.TUN.Routes) != 1 || got.TUN.Routes[0] != source {
		t.Fatalf("persisted routes = %#v, want %q", got.TUN.Routes, source)
	}
}

func TestSystemRouteHandlersListSourceRefreshTimes(t *testing.T) {
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
	source := "https://core.telegram.org/resources/cidr.txt"
	if _, err := config.Update(func(cfg *config.Config) error {
		cfg.AssetRefreshInterval = 3600
		cfg.TUN.Routes = []string{source}
		return nil
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	updatedAt := time.Now().UTC().Truncate(time.Second)
	if err := st.PutAsset(source, []byte("1.1.1.0/24"), updatedAt); err != nil {
		t.Fatalf("put asset: %v", err)
	}
	asset, err := st.GetAsset(source)
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	updatedAt = asset.UpdatedAt

	s := &Server{store: st}
	rec := runRelayHandler(t, s.handleSystemRoutes, http.MethodGet, "/api/system/routes", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entries []systemRouteEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode routes: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if !entries[0].LastUpdated.Equal(updatedAt) {
		t.Fatalf("last updated = %s, want %s", entries[0].LastUpdated, updatedAt)
	}
	if want := updatedAt.Add(time.Hour); !entries[0].NextUpdate.Equal(want) {
		t.Fatalf("next update = %s, want %s", entries[0].NextUpdate, want)
	}
}

func TestSystemRouteHandlersRefreshSource(t *testing.T) {
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
	var originHits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&originHits, 1)
		_, _ = w.Write([]byte("1.1.1.0/24\n"))
	}))
	defer origin.Close()

	if _, err := config.Update(func(cfg *config.Config) error {
		cfg.TUN.Routes = []string{origin.URL}
		return nil
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	manager, err := assets.New(st, time.Hour, nil, nil)
	if err != nil {
		t.Fatalf("assets.New() error = %v", err)
	}
	dnsServer, err := pdns.NewServer(manager)
	if err != nil {
		t.Fatalf("dns.NewServer() error = %v", err)
	}

	s := &Server{store: st, dns: dnsServer}
	rec := runRelayHandler(t, s.handleRefreshSystemRoute, http.MethodPost, "/api/system/routes/refresh", nil, systemRouteRequest{Route: origin.URL})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body = %s", rec.Code, rec.Body.String())
	}
	if hits := atomic.LoadInt32(&originHits); hits != 1 {
		t.Fatalf("origin hits = %d, want 1", hits)
	}
	asset, err := st.GetAsset(origin.URL)
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	if strings.TrimSpace(string(asset.Content)) != "1.1.1.0/24" {
		t.Fatalf("asset content = %q", string(asset.Content))
	}
}
