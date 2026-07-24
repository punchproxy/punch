package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

func TestDirectRelayIsHealthyWithoutLatencyCheck(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	selector, err := NewSelector(
		config.Relay{Select: "auto"},
		config.Check{
			OutsideURL:   "http://www.gstatic.com/generate_204",
			FullInterval: 300,
			Tolerance:    50,
		},
		nil, func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("direct relay should not be benchmarked")
			return nil, nil
		}, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}

	selector.Benchmark()
	health := selector.HealthList()
	if len(health) != 1 {
		t.Fatalf("HealthList() length = %d, want 1", len(health))
	}
	if health[0].Group != directGroupName || health[0].Status != HealthHealthy || health[0].Latency != 0 || !health[0].LastCheckedAt.IsZero() {
		t.Fatalf("DIRECT health = %#v", health[0])
	}
}

func TestDedupeRelayMappingsRenamesDuplicateNames(t *testing.T) {
	got := dedupeRelayMappings([]map[string]any{
		{"name": "hk-1", "type": "ss"},
		{"name": "hk-1", "type": "vmess"},
		{"name": "hk-1-1", "type": "direct"},
		{"name": "hk-1", "type": "trojan"},
	})
	var names []string
	for _, mapping := range got {
		name, _ := mapping["name"].(string)
		names = append(names, name)
	}
	want := []string{"hk-1", "hk-1-1", "hk-1-1-1", "hk-1-2"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %#v, want %#v", names, want)
		}
	}
}

