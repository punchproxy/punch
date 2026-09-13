package relay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

func dependencyConfig() config.Relay {
	transitOnly := false
	return config.Relay{Select: "manual", Groups: []config.RelayGroup{
		{Type: "inline", Name: "exit", Select: "manual", Proxies: []map[string]any{
			{"name": "exit-1", "type": "socks5", "server": "127.0.0.1", "port": 10001, "dialer-proxy": "transit"},
		}},
		{Type: "inline", Name: "transit", Select: "manual", ExitEligible: &transitOnly, Proxies: []map[string]any{
			{"name": "one", "type": "socks5", "server": "127.0.0.1", "port": 10002},
			{"name": "two", "type": "socks5", "server": "127.0.0.1", "port": 10003},
		}},
	}}
}

func newDependencySelector(t *testing.T) *Selector {
	t.Helper()
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := NewSelector(dependencyConfig(), config.Check{OutsideURL: "http://127.0.0.1:10004/check"}, nil, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

func TestDependencyValidationRejectsInvalidGraph(t *testing.T) {
	cases := []struct {
		name string
		edit func(*config.Relay)
		want string
	}{
		{"missing", func(c *config.Relay) { c.Groups[0].Proxies[0]["dialer-proxy"] = "missing" }, "not found"},
		{"nonstring", func(c *config.Relay) { c.Groups[0].Proxies[0]["dialer-proxy"] = 42 }, "nonempty group name"},
		{"empty", func(c *config.Relay) { c.Groups[0].Proxies[0]["dialer-proxy"] = "" }, "nonempty group name"},
		{"self", func(c *config.Relay) { c.Groups[0].Proxies[0]["dialer-proxy"] = "exit" }, "cycle"},
		{"unselected cycle", func(c *config.Relay) { c.Groups[1].Proxies[1]["dialer-proxy"] = "exit" }, "cycle"},
		{"duplicate group", func(c *config.Relay) { c.Groups = append(c.Groups, c.Groups[1]) }, "duplicate"},
		{"unsupported adapter", func(c *config.Relay) { c.Groups[0].Proxies[0]["type"] = "direct" }, "does not support"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dependencyConfig()
			tc.edit(&cfg)
			if err := (&Selector{}).ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateConfig() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDependencySelectionKeepsExitAndInvalidatesOldPath(t *testing.T) {
	s := newDependencySelector(t)
	g := s.groups[0]
	s.mu.Lock()
	before := s.snapshotBenchmarkTargetLocked(benchmarkTarget{group: g, dialer: g.dialers[0]})
	h := s.health[s.healthKey("exit", "exit-1")]
	h.Status, h.URLTestLatency, h.Latency = HealthHealthy, 20, 20
	h.LastCheckedAt = time.Now()
	s.mu.Unlock()
	if _, err := s.SelectGroupRelay("transit", "two", false); err != nil {
		t.Fatal(err)
	}
	if got := s.ActiveName(); got != "exit / exit-1" {
		t.Fatalf("active = %q", got)
	}
	s.mu.RLock()
	current := s.benchmarkTargetCurrentLocked(before)
	after := s.snapshotBenchmarkTargetLocked(benchmarkTarget{group: g, dialer: g.dialers[0]})
	s.mu.RUnlock()
	if current || before.pathKey == after.pathKey || before.dialer == after.dialer {
		t.Fatal("upstream switch reused an old path snapshot")
	}
	if h.Status != HealthPending || h.URLTestLatency != 0 || !h.LastCheckedAt.IsZero() {
		t.Fatalf("old exit result remains current: %+v", h)
	}
	if len(after.path) != 2 || after.path[0] != (RelayHop{Group: "transit", Relay: "two"}) {
		t.Fatalf("path = %+v", after.path)
	}
	if _, err := s.SelectGroupRelay("transit", "one", true); err != ErrGroupNotExitEligible {
		t.Fatalf("activate transit = %v", err)
	}
	for _, row := range s.HealthList() {
		if row.Group == "transit" && row.Name == "transit / two" && (!row.InUse || !row.GroupSelected || row.Selected || row.CheckInterval != 10) {
			t.Fatalf("transit indicators = %+v", row)
		}
	}
}

func TestDependencyConfigChangePreservesOnlyUnchangedPaths(t *testing.T) {
	s := newDependencySelector(t)
	s.mu.Lock()
	old := s.snapshotBenchmarkTargetLocked(benchmarkTarget{group: s.groups[0], dialer: s.groups[0].dialers[0]})
	h := s.health[s.healthKey("exit", "exit-1")]
	h.Status, h.URLTestLatency = HealthHealthy, 25
	s.mu.Unlock()
	if err := s.ApplyConfig(dependencyConfig(), config.Check{Interval: 20}); err != nil {
		t.Fatal(err)
	}
	if got := s.health[s.healthKey("exit", "exit-1")]; got.Status != HealthHealthy || got.URLTestLatency != 25 {
		t.Fatalf("settings edit discarded unchanged health: %+v", got)
	}
	cfg := dependencyConfig()
	cfg.Groups[1].Proxies[0]["username"] = "changed"
	if err := s.ApplyConfig(cfg, config.Check{Interval: 20}); err != nil {
		t.Fatal(err)
	}
	if s.benchmarkTargetCurrentLocked(old) {
		t.Fatal("upstream credential change did not invalidate snapshot")
	}
	if got := s.health[s.healthKey("exit", "exit-1")]; got.Status != HealthPending || got.URLTestLatency != 0 {
		t.Fatalf("changed path retained health: %+v", got)
	}
}

func TestDependencyLevelsExpandAndDeduplicateUpstream(t *testing.T) {
	s := newDependencySelector(t)
	root := s.groups[0]
	target := benchmarkTarget{group: root, dialer: root.dialers[0]}
	levels := s.dependencyLevels([]benchmarkTarget{target, target})
	if len(levels) != 2 || len(levels[0]) != 2 || len(levels[1]) != 1 {
		t.Fatalf("levels = %+v", levels)
	}
	if levels[0][0].group.name != "transit" || levels[1][0].group.name != "exit" {
		t.Fatalf("dependency order = %+v", levels)
	}
}

func TestDependencyRefreshRejectsCycleAndRetainsWorkingGroup(t *testing.T) {
	dir := t.TempDir()
	provider := filepath.Join(dir, "transit.yaml")
	valid := "proxies:\n  - {name: one, type: socks5, server: 127.0.0.1, port: 10002}\n"
	if err := os.WriteFile(provider, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := config.Open(filepath.Join(dir, "punch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	manager, err := assets.New(st, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := dependencyConfig()
	cfg.Groups[1].Type, cfg.Groups[1].URL, cfg.Groups[1].Proxies = "remote", provider, nil
	s, err := NewSelector(cfg, config.Check{}, manager, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	oldGroup := s.groups[1]
	oldPath := s.snapshotBenchmarkTargetLocked(benchmarkTarget{group: s.groups[0], dialer: s.groups[0].dialers[0]})
	invalid := "proxies:\n  - {name: one, type: socks5, server: 127.0.0.1, port: 10002, dialer-proxy: exit}\n"
	if err := os.WriteFile(provider, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadGroup("transit"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("reload error = %v", err)
	}
	if s.groups[1] != oldGroup || !s.benchmarkTargetCurrentLocked(oldPath) || oldGroup.refreshing {
		t.Fatal("invalid refresh replaced the working group or left refresh running")
	}
	if err := os.WriteFile(provider, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	// The provider can change during persistence. Publish the graph that was
	// validated, instead of rereading unvalidated remote content after saving.
	if err := s.ApplyConfigWithSave(cfg, config.Check{}, func() error {
		return os.WriteFile(provider, []byte(invalid), 0600)
	}); err != nil {
		t.Fatalf("provider was reread after save: %v", err)
	}
	if dependencyName(s.groups[1].dialers[0]) != "" {
		t.Fatal("installed provider content that changed after validation")
	}
}

func TestDependencyFailedSaveLeavesRuntimeUnchanged(t *testing.T) {
	s := newDependencySelector(t)
	old := s.groups[0]
	cfg := dependencyConfig()
	cfg.Groups[0].Proxies[0]["port"] = 20001
	wantErr := errors.New("save failed")
	if err := s.ApplyConfigWithSave(cfg, config.Check{}, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("save = %v", err)
	}
	if s.groups[0] != old {
		t.Fatal("failed save changed runtime group")
	}
}
