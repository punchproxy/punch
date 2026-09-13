package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestConditionalReplacePreservesConcurrentScalarUpdate(t *testing.T) {
	st := openTestStore(t)
	if err := Init(st); err != nil {
		t.Fatal(err)
	}
	expected, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := WithValue(expected, "check.interval", "99")
	if err != nil {
		t.Fatal(err)
	}
	if expected.Check.Interval == updated.Check.Interval {
		t.Fatal("preparing a scalar update changed the original snapshot")
	}
	if err := Set("dns.cache_size", "12345"); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceIfUnchanged(expected, updated); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replace error = %v, want conflict", err)
	}
	stored, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DNS.CacheSize != 12345 || stored.Check.Interval != expected.Check.Interval {
		t.Fatalf("stale update changed persisted values: cache=%d interval=%d", stored.DNS.CacheSize, stored.Check.Interval)
	}
	expected, err = Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	updated, err = WithValue(expected, "check.interval", "99")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceIfUnchanged(expected, updated); err != nil {
		t.Fatalf("current snapshot replace failed: %v", err)
	}
	stored, err = Load(st)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Check.Interval != 99 || stored.DNS.CacheSize != 12345 {
		t.Fatalf("accepted update changed unrelated values: cache=%d interval=%d", stored.DNS.CacheSize, stored.Check.Interval)
	}
}

func TestRelayExitEligibilityRoundTrip(t *testing.T) {
	st := openTestStore(t)
	eligible, transitOnly := true, false
	cfg := Default()
	cfg.Relay.Groups = []RelayGroup{
		{Type: "inline", Name: "legacy"},
		{Type: "inline", Name: "exit", ExitEligible: &eligible},
		{Type: "inline", Name: "transit", ExitEligible: &transitOnly},
	}
	if err := Save(st, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range cfg.Relay.Groups {
		group := got.Relay.Groups[i]
		if (group.ExitEligible == nil) != (want.ExitEligible == nil) || group.ExitAllowed() != want.ExitAllowed() {
			t.Fatalf("group %q exit eligibility changed: got %#v, want %#v", group.Name, group.ExitEligible, want.ExitEligible)
		}
	}
	cloned := cloneRelayGroups(got.Relay.Groups)
	*cloned[2].ExitEligible = true
	if got.Relay.Groups[2].ExitAllowed() {
		t.Fatal("editing a snapshot changed the source exit eligibility")
	}
}

func TestExistingRelayGroupsRemainExitEligibleAfterMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "punch.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Relay.Groups = []RelayGroup{{Type: "inline", Name: "existing"}}
	if err := Save(st, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Migrator().DropColumn(&relayGroupModel{}, "exit_eligible"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	got, err := Load(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Relay.Groups) != 1 || got.Relay.Groups[0].ExitEligible != nil || !got.Relay.Groups[0].ExitAllowed() {
		t.Fatalf("existing group's default changed after migration: %#v", got.Relay.Groups)
	}
}