func TestRemoteRelayGroupIgnoresProviderResolvers(t *testing.T) {
	dir := t.TempDir()
	providerPath := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(providerPath, []byte(`
proxies:
  - name: hk-1
    type: ss
    server: relay.example
    port: 443
    cipher: aes-128-gcm
    password: secret
resolvers:
  - url: https://dns.example/dns-query
    bootstrap: 1.1.1.1
`), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}

	st, err := config.Open(filepath.Join(dir, "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	assetManager, err := assets.New(st, 0, nil, nil)
	if err != nil {
		t.Fatalf("new asset manager: %v", err)
	}

	selector := &Selector{
		assets: assetManager,
		resolveRelayDomain: func(ctx context.Context, groupName, host string) ([]netip.Addr, time.Time, error) {
			if groupName != "main" {
				t.Fatalf("groupName = %q, want main", groupName)
			}
			if host != "relay.example" {
				t.Fatalf("host = %q, want relay.example", host)
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, time.Now().Add(time.Minute), nil
		},
	}
	group, err := selector.buildGroup(config.RelayGroup{
		Type: "remote",
		Name: "main",
		URL:  providerPath,
	}, assetManager, map[string]relayPayload{})
	if err != nil {
		t.Fatalf("buildGroup() error = %v", err)
	}
	if len(group.dialers) != 1 {
		t.Fatalf("dialers = %d, want 1", len(group.dialers))
	}
	dialer, ok := group.dialers[0].(*LazyRelayDialer)
	if !ok {
		t.Fatalf("dialer type = %T, want *LazyRelayDialer", group.dialers[0])
	}
	addr, err := dialer.resolvedRelayAddr(context.Background())
	if err != nil {
		t.Fatalf("resolvedRelayAddr() error = %v", err)
	}
	if addr != "203.0.113.10:443" {
		t.Fatalf("addr = %q, want 203.0.113.10:443", addr)
	}
}

func TestBenchmarkTargetReevaluatesGlobalAutoSelection(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	current := &testDialer{name: "current"}
	preferred := &testDialer{name: "preferred"}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "auto",
		outsideURL: target.URL,
		tolerance:  50 * time.Millisecond,
		store:      st,
		bus:        eventbus.New(),
		groups: []*group{
			{name: "current", mode: "auto", dialers: []Dialer{current}},
			{name: "preferred", mode: "auto", dialers: []Dialer{preferred}},
		},
	}
	selector.health[selector.healthKey("current", "current")] = &RelayHealth{
		Name:           selector.displayName("current", "current"),
		Group:          "current",
		Status:         HealthHealthy,
		Latency:        1000,
		URLTestLatency: 1000,
	}
	selector.health[selector.healthKey("preferred", "preferred")] = &RelayHealth{
		Name:   selector.displayName("preferred", "preferred"),
		Group:  "preferred",
		Status: HealthUntested,
	}

	if err := selector.BenchmarkTarget("preferred"); err != nil {
		t.Fatalf("BenchmarkTarget() error = %v", err)
	}
	if got := selector.ActiveName(); got != "preferred / preferred" {
		t.Fatalf("ActiveName() = %q, want preferred / preferred", got)
	}
	health := selector.health[selector.healthKey("preferred", "preferred")]
	if health.Status != HealthHealthy || health.URLTestLatency == 0 {
		t.Fatalf("preferred health = %#v", health)
	}
}

func TestReportAutoRelaySwitchReason(t *testing.T) {
	cases := []struct {
		name            string
		prevStatus      HealthStatus
		wantReason      string
		wantFromLatency string
	}{
		{name: "previous relay down is fail-over", prevStatus: HealthDown, wantReason: "fail-over", wantFromLatency: "from_latency_ms=0"},
		{name: "previous relay healthy is latency optimization", prevStatus: HealthHealthy, wantReason: "latency optimization", wantFromLatency: "from_latency_ms=200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prevLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prevLogger) })

			s := &Selector{
				health: make(map[string]*RelayHealth),
				groups: []*group{
					{name: "old", dialers: []Dialer{&testDialer{name: "old"}}},
					{name: "new", dialers: []Dialer{&testDialer{name: "new"}}},
				},
			}
			prevKey := s.healthKey("old", "old")
			s.health[prevKey] = &RelayHealth{Name: "old / old", Group: "old", Status: tc.prevStatus, URLTestLatency: 200}
			s.health[s.healthKey("new", "new")] = &RelayHealth{Name: "new / new", Group: "new", Status: HealthHealthy, URLTestLatency: 80}
			prevName := s.activeNameLocked()

			s.active.Store(1)
			s.reportAutoRelaySwitchLocked(prevName, prevKey)

			logged := buf.String()
			if !strings.Contains(logged, "relay switched") || !strings.Contains(logged, tc.wantReason) {
				t.Fatalf("switch log = %q, want relay switched with reason %q", logged, tc.wantReason)
			}
			if !strings.Contains(logged, tc.wantFromLatency) || !strings.Contains(logged, "to_latency_ms=80") {
				t.Fatalf("switch log = %q, want %s and to_latency_ms=80", logged, tc.wantFromLatency)
			}
		})
	}
}

func TestAutoSelectionKeepsRelayWithinTolerance(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	current := &testDialer{name: "a"}
	other := &countingDialer{testDialer: testDialer{name: "b"}}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "manual",
		outsideURL: "http://127.0.0.1:9",
		tolerance:  50 * time.Millisecond,
		store:      st,
		bus:        eventbus.New(),
		groups:     []*group{{name: "g", mode: "auto", dialers: []Dialer{current, other}}},
	}
	selector.health[selector.healthKey("g", "a")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 100}
	selector.health[selector.healthKey("g", "b")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 80}

	selector.reevaluateAutoSelections()

	if got := selector.ActiveName(); got != "g / a" {
		t.Fatalf("ActiveName() = %q, want g / a (20ms gain is within 50ms tolerance)", got)
	}
	if got := other.checks.Load(); got != 0 {
		t.Fatalf("candidate checks = %d, want 0 (no switch, no confirmation check)", got)
	}
}

