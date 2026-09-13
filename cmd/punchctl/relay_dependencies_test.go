package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelaySelectWithoutActivation(t *testing.T) {
	var selected bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]relayHealth{{Name: "transit / two", Group: "transit"}})
			return
		}
		if r.URL.Path != "/api/relays/two/select" || r.URL.Query().Get("group") != "transit" || r.URL.Query().Get("activate") != "false" {
			t.Errorf("unexpected selection: %s", r.URL)
		}
		selected = true
		_ = json.NewEncoder(w).Encode(map[string]string{"relay": "transit / two"})
	}))
	defer server.Close()
	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "select", "two", "--group", "transit", "--activate=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !selected || !strings.Contains(out.String(), `within group "transit"`) {
		t.Fatalf("group-only selection output: %s", out.String())
	}
}

func TestRelayGroupExitEligibilityFlags(t *testing.T) {
	for _, operation := range []string{"create", "set"} {
		t.Run(operation, func(t *testing.T) {
			var saved relayGroupConfig
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(relayGroupStatus{Config: relayGroupConfig{Type: "remote", Name: "transit", URL: "https://example.test/relays.yaml", Select: "manual"}})
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
					t.Error(err)
				}
				if operation == "create" {
					w.WriteHeader(http.StatusCreated)
				}
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}))
			defer server.Close()
			cmd := newRootCommand(commandConfig{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, client: server.Client()})
			args := []string{"--addr", server.URL, "relaygroups", operation, "transit", "--exit-eligible=false"}
			if operation == "create" {
				args = append(args, "--url", "https://example.test/relays.yaml")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if saved.ExitEligible == nil || *saved.ExitEligible || saved.URL != "https://example.test/relays.yaml" {
				t.Fatalf("saved group: %#v", saved)
			}
		})
	}
}
