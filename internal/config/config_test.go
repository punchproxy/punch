package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLoadSeedsDefaultConfigTables(t *testing.T) {
	st := openTestStore(t)

	cfg, err := Load(st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DNS.ListenAddress != "0.0.0.0" {
		t.Fatalf("dns listen address = %q, want default first-run address", cfg.DNS.ListenAddress)
	}
	if cfg.DNS.CustomPort != 53 {
		t.Fatalf("dns custom port = %d, want default first-run port", cfg.DNS.CustomPort)
	}
	if got := DNSListenAddr(cfg.DNS); got != "0.0.0.0:53" {
		t.Fatalf("dns listen = %q, want default first-run listen", got)
	}
	if cfg.Check.Interval != 30 {
		t.Fatalf("check interval = %d, want 30", cfg.Check.Interval)
	}
	if cfg.Check.FullInterval != 86400 {
		t.Fatalf("check full interval = %d, want 86400", cfg.Check.FullInterval)
	}
	if cfg.Check.FullTriggerFailures != 5 {
		t.Fatalf("check full trigger failures = %d, want 5", cfg.Check.FullTriggerFailures)
	}
	if cfg.Check.OutsideURL != "https://www.gstatic.com/generate_204" {
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
	want.DNS.ListenAddress = "127.0.0.1"
	want.DNS.CustomPort = 5353
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
	want.TUN = TUN{Routes: []string{"0.0.0.0/1", "128.0.0.0/1"}}
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
		OutsideURL:          "https://example.com/204",
		DomesticURL:         "https://domestic.example/204",
		FullInterval:        60,
		Interval:            15,
		FullTriggerFailures: 3,
		Tolerance:           10,
		Concurrency:         4,
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

func TestOpenMigratesLegacyDNSListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "punch.db")
	seedLegacyConfigBase(t, path, "127.0.0.1:5353")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	cfg, err := Load(st)
	if err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if cfg.DNS.ListenAddress != "127.0.0.1" {
		t.Fatalf("migrated dns.listen_address = %q, want 127.0.0.1", cfg.DNS.ListenAddress)
	}
	if cfg.DNS.CustomPort != 5353 {
		t.Fatalf("migrated dns.custom_port = %d, want 5353", cfg.DNS.CustomPort)
	}
}

func TestSingletonGetSetScalarValues(t *testing.T) {
	st := openTestStore(t)
	if err := Init(st); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := Set("dns.listen_address", "127.0.0.1"); err != nil {
		t.Fatalf("set dns.listen_address: %v", err)
	}
	got, err := Get("dns.listen_address")
	if err != nil {
		t.Fatalf("get dns.listen_address: %v", err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("dns.listen_address = %q, want updated value", got)
	}
	if err := Set("dns.custom_port", "5354"); err != nil {
		t.Fatalf("set dns.custom_port: %v", err)
	}
	got, err = Get("dns.custom_port")
	if err != nil {
		t.Fatalf("get dns.custom_port: %v", err)
	}
	if got != "5354" {
		t.Fatalf("dns.custom_port = %q, want updated value", got)
	}

	persisted, err := Load(st)
	if err != nil {
		t.Fatalf("load persisted: %v", err)
	}
	if persisted.DNS.ListenAddress != "127.0.0.1" {
		t.Fatalf("persisted dns.listen_address = %q, want updated value", persisted.DNS.ListenAddress)
	}
	if persisted.DNS.CustomPort != 5354 {
		t.Fatalf("persisted dns.custom_port = %d, want updated value", persisted.DNS.CustomPort)
	}
	if got := DNSListenAddr(persisted.DNS); got != "127.0.0.1:5354" {
		t.Fatalf("persisted dns listen = %q, want updated value", got)
	}

	if err := Set("check.interval", "15"); err != nil {
		t.Fatalf("set check.interval: %v", err)
	}
	got, err = Get("check.interval")
	if err != nil {
		t.Fatalf("get check.interval: %v", err)
	}
	if got != "15" {
		t.Fatalf("check.interval = %q, want 15", got)
	}
	if err := Set("check.full_interval", "60"); err != nil {
		t.Fatalf("set check.full_interval: %v", err)
	}
	got, err = Get("check.full_interval")
	if err != nil {
		t.Fatalf("get check.full_interval: %v", err)
	}
	if got != "60" {
		t.Fatalf("check.full_interval = %q, want 60", got)
	}
	if err := Set("check.full_trigger_failures", "3"); err != nil {
		t.Fatalf("set check.full_trigger_failures: %v", err)
	}
	got, err = Get("check.full_trigger_failures")
	if err != nil {
		t.Fatalf("get check.full_trigger_failures: %v", err)
	}
	if got != "3" {
		t.Fatalf("check.full_trigger_failures = %q, want 3", got)
	}
	persisted, err = Load(st)
	if err != nil {
		t.Fatalf("load persisted check config: %v", err)
	}
	if persisted.Check.Interval != 15 {
		t.Fatalf("persisted check.interval = %d, want 15", persisted.Check.Interval)
	}
	if persisted.Check.FullInterval != 60 {
		t.Fatalf("persisted check.full_interval = %d, want 60", persisted.Check.FullInterval)
	}
	if persisted.Check.FullTriggerFailures != 3 {
		t.Fatalf("persisted check.full_trigger_failures = %d, want 3", persisted.Check.FullTriggerFailures)
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
	if _, err := Get("dns.listen"); err != ErrNotFound {
		t.Fatalf("get dns.listen error = %v, want ErrNotFound", err)
	}
	if err := Set("dns.listen", "127.0.0.1:5354"); err != ErrNotFound {
		t.Fatalf("set dns.listen error = %v, want ErrNotFound", err)
	}
	if _, err := Get("tun.device"); err != ErrNotFound {
		t.Fatalf("get tun.device error = %v, want ErrNotFound", err)
	}
	if err := Set("tun.device", "utun9"); err != ErrNotFound {
		t.Fatalf("set tun.device error = %v, want ErrNotFound", err)
	}
}

func seedLegacyConfigBase(t *testing.T, path, dnsListen string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("legacy sqlite handle: %v", err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&legacyConfigBaseModel{}); err != nil {
		t.Fatalf("create legacy config_base: %v", err)
	}
	disableIPv6FakeIP := true
	row := legacyConfigBaseModel{
		ID:                       1,
		LogLevel:                 "info",
		AssetRefreshInterval:     3600,
		DNSListen:                dnsListen,
		DNSCacheSize:             100000,
		DNSFakeIPRange:           "198.18.0.0/15",
		DNSFakeIPv6Range:         "fdfe:dcba:9876::/64",
		DNSFakeIPTTL:             "1h",
		DNSDisableIPv6FakeIP:     &disableIPv6FakeIP,
		TUNDevice:                "punch0",
		RelaySelect:              "auto",
		RelayAutoURL:             "http://www.gstatic.com/generate_204",
		CheckDomesticURL:         "http://connect.rom.miui.com/generate_204",
		CheckFullInterval:        86400,
		RelayAutoTolerance:       50,
		RelayCheckConcurrency:    10,
		CheckInterval:            10,
		CheckFullTriggerFailures: 5,
		APIListen:                "127.0.0.1:28854",
		SessionsHistoryLimit:     1000,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert legacy config_base: %v", err)
	}
}

type legacyConfigBaseModel struct {
	ID                       int    `gorm:"column:id;primaryKey;autoIncrement:false"`
	LogLevel                 string `gorm:"column:log_level;not null"`
	LogFile                  string `gorm:"column:log_file;not null"`
	AssetRefreshInterval     int    `gorm:"column:asset_refresh_interval;not null"`
	DNSListen                string `gorm:"column:dns_listen;not null"`
	DNSCacheSize             int    `gorm:"column:dns_cache_size;not null"`
	DNSFakeIPRange           string `gorm:"column:dns_fake_ip_range;not null"`
	DNSFakeIPv6Range         string `gorm:"column:dns_fake_ipv6_range"`
	DNSFakeIPTTL             string `gorm:"column:dns_fakeip_ttl;not null;default:1h"`
	DNSDisableIPv6FakeIP     *bool  `gorm:"column:dns_disable_ipv6_fakeip;default:1"`
	TUNDevice                string `gorm:"column:tun_device;not null"`
	RelaySelect              string `gorm:"column:relay_select;not null"`
	RelayAutoURL             string `gorm:"column:relay_auto_url;not null"`
	CheckDomesticURL         string `gorm:"column:check_domestic_url"`
	CheckFullInterval        int    `gorm:"column:relay_auto_interval;not null"`
	RelayAutoTolerance       int    `gorm:"column:relay_auto_tolerance;not null"`
	RelayCheckConcurrency    int    `gorm:"column:relay_check_concurrency;not null;default:10"`
	CheckInterval            int    `gorm:"column:check_selected_interval;not null;default:10"`
	CheckFullTriggerFailures int    `gorm:"column:check_full_trigger_failures;not null;default:5"`
	APIListen                string `gorm:"column:api_listen;not null"`
	APISecret                string `gorm:"column:api_secret;not null"`
	SessionsHistoryLimit     int    `gorm:"column:sessions_history_limit;not null;default:1000"`
}

func (legacyConfigBaseModel) TableName() string { return "config_base" }

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