func TestAutoSelectionSwitchesBeyondToleranceAfterConfirmation(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	current := &testDialer{name: "a"}
	other := &countingDialer{testDialer: testDialer{name: "b"}}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "manual",
		outsideURL: target.URL,
		tolerance:  50 * time.Millisecond,
		store:      st,
		bus:        eventbus.New(),
		groups:     []*group{{name: "g", mode: "auto", dialers: []Dialer{current, other}}},
	}
	selector.health[selector.healthKey("g", "a")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 200}
	selector.health[selector.healthKey("g", "b")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 30}

	selector.reevaluateAutoSelections()

	if got := selector.ActiveName(); got != "g / b" {
		t.Fatalf("ActiveName() = %q, want g / b", got)
	}
	if got := other.checks.Load(); got != 1 {
		t.Fatalf("candidate checks = %d, want 1 confirmation check before the switch", got)
	}
}

func TestLatencySwitchCancelledWhenConfirmationMissesTolerance(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(slow.Close)

	var buf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	current := &testDialer{name: "a"}
	candidate := &fixedTargetDialer{testDialer: testDialer{name: "b"}, target: slow.Listener.Addr().String()}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "auto",
		outsideURL: slow.URL,
		tolerance:  50 * time.Millisecond,
		store:      st,
		bus:        eventbus.New(),
		groups: []*group{
			{name: "ga", dialers: []Dialer{current}},
			{name: "gb", dialers: []Dialer{candidate}},
		},
	}
	selector.health[selector.healthKey("ga", "a")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 100}
	// Stale health claims the candidate is far faster; the confirmation check
	// will measure ~200ms and disprove it.
	selector.health[selector.healthKey("gb", "b")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 10}

	selector.reevaluateAutoSelections()

	if got := selector.ActiveName(); got != "ga / a" {
		t.Fatalf("ActiveName() = %q, want ga / a (switch should be cancelled)", got)
	}
	if !strings.Contains(buf.String(), "relay switch cancelled after confirmation check") {
		t.Fatalf("log = %q, want cancellation message", buf.String())
	}
	if fresh := selector.health[selector.healthKey("gb", "b")].URLTestLatency; fresh < 150 {
		t.Fatalf("candidate URLTestLatency = %dms, want the fresh ~200ms measurement", fresh)
	}
}

func TestFailOverSwitchSkipsConfirmationCheck(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	current := &testDialer{name: "a"}
	other := &countingDialer{testDialer: testDialer{name: "b"}}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "manual",
		outsideURL: "http://127.0.0.1:9",
		tolerance:  50 * time.Millisecond,
		store:      st,
		bus:        eventbus.New(),
		groups:     []*group{{name: "g", mode: "auto", dialers: []Dialer{current, other}}},
	}
	selector.health[selector.healthKey("g", "a")] = &RelayHealth{Status: HealthDown}
	selector.health[selector.healthKey("g", "b")] = &RelayHealth{Status: HealthHealthy, URLTestLatency: 80}

	selector.reevaluateAutoSelections()

	if got := selector.ActiveName(); got != "g / b" {
		t.Fatalf("ActiveName() = %q, want g / b", got)
	}
	if got := other.checks.Load(); got != 0 {
		t.Fatalf("candidate checks = %d, want 0 (fail-over switches immediately)", got)
	}
}

func TestApplyConfigPreservesExistingHealthAndMarksNewRelaysPending(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	cfg := config.Relay{
		Select: "auto",
		Groups: []config.RelayGroup{{
			Type: "inline",
			Name: "main",
			Proxies: []map[string]any{{
				"name": "hk-1",
				"type": "direct",
			}},
		}},
	}
	checkCfg := config.Check{OutsideURL: "http://www.gstatic.com/generate_204", FullInterval: 300, Tolerance: 50}
	selector, err := NewSelector(cfg, checkCfg, nil, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	key := selector.healthKey("main", "hk-1")
	selector.mu.Lock()
	selector.health[key].Status = HealthHealthy
	selector.health[key].Latency = 42
	selector.health[key].URLTestLatency = 42
	checkedAt := time.Now().Add(-time.Minute)
	selector.health[key].LastCheckedAt = checkedAt
	selector.mu.Unlock()

	cfg.Groups[0].Proxies = append(cfg.Groups[0].Proxies, map[string]any{
		"name": "jp-1",
		"type": "direct",
	})
	if err := selector.ApplyConfig(cfg, checkCfg); err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}

	health := map[string]RelayHealth{}
	for _, h := range selector.HealthList() {
		health[h.Name] = h
	}
	if got := health["main / hk-1"]; got.Status != HealthHealthy || got.Latency != 42 || !got.LastCheckedAt.Equal(checkedAt) {
		t.Fatalf("existing relay health was not preserved: %#v", got)
	}
	if got := health["main / jp-1"]; got.Status != HealthPending {
		t.Fatalf("new relay status = %q, want %q", got.Status, HealthPending)
	}
}

