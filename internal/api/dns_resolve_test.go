package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
)

func TestDNSResolveHandler(t *testing.T) {
	cfg := &config.Config{}
	cfg.DNS.CacheSize = 10
	cfg.DNS.FakeIPRange = "198.18.0.0/24"
	cfg.DNS.FakeIPTTL = "1h"
	cfg.DNS.Rules.Domains = []config.DomainRule{{Decision: config.DecisionReject, Source: "domain:ads.example"}}

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
		t.Fatalf("init config: %v", err)
	}
	if err := config.Replace(cfg); err != nil {
		t.Fatalf("replace config: %v", err)
	}

	dnsServer, err := pdns.NewServer(nil)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := dnsServer.LoadInitialRules(); err != nil {
		t.Fatalf("LoadInitialRules() error = %v", err)
	}

	s := &Server{dns: dnsServer}
	rec := runRelayHandler(t, s.handleDNSResolve, http.MethodGet, "/api/dns/resolve?domain=www.ads.example&type=A", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var got pdns.QueryResolution
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode resolve result: %v", err)
	}
	if got.Domain != "www.ads.example" || got.QType != "A" {
		t.Fatalf("query = %s/%s, want www.ads.example/A", got.Domain, got.QType)
	}
	if got.Decision != pdns.DecisionReject || got.Rule != "reject-domain" {
		t.Fatalf("decision/rule = %s/%s, want %s/reject-domain", got.Decision, got.Rule, pdns.DecisionReject)
	}
	if got.Response != "0.0.0.0" || got.RCode != "NOERROR" {
		t.Fatalf("response/rcode = %q/%q, want 0.0.0.0/NOERROR", got.Response, got.RCode)
	}
	if len(got.Answers) != 1 || got.Answers[0].Type != "A" || got.Answers[0].Value != "0.0.0.0" {
		t.Fatalf("answers = %#v, want A 0.0.0.0", got.Answers)
	}
}

func TestDNSResolveHandlerRequiresDomain(t *testing.T) {
	s := &Server{dns: &pdns.Server{}}
	rec := runRelayHandler(t, s.handleDNSResolve, http.MethodGet, "/api/dns/resolve", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want bad request", rec.Code)
	}
}
