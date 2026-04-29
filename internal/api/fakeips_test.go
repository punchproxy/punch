package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
)

func TestDNSFakeIPsHandler(t *testing.T) {
	cfg := config.Default()
	cfg.DNS.FakeIPRange = "198.18.0.0/24"
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
	ip := dnsServer.FakeIPPool().Lookup("example.com")
	if !dnsServer.FakeIPPool().Acquire(ip, "sess-1") {
		t.Fatal("acquire fake IP failed")
	}

	s := &Server{dns: dnsServer}
	rec := runRelayHandler(t, s.handleDNSFakeIPs, http.MethodGet, "/api/dns/fakeips", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var entries []fakeIPEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries length = %d, want 1", len(entries))
	}
	if entries[0].FakeIP != ip.String() || entries[0].Domain != "example.com" || entries[0].State != "active" {
		t.Fatalf("entry = %#v", entries[0])
	}
	if len(entries[0].SessionIDs) != 1 || entries[0].SessionIDs[0] != "sess-1" {
		t.Fatalf("entry sessions = %v, want [sess-1]", entries[0].SessionIDs)
	}
}