func TestApplyConfigRetiresAdaptersWithoutClosingActiveStreams(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	selector, err := NewSelector(config.Relay{}, config.Check{}, nil, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	old := &closeTrackingDialer{}
	t.Cleanup(func() { _ = old.Close() })

	selector.mu.Lock()
	selector.groups = []*group{{
		name:    "old",
		mode:    "manual",
		dialers: []Dialer{old},
	}}
	selector.active.Store(0)
	selector.mu.Unlock()

	if err := selector.ApplyConfig(config.Relay{}, config.Check{}); err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if old.closed.Load() {
		t.Fatal("superseded adapter was closed while live streams may still reference it")
	}
}

func TestHealthListIncludesRelaySpec(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	cfg := config.Relay{
		Select: "auto",
		Groups: []config.RelayGroup{{
			Type: "inline",
			Name: "main",
			Proxies: []map[string]any{{
				"name": "hk-1",
				"type": "direct",
			}},
		}},
	}
	checkCfg := config.Check{OutsideURL: "http://www.gstatic.com/generate_204", FullInterval: 300, Tolerance: 50}
	selector, err := NewSelector(cfg, checkCfg, nil, nil, st, eventbus.New(), nil)
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}

	var got RelayHealth
	for _, h := range selector.HealthList() {
		if h.Name == "main / hk-1" {
			got = h
			break
		}
	}
	if got.Spec["name"] != "hk-1" || got.Spec["type"] != "direct" {
		t.Fatalf("relay spec = %#v", got.Spec)
	}
	got.Spec["name"] = "mutated"
	if selector.HealthList()[0].Spec["name"] != "hk-1" {
		t.Fatalf("HealthList exposed mutable relay spec: %#v", selector.HealthList()[0].Spec)
	}
}

func TestBenchmarkLimitsConcurrentRelayChecks(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	var active atomic.Int64
	var maxActive atomic.Int64
	dialers := make([]Dialer, 12)
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "auto",
		outsideURL: target.URL,
		store:      st,
		bus:        eventbus.New(),
	}
	g := &group{name: "main", mode: "auto"}
	for i := range dialers {
		d := &meteredDialer{
			testDialer: testDialer{name: "relay-" + strconv.Itoa(i)},
			active:     &active,
			maxActive:  &maxActive,
		}
		dialers[i] = d
		g.dialers = append(g.dialers, d)
		selector.health[selector.healthKey(g.name, d.Name())] = &RelayHealth{
			Name:   selector.displayName(g.name, d.Name()),
			Group:  g.name,
			Status: HealthPending,
		}
	}
	selector.groups = []*group{g}

	selector.Benchmark()
	if got := maxActive.Load(); got > defaultCheckConcurrency {
		t.Fatalf("max concurrent checks = %d, want <= %d", got, defaultCheckConcurrency)
	}
	for _, h := range selector.HealthList() {
		if h.Status == HealthPending || h.Status == HealthChecking || h.Status == HealthUntested {
			t.Fatalf("relay left in transient status after benchmark: %#v", h)
		}
	}
}

