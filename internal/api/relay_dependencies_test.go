package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/relay"
)

func newRelayDependencyServer(t *testing.T) *Server {
	t.Helper()
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := config.Init(st); err != nil {
		t.Fatal(err)
	}
	transitOnly := false
	cfg := config.Default()
	cfg.Relay = config.Relay{Select: "manual", Groups: []config.RelayGroup{
		{Type: "inline", Name: "exit", Select: "manual", Proxies: []map[string]any{
			{"name": "out", "type": "socks5", "server": "127.0.0.1", "port": 10001, "dialer-proxy": "transit"},
		}},
		{Type: "inline", Name: "transit", Select: "manual", ExitEligible: &transitOnly, Proxies: []map[string]any{
			{"name": "one", "type": "socks5", "server": "127.0.0.1", "port": 10002},
			{"name": "two", "type": "socks5", "server": "127.0.0.1", "port": 10003},
		}},
	}}
	if err := config.Replace(cfg); err != nil {
		t.Fatal(err)
	}
	sel, err := relay.NewSelector(cfg.Relay, cfg.Check, nil, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sel.Stop)
	return &Server{store: st, selector: sel}
}

func TestRelayDependencyMutationRejectsBeforePersistence(t *testing.T) {
	s := newRelayDependencyServer(t)
	before, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	rec := runRelayHandler(t, s.handleUpdateRelay, http.MethodPut, "/api/relaygroups/exit/relays/out", map[string]string{"group": "exit", "relay": "out"}, map[string]any{
		"name": "out", "type": "socks5", "server": "127.0.0.1", "port": 10001, "dialer-proxy": "missing",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid reference: status %d: %s", rec.Code, rec.Body.String())
	}
	rec = runRelayHandler(t, s.handleDeleteRelayGroup, http.MethodDelete, "/api/relaygroups/transit", map[string]string{"group": "transit"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete referenced group: status %d: %s", rec.Code, rec.Body.String())
	}
	after, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Relay, after.Relay) {
		t.Fatal("rejected mutation changed the persisted relay configuration")
	}
	if s.selector.ActiveName() != "exit / out" {
		t.Fatalf("rejected mutation changed active relay to %q", s.selector.ActiveName())
	}
}

func TestSelectRelayWithinTransitGroupKeepsExit(t *testing.T) {
	s := newRelayDependencyServer(t)
	rec := runRelayHandler(t, s.handleSelectRelay, http.MethodPost, "/api/relays/two/select?group=transit&activate=false", map[string]string{"relay": "two"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("select transit member: status %d: %s", rec.Code, rec.Body.String())
	}
	if s.selector.ActiveName() != "exit / out" {
		t.Fatalf("group-only selection changed exit to %q", s.selector.ActiveName())
	}
	selections, err := config.LoadRelaySelections(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if selections.ActiveGroup != "exit" || selections.GroupRelay["transit"] != "two" {
		t.Fatalf("selection was not saved correctly: %#v", selections)
	}
	for _, query := range []string{"?group=transit", "?group=transit&activate=true", "?group=transit&activate=invalid", "?activate=false"} {
		rec = runRelayHandler(t, s.handleSelectRelay, http.MethodPost, "/api/relays/one/select"+query, map[string]string{"relay": "one"}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status %d: %s", query, rec.Code, rec.Body.String())
		}
	}
	if s.selector.ActiveName() != "exit / out" {
		t.Fatal("rejected selection changed exit")
	}
}

func TestLiveRelayConfigSaveFailureLeavesRuntimeUnchanged(t *testing.T) {
	s := newRelayDependencyServer(t)
	before := s.selector.GroupList()
	value, err := config.Get("check.interval")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	rec := runRelayHandler(t, s.handleSetConfigValue, http.MethodPut, "/api/config/check.interval", map[string]string{"key": "check.interval"}, configValueRequest{Value: "99"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("failed scalar save: status %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := config.Get("check.interval"); err != nil || got != value {
		t.Fatalf("failed save changed singleton value: %q, %v", got, err)
	}
	if after := s.selector.GroupList(); !reflect.DeepEqual(before, after) {
		t.Fatal("failed scalar save changed runtime groups")
	}
	cfg, err := config.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	group := cfg.Relay.Groups[1]
	eligible := true
	group.ExitEligible = &eligible
	rec = runRelayHandler(t, s.handleUpdateRelayGroup, http.MethodPut, "/api/relaygroups/transit", map[string]string{"group": "transit"}, group)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("failed group save: status %d: %s", rec.Code, rec.Body.String())
	}
	if after := s.selector.GroupList(); !reflect.DeepEqual(before, after) {
		t.Fatal("failed group save changed runtime groups")
	}
}

func TestLiveRelayConfigConflictLeavesRuntimeUnchanged(t *testing.T) {
	s := newRelayDependencyServer(t)
	before := s.selector.GroupList()
	expected, err := config.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := config.WithValue(expected, "check.interval", "99")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Set("dns.cache_size", "12345"); err != nil {
		t.Fatal(err)
	}
	err = s.selector.ApplyConfigWithSave(updated.Relay, updated.Check, func() error {
		return config.ReplaceIfUnchanged(expected, updated)
	})
	if !errors.Is(err, config.ErrConflict) || configErrorStatus(err) != http.StatusConflict {
		t.Fatalf("stale live update error = %v, status = %d", err, configErrorStatus(err))
	}
	if after := s.selector.GroupList(); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected stale update changed runtime relay state")
	}
	stored, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DNS.CacheSize != 12345 || stored.Check.Interval != expected.Check.Interval {
		t.Fatalf("rejected stale update changed stored values: cache=%d interval=%d", stored.DNS.CacheSize, stored.Check.Interval)
	}
}
