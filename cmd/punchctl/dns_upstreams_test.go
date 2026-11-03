package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestDNSUpstreamsSetDisabledState(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		disabled bool
		clear    bool
	}{
		{name: "disable", args: []string{"--disabled"}, disabled: true},
		{name: "enable", args: []string{"--disabled=false"}},
		{name: "clear settings while disabled", args: []string{"--bootstrap", "", "--domains", ""}, disabled: true, clear: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var putBody createUpstreamRequest
			var putCalled bool
			const url = "https://192.0.2.1/dns-query"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/dns/upstreams" {
					t.Errorf("path = %q, want /api/dns/upstreams", r.URL.Path)
				}
				switch r.Method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode([]upstreamStats{{URL: url, Bootstrap: "1.1.1.1", Domains: []string{"domain:example.test"}, Disabled: !tt.disabled || tt.clear}})
				case http.MethodPut:
					putCalled = true
					if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
						t.Errorf("decode update: %v", err)
					}
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				default:
					t.Errorf("unexpected method: %s", r.Method)
				}
			}))
			defer server.Close()
			cmd := newRootCommand(commandConfig{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, client: server.Client()})
			cmd.SetArgs(append([]string{"--addr", server.URL, "dns", "upstreams", "set", url}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !putCalled || putBody.URL != url || putBody.Disabled != tt.disabled {
				t.Fatalf("update = %+v, PUT called = %v, want disabled %v", putBody, putCalled, tt.disabled)
			}
			if tt.clear {
				if putBody.Bootstrap != "" || putBody.Domains == nil || len(putBody.Domains) != 0 {
					t.Fatalf("cleared settings = %+v, want empty bootstrap and explicit empty domains", putBody)
				}
			} else if putBody.Bootstrap != "1.1.1.1" || !slices.Equal(putBody.Domains, []string{"domain:example.test"}) {
				t.Fatalf("toggle changed existing settings: %+v", putBody)
			}
		})
	}
}