func TestBenchmarkUsesConfiguredCheckConcurrencyAcrossOverlappingRuns(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	const concurrency = 3
	var active atomic.Int64
	var maxActive atomic.Int64
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "auto",
		outsideURL: target.URL,
		store:      st,
		bus:        eventbus.New(),
		checkSem:   make(chan struct{}, concurrency),
	}
	g := &group{name: "main", mode: "auto"}
	for i := 0; i < 9; i++ {
		d := &meteredDialer{
			testDialer: testDialer{name: "relay-" + strconv.Itoa(i)},
			active:     &active,
			maxActive:  &maxActive,
		}
		g.dialers = append(g.dialers, d)
		selector.health[selector.healthKey(g.name, d.Name())] = &RelayHealth{
			Name:   selector.displayName(g.name, d.Name()),
			Group:  g.name,
			Status: HealthPending,
		}
	}
	selector.groups = []*group{g}

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			selector.Benchmark()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("benchmark timed out")
		}
	}
	if got := maxActive.Load(); got > concurrency {
		t.Fatalf("max concurrent checks = %d, want <= %d", got, concurrency)
	}
}

func TestSelectedCheckLoopChecksOnlyActiveRelay(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	active := &countingDialer{testDialer: testDialer{name: "active"}}
	inactive := &countingDialer{testDialer: testDialer{name: "inactive"}}
	selector := &Selector{
		health:                make(map[string]*RelayHealth),
		mode:                  "manual",
		outsideURL:            target.URL,
		selectedCheckInterval: 10 * time.Millisecond,
		store:                 st,
		bus:                   eventbus.New(),
		stopCh:                make(chan struct{}),
		selectedConfigCh:      make(chan struct{}, 1),
	}
	g := &group{name: "main", mode: "manual", dialers: []Dialer{active, inactive}}
	selector.groups = []*group{g}
	for _, d := range g.dialers {
		selector.health[selector.healthKey(g.name, d.Name())] = &RelayHealth{
			Name:   selector.displayName(g.name, d.Name()),
			Group:  g.name,
			Status: HealthPending,
		}
	}

	done := make(chan struct{})
	go func() {
		selector.selectedCheckLoop()
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for active.checks.Load() == 0 {
		select {
		case <-deadline:
			selector.Stop()
			t.Fatal("selected relay was not checked")
		case <-time.After(10 * time.Millisecond):
		}
	}

	selector.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("selected check loop did not stop")
	}
	if got := inactive.checks.Load(); got != 0 {
		t.Fatalf("inactive checks = %d, want 0", got)
	}
}

func TestSelectedConnectivityBypassesBenchmarkSemaphore(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	active := &countingDialer{testDialer: testDialer{name: "active"}}
	selector := &Selector{
		health:        make(map[string]*RelayHealth),
		mode:          "manual",
		outsideURL:    target.URL,
		domesticURL:   target.URL,
		outsideHealth: ConnectivityCheck{URL: target.URL},
		checkSem:      make(chan struct{}, 1),
		store:         st,
		bus:           eventbus.New(),
	}
	selector.checkSem <- struct{}{}
	t.Cleanup(func() { <-selector.checkSem })

	g := &group{name: "main", mode: "manual", dialers: []Dialer{active}}
	selector.groups = []*group{g}
	staleCheckedAt := time.Now().Add(-time.Hour)
	selector.health[selector.healthKey(g.name, active.Name())] = &RelayHealth{
		Name:              selector.displayName(g.name, active.Name()),
		Group:             g.name,
		Status:         HealthHealthy,
		Latency:        99,
		URLTestLatency: 99,
		LastCheckedAt:  staleCheckedAt,
	}

	done := make(chan struct{})
	go func() {
		selector.CheckSelectedConnectivity()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("selected connectivity check blocked behind benchmark semaphore")
	}

	status := selector.ConnectivityStatus()
	if status.Domestic.LastCheckedAt.IsZero() {
		t.Fatal("domestic connectivity was not checked")
	}
	if !status.Outside.LastCheckedAt.After(staleCheckedAt) {
		t.Fatalf("outside connectivity check time = %s, want after stale relay time %s", status.Outside.LastCheckedAt, staleCheckedAt)
	}
	if status.Outside.URL != target.URL || status.Outside.Status != HealthHealthy || status.Outside.Latency <= 0 {
		t.Fatalf("outside connectivity status = %#v", status.Outside)
	}
	if got := active.checks.Load(); got != 1 {
		t.Fatalf("selected relay checks = %d, want 1", got)
	}
}

