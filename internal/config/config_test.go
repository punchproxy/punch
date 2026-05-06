package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadSeedsDefaultConfigTables(t *testing.T) {
	st := openTestStore(t)

	cfg, err := Load(st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DNS.Listen != "0.0.0.0:28853" {
		t.Fatalf("dns listen = %q, want default first-run listen", cfg.DNS.Listen)
	}
	if cfg.Check.SelectedInterval != 10 {
		t.Fatalf("check selected interval = %d, want 10", cfg.Check.SelectedInterval)
	}
	if cfg.Check.OutsideURL != "http://www.gstatic.com/generate_204" {
		t.Fatalf("check outside url = %q, want default outside url", cfg.Check.OutsideURL)
	}
	if cfg.Check.DomesticURL != "http://connect.rom.miui.com/generate_204" {
		t.Fatalf("check domestic url = %q, want default domestic url", cfg.Check.DomesticURL)
	}

	var count int64
	if err := st.DB().Model(&configBaseModel{}).Count(&count).Error; err != nil {
		t.Fatalf("count config_base: %v", err)
	}
	if count != 1 {
		t.Fatalf("config_base rows = %d, want 1", count)
	}
}

func TestConfigSaveLoadRoundTrip(t *testing.T) {
	st := openTestStore(t)
	want := Default()
	want.LogLevel = "debug"
	want.LogFile = "/tmp/punch.log"
	want.AssetRefreshInterval = 42
	want.DNS.Listen = "127.0.0.1:5353"
	want.DNS.Upstream = []Upstream{
		{URL: "https://dns.example/dns-query", Bootstrap: "1.1.1.1", Domains: []string{"domain:example.com"}},
		{URL: "tls://1.0.0.1:853"},
	}
	want.DNS.CacheSize = 1234
	want.DNS.FakeIPRange = "198.19.0.0/16"
	want.DNS.FakeIPv6Range = "fdfe:dcba:9877::/64"
	want.DNS.FakeIPTTL = "30m"
	want.DNS.Rules = DNSRules{
		Domains: []DomainRule{
			{Decision: DecisionReject, Source: "qtype:ptr"},
			{Decision: DecisionReject, Source: "qtype:https"},
			{Decision: DecisionReject, Source: "domain:ads.example"},
			{Decision: DecisionRelay, Source: "keyword:blocked"},
			{Decision: DecisionDirect, Source: "full:local.example"},
		},
		CIDRs: []CIDRRule{
			{Decision: DecisionDirect, Source: "10.0.0.0/8"},
			{Decision: DecisionReject, Source: "203.0.113.0/24"},
		},
	}
	want.TUN = TUN{Device: "utun9", Routes: []string{"0.0.0.0/1", "128.0.0.0/1"}}
	want.Relay = Relay{
		Select: "manual",
		Groups: []RelayGroup{{
			Type:            "inline",
			Name:            "test",
			Select:          "manual",
			Proxies:         []map[string]any{{"name": "p1", "type": "direct"}},
			RefreshDuration: 120,
		}},
	}
	want.Check = Check{
		OutsideURL:       "https://example.com/204",
		DomesticURL:      "https://domestic.example/204",
		Interval:         60,
		Tolerance:        10,
		Concurrency:      4,
		SelectedInterval: 15,
	}
	want.API = API{Listen: "127.0.0.1:18080", Secret: "secret"}

	if err := Save(st, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestConfigUsesRelationalTables(t *testing.T) {
	st := openTestStore(t)
	cfg := Default()
	cfg.TUN.Routes = []string{"10.0.0.0/8"}
	cfg.DNS.Upstream = []Upstream{{URL: "https://dns.example/dns-query", Bootstrap: "1.1.1.1", Domains: []string{"domain:example.com"}}}
	cfg.Relay.Groups = []RelayGroup{{
		Type:    "inline",
		Name:    "inline",
		Select:  "manual",
		Proxies: []map[string]any{{"name": "p1", "type": "direct"}},
	}}

	if err := Save(st, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	counts := map[string]struct {
		model any
		want  int64
	}{
		"tun_routes":           {model: &tunRouteModel{}, want: 1},
		"dns_upstream_domains": {model: &dnsUpstreamDomainModel{}, want: 1},
		"relay_groups":         {model: &relayGroupModel{}, want: 1},
		"relay_group_proxies":  {model: &relayGroupProxyModel{}, want: 1},
	}
	for name, tc := range counts {
		var count int64
		if err := st.DB().Model(tc.model).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != tc.want {
			t.Fatalf("%s rows = %d, want %d", name, count, tc.want)
		}
	}
}

func TestSingletonGetSetScalarValues(t *testing.T) {
	st := openTestStore(t)
	if err := Init(st); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := Set("dns.listen", "127.0.0.1:5354"); err != nil {
		t.Fatalf("set dns.listen: %v", err)
	}
	got, err := Get("dns.listen")
	if err != nil {
		t.Fatalf("get dns.listen: %v", err)
	}
	if got != "127.0.0.1:5354" {
		t.Fatalf("dns.listen = %q, want updated value", got)
	}

	persisted, err := Load(st)
	if err != nil {
		t.Fatalf("load persisted: %v", err)
	}
	if persisted.DNS.Listen != "127.0.0.1:5354" {
		t.Fatalf("persisted dns.listen = %q, want updated value", persisted.DNS.Listen)
	}

	if err := Set("check.selected_interval", "15"); err != nil {
		t.Fatalf("set check.selected_interval: %v", err)
	}
	got, err = Get("check.selected_interval")
	if err != nil {
		t.Fatalf("get check.selected_interval: %v", err)
	}
	if got != "15" {
		t.Fatalf("check.selected_interval = %q, want 15", got)
	}
	persisted, err = Load(st)
	if err != nil {
		t.Fatalf("load persisted check config: %v", err)
	}
	if persisted.Check.SelectedInterval != 15 {
		t.Fatalf("persisted check.selected_interval = %d, want 15", persisted.Check.SelectedInterval)
	}

	if err := Set("check.outside_url", "https://outside.example/204"); err != nil {
		t.Fatalf("set check.outside_url: %v", err)
	}
	if err := Set("check.domestic_url", "https://domestic.example/204"); err != nil {
		t.Fatalf("set check.domestic_url: %v", err)
	}
	got, err = Get("check.outside_url")
	if err != nil {
		t.Fatalf("get check.outside_url: %v", err)
	}
	if got != "https://outside.example/204" {
		t.Fatalf("check.outside_url = %q, want https://outside.example/204", got)
	}
	got, err = Get("check.domestic_url")
	if err != nil {
		t.Fatalf("get check.domestic_url: %v", err)
	}
	if got != "https://domestic.example/204" {
		t.Fatalf("check.domestic_url = %q, want https://domestic.example/204", got)
	}
	if _, err := Get("check.url"); err != ErrNotFound {
		t.Fatalf("get check.url error = %v, want ErrNotFound", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return st
}
