package config

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSettingsRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.GetSetting("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := s.SetSetting("hello", []byte("world")); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetSetting("hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("got %q, want %q", got, "world")
	}

	if err := s.SetSetting("hello", []byte("again")); err != nil {
		t.Fatalf("set upsert: %v", err)
	}
	got, _ = s.GetSetting("hello")
	if string(got) != "again" {
		t.Fatalf("upsert got %q, want %q", got, "again")
	}
}

func TestStoreAssetRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.GetAsset("https://example.com/a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := s.PutAsset("https://example.com/a.txt", []byte("payload"), when); err != nil {
		t.Fatalf("put: %v", err)
	}
	a, err := s.GetAsset("https://example.com/a.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(a.Content) != "payload" {
		t.Fatalf("content mismatch: %q", a.Content)
	}
	if !a.UpdatedAt.Equal(when) {
		t.Fatalf("time mismatch: got %v want %v", a.UpdatedAt, when)
	}

	metas, err := s.ListAssetMeta()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 1 || metas[0].URL != "https://example.com/a.txt" || metas[0].Size != int64(len("payload")) {
		t.Fatalf("meta unexpected: %+v", metas)
	}
}

func TestRelaySelectionsRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := LoadRelaySelections(s); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	want := RelaySelections{
		ActiveGroup: "auto",
		GroupRelay: map[string]string{
			"auto":   "proxy-a",
			"manual": "proxy-b",
		},
	}
	if err := SaveRelaySelections(s, want); err != nil {
		t.Fatalf("save selections: %v", err)
	}
	got, err := LoadRelaySelections(s)
	if err != nil {
		t.Fatalf("load selections: %v", err)
	}
	if got.ActiveGroup != want.ActiveGroup {
		t.Fatalf("active group = %q, want %q", got.ActiveGroup, want.ActiveGroup)
	}
	for group, relay := range want.GroupRelay {
		if got.GroupRelay[group] != relay {
			t.Fatalf("relay for %q = %q, want %q", group, got.GroupRelay[group], relay)
		}
	}
}