func TestConnectivityStatusReturnsOutsideHealthRegardlessOfActiveRelay(t *testing.T) {
	checkedAt := time.Now().Add(-time.Minute).Round(0)
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		outsideURL: "http://example.test/generate_204",
		outsideHealth: ConnectivityCheck{
			URL:           "http://example.test/generate_204",
			Status:        HealthHealthy,
			Latency:       42,
			LastCheckedAt: checkedAt,
			History: []HealthRecord{{
				Time:    checkedAt,
				Status:  HealthHealthy,
				Latency: 42,
			}},
		},
		// outsideHealthKey points to a relay that is no longer active —
		// ConnectivityStatus should still return outsideHealth verbatim.
		outsideHealthKey: "main\x00previous",
	}
	active := &testDialer{name: "active"}
	g := &group{name: "main", mode: "manual", dialers: []Dialer{active}}
	selector.groups = []*group{g}

	status := selector.ConnectivityStatus()
	if status.Outside.Status != HealthHealthy || status.Outside.Latency != 42 || !status.Outside.LastCheckedAt.Equal(checkedAt) {
		t.Fatalf("outside connectivity = %#v", status.Outside)
	}
	if len(status.Outside.History) != 1 {
		t.Fatalf("outside history length = %d, want 1", len(status.Outside.History))
	}
}

func TestSelectedCheckFailuresTriggerFullBenchmark(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	bad := &failingDialer{testDialer: testDialer{name: "bad"}}
	good := &countingDialer{testDialer: testDialer{name: "good"}}
	selector := &Selector{
		health:              make(map[string]*RelayHealth),
		mode:                "auto",
		outsideURL:          target.URL,
		fullTriggerFailures: 2,
		store:               st,
		bus:                 eventbus.New(),
	}
	g := &group{name: "main", mode: "auto", dialers: []Dialer{bad, good}}
	selector.groups = []*group{g}
	selector.health[selector.healthKey(g.name, bad.Name())] = &RelayHealth{
		Name:           selector.displayName(g.name, bad.Name()),
		Group:          g.name,
		Status:         HealthHealthy,
		Latency:        20,
		URLTestLatency: 20,
	}
	// The alternate relay has a known-good cached latency: a single selected
	// check failure must still hold position rather than fail over to it.
	selector.health[selector.healthKey(g.name, good.Name())] = &RelayHealth{
		Name:           selector.displayName(g.name, good.Name()),
		Group:          g.name,
		Status:         HealthHealthy,
		Latency:        30,
		URLTestLatency: 30,
	}

	selector.CheckSelectedConnectivity()
	if got := good.checks.Load(); got != 0 {
		t.Fatalf("healthy relay checks after first selected failure = %d, want 0", got)
	}
	if got := selector.ActiveName(); got != "main / bad" {
		t.Fatalf("active relay after first selected failure = %q, want bad", got)
	}

	selector.CheckSelectedConnectivity()
	if got := good.checks.Load(); got != 1 {
		t.Fatalf("healthy relay checks after trigger = %d, want 1", got)
	}
	if got := selector.ActiveName(); got != "main / good" {
		t.Fatalf("active relay after full benchmark = %q, want good", got)
	}
	if got := selector.selectedCheckFailures; got != 0 {
		t.Fatalf("selected check failures after trigger = %d, want 0", got)
	}
}

