package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOpenMigratesLegacyDNSUpstreamsAsEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "punch.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get legacy database handle: %v", err)
	}
	if err := db.Exec(`CREATE TABLE dns_upstreams (
		position integer PRIMARY KEY, url text NOT NULL, bootstrap text NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create legacy upstream table: %v", err)
	}
	if err := db.Exec("INSERT INTO dns_upstreams VALUES (0, ?, ?)", "https://dns.example/dns-query", "1.1.1.1").Error; err != nil {
		t.Fatalf("insert legacy upstream: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer st.Close()
	got, err := loadDNSUpstreams(st)
	if err != nil {
		t.Fatalf("load migrated upstreams: %v", err)
	}
	want := []Upstream{{URL: "https://dns.example/dns-query", Bootstrap: "1.1.1.1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated upstreams = %+v, want %+v", got, want)
	}
}

func TestDNSUpstreamDisabledPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "punch.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := Default()
	cfg.DNS.Upstream[0].Disabled = true
	if err := Save(st, cfg); err != nil {
		t.Fatalf("save disabled upstream: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	got, err := Load(st)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !reflect.DeepEqual(got.DNS.Upstream, cfg.DNS.Upstream) {
		t.Fatalf("reopened upstreams = %+v, want %+v", got.DNS.Upstream, cfg.DNS.Upstream)
	}
}
