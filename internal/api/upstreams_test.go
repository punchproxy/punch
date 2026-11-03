package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
)

func TestDNSUpstreamHandlersPreserveSettingsWhenToggling(t *testing.T) {
	for _, initiallyDisabled := range []bool{false, true} {
		name := "create enabled by default"
		if initiallyDisabled {
			name = "create disabled"
		}
		t.Run(name, func(t *testing.T) {
			st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer st.Close()
			if err := config.Init(st); err != nil {
				t.Fatalf("init config: %v", err)
			}
			cfg := config.Default()
			cfg.DNS.Upstream = nil
			if err := config.Replace(cfg); err != nil {
				t.Fatalf("replace config: %v", err)
			}
			dnsServer, err := pdns.NewServer(nil)
			if err != nil {
				t.Fatalf("NewServer() error = %v", err)
			}
			s := &Server{dns: dnsServer}
			const url = "https://192.0.2.1/dns-query"
			create := map[string]any{"url": url, "bootstrap": " 1.1.1.1 ", "domains": []string{" example.test "}}
			if initiallyDisabled {
				create["disabled"] = true
			}
			rec := runRelayHandler(t, s.handleCreateDNSUpstream, http.MethodPost, "/api/dns/upstreams", nil, create)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create status = %d body = %s", rec.Code, rec.Body.String())
			}
			want := config.Upstream{URL: url, Bootstrap: "1.1.1.1", Domains: []string{"domain:example.test"}, Disabled: initiallyDisabled}
			assertUpstream := func() {
				t.Helper()
				stored, err := config.Load(st)
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if len(stored.DNS.Upstream) != 1 || !reflect.DeepEqual(stored.DNS.Upstream[0], want) {
					t.Fatalf("stored upstreams = %+v, want %+v", stored.DNS.Upstream, want)
				}
				rec := runRelayHandler(t, s.handleDNSUpstreams, http.MethodGet, "/api/dns/upstreams", nil, nil)
				if rec.Code != http.StatusOK {
					t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
				}
				var stats []pdns.UpstreamStats
				if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
					t.Fatalf("decode stats: %v", err)
				}
				if len(stats) != 1 || stats[0].URL != want.URL || stats[0].Disabled != want.Disabled || stats[0].Bootstrap != want.Bootstrap || !reflect.DeepEqual(stats[0].Domains, want.Domains) {
					t.Fatalf("upstream stats = %+v, want settings %+v", stats, want)
				}
			}
			assertUpstream()

			updates := []struct {
				body     map[string]any
				disabled bool
			}{
				{body: map[string]any{"disabled": true}, disabled: true},
				{body: map[string]any{"bootstrap": "8.8.8.8"}, disabled: true},
				{body: map[string]any{"bootstrap": "", "domains": []string{}}, disabled: true},
				{body: map[string]any{"disabled": false}},
			}
			for _, update := range updates {
				update.body["url"] = url
				rec := runRelayHandler(t, s.handleUpdateDNSUpstream, http.MethodPut, "/api/dns/upstreams", nil, update.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("update %v status = %d body = %s", update.body, rec.Code, rec.Body.String())
				}
				want.Disabled = update.disabled
				if bootstrap, ok := update.body["bootstrap"].(string); ok {
					want.Bootstrap = bootstrap
				}
				if _, ok := update.body["domains"]; ok {
					want.Domains = nil
				}
				assertUpstream()
			}
			rec = runRelayHandler(t, s.handleUpdateDNSUpstream, http.MethodPut, "/api/dns/upstreams", nil, map[string]any{"url": "192.0.2.2", "disabled": true})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("missing upstream update status = %d body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