func TestSelectedCheckFailuresIgnoredWhenInternetDown(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	bad := &failingDialer{testDialer: testDialer{name: "bad"}}
	good := &countingDialer{testDialer: testDialer{name: "good"}}
	selector := &Selector{
		health:              make(map[string]*RelayHealth),
		mode:                "auto",
		outsideURL:          target.URL,
		domesticURL:         target.URL,
		fullTriggerFailures: 2,
		directDialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("internet unavailable")
		},
		store: st,
		bus:   eventbus.New(),
	}
	g := &group{name: "main", mode: "auto", dialers: []Dialer{bad, good}}
	selector.groups = []*group{g}
	for _, d := range g.dialers {
		selector.health[selector.healthKey(g.name, d.Name())] = &RelayHealth{
			Name:   selector.displayName(g.name, d.Name()),
			Group:  g.name,
			Status: HealthPending,
		}
	}

	selector.CheckSelectedConnectivity()
	selector.CheckSelectedConnectivity()

	if got := good.checks.Load(); got != 0 {
		t.Fatalf("healthy relay checks after internet outage = %d, want 0", got)
	}
	if got := selector.ActiveName(); got != "main / bad" {
		t.Fatalf("active relay after internet outage = %q, want bad", got)
	}
	if got := selector.selectedCheckFailures; got != 0 {
		t.Fatalf("selected check failures during internet outage = %d, want 0", got)
	}
	status := selector.ConnectivityStatus()
	if status.Domestic.Status != HealthDown {
		t.Fatalf("domestic connectivity status = %q, want %q", status.Domestic.Status, HealthDown)
	}
}

func TestSelectedCheckLoopChecksDomesticConnectivity(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	selector := &Selector{
		domesticURL:           target.URL,
		selectedCheckInterval: 10 * time.Millisecond,
		stopCh:                make(chan struct{}),
		selectedConfigCh:      make(chan struct{}, 1),
		bus:                   eventbus.New(),
	}

	done := make(chan struct{})
	go func() {
		selector.selectedCheckLoop()
		close(done)
	}()

	var status ConnectivityStatus
	deadline := time.After(2 * time.Second)
	for {
		status = selector.ConnectivityStatus()
		if !status.Domestic.LastCheckedAt.IsZero() {
			break
		}
		select {
		case <-deadline:
			selector.Stop()
			t.Fatal("domestic connectivity was not checked")
		case <-time.After(10 * time.Millisecond):
		}
	}

	selector.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("selected check loop did not stop")
	}
	if status.Domestic.URL != target.URL || status.Domestic.Status != HealthHealthy || status.Domestic.Latency <= 0 {
		t.Fatalf("domestic connectivity status = %#v", status.Domestic)
	}
	if status.CheckIntervalMS != 10 {
		t.Fatalf("connectivity interval = %d, want 10", status.CheckIntervalMS)
	}
	if len(status.Domestic.History) == 0 {
		t.Fatal("domestic connectivity history is empty")
	}
	if got := requests.Load(); got < 2 {
		t.Fatalf("domestic URL requests = %d, want at least 2", got)
	}
}

func TestBenchmarkPublishesCompletedRelayBeforeWholeBatchFinishes(t *testing.T) {
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	blocker := &blockingDialer{
		testDialer: testDialer{name: "second"},
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	selector := &Selector{
		health:     make(map[string]*RelayHealth),
		mode:       "auto",
		outsideURL: target.URL,
		store:      st,
		bus:        eventbus.New(),
		checkSem:   make(chan struct{}, 1),
	}
	g := &group{name: "main", mode: "auto"}
	for _, d := range []Dialer{
		&testDialer{name: "first"},
		blocker,
		&testDialer{name: "third"},
	} {
		g.dialers = append(g.dialers, d)
		selector.health[selector.healthKey(g.name, d.Name())] = &RelayHealth{
			Name:   selector.displayName(g.name, d.Name()),
			Group:  g.name,
			Status: HealthPending,
		}
	}
	selector.groups = []*group{g}

	done := make(chan struct{})
	go func() {
		selector.Benchmark()
		close(done)
	}()

	select {
	case <-blocker.started:
	case <-time.After(5 * time.Second):
		t.Fatal("second relay did not start checking")
	}

	checking := 0
	final := 0
	pending := 0
	for _, h := range selector.HealthList() {
		switch h.Status {
		case HealthChecking:
			checking++
		case HealthHealthy, HealthDegraded, HealthDown:
			final++
		case HealthPending:
			pending++
		default:
			t.Fatalf("unexpected relay status while batch is running: %#v", h)
		}
	}
	if checking != 1 {
		t.Fatalf("checking relays = %d, want 1", checking)
	}
	if final+pending == 0 {
		t.Fatalf("all relays are checking; final=%d pending=%d", final, pending)
	}

	close(blocker.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("benchmark timed out")
	}
}

func TestRelayHealthHistoryKeepsLatestTwentyCompletedChecks(t *testing.T) {
	h := &RelayHealth{}
	for i := 0; i < maxHealthRecords+2; i++ {
		h.LastCheckedAt = time.Unix(int64(i), 0)
		h.Status = HealthHealthy
		h.Latency = int64(i)
		appendRelayHealthRecord(h)
	}

	if len(h.History) != maxHealthRecords {
		t.Fatalf("history length = %d, want %d", len(h.History), maxHealthRecords)
	}
	first := h.History[0]
	if first.Time != time.Unix(2, 0) || first.Latency != 2 {
		t.Fatalf("oldest kept record = %#v, want check 2", first)
	}
	last := h.History[len(h.History)-1]
	wantLast := int64(maxHealthRecords + 1)
	if last.Time != time.Unix(wantLast, 0) || last.Latency != wantLast {
		t.Fatalf("newest kept record = %#v, want check %d", last, wantLast)
	}
}

func TestRelayURLLatencyUsesWarmedRoundTrip(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	selector := &Selector{outsideURL: target.URL}
	dialer := &slowFirstDialer{
		testDialer: testDialer{name: "slow-first-dial"},
		delay:      300 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	latency, err := selector.testRelayURL(ctx, dialer)
	if err != nil {
		t.Fatalf("testRelayURL() error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if got := dialer.dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
	if latency >= 250*time.Millisecond {
		t.Fatalf("latency = %v, want warmed request latency below initial dial delay", latency)
	}
}

type testDialer struct {
	name string
}

func (d *testDialer) Name() string     { return d.name }
func (d *testDialer) Type() string     { return "test" }
func (d *testDialer) Addr() string     { return "127.0.0.1:1" }
func (d *testDialer) SupportUDP() bool { return false }
func (d *testDialer) Close() error     { return nil }

func (d *testDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

type slowFirstDialer struct {
	testDialer
	delay time.Duration
	dials atomic.Int64
}

func (d *slowFirstDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.dials.Add(1) == 1 {
		timer := time.NewTimer(d.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// fixedTargetDialer always connects to its own target, letting a test route a
// relay's traffic to a dedicated (e.g. slow) server regardless of the address.
type fixedTargetDialer struct {
	testDialer
	target string
}

func (d *fixedTargetDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

type meteredDialer struct {
	testDialer
	active    *atomic.Int64
	maxActive *atomic.Int64
}

func (d *meteredDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	current := d.active.Add(1)
	for {
		max := d.maxActive.Load()
		if current <= max || d.maxActive.CompareAndSwap(max, current) {
			break
		}
	}
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		d.active.Add(-1)
		return nil, ctx.Err()
	}
	d.active.Add(-1)
	return d.testDialer.DialContext(ctx, network, address)
}

type countingDialer struct {
	testDialer
	checks atomic.Int64
}

func (d *countingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.checks.Add(1)
	return d.testDialer.DialContext(ctx, network, address)
}

type failingDialer struct {
	testDialer
	checks atomic.Int64
}

func (d *failingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.checks.Add(1)
	return nil, errors.New("relay unavailable")
}

type blockingDialer struct {
	testDialer
	started chan struct{}
	release chan struct{}
}

func (d *blockingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	close(d.started)
	select {
	case <-d.release:
		return d.testDialer.DialContext(ctx, network, address)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
