package relay

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
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
			OutsideURL: "http://www.gstatic.com/generate_204",
			Interval:   300,
			Tolerance:  50,
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

func TestRemoteRelayGroupUsesProviderResolvers(t *testing.T) {
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
		resolveRelayDomain: func(context.Context, string, string, []config.Upstream) ([]netip.Addr, time.Time, error) {
			return nil, time.Time{}, nil
		},
	}
	group, err := selector.buildGroup(config.RelayGroup{
		Type: "remote",
		Name: "main",
		URL:  providerPath,
		RelayDomainResolver: []config.Upstream{{
			URL: "https://fallback.example/dns-query",
		}},
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
	if len(dialer.upstreams) != 1 || dialer.upstreams[0].URL != "https://dns.example/dns-query" {
		t.Fatalf("upstreams = %#v", dialer.upstreams)
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
	if health.Status != HealthHealthy || health.TCPConnectLatency == 0 || health.URLTestLatency == 0 {
		t.Fatalf("preferred health = %#v", health)
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
	checkCfg := config.Check{OutsideURL: "http://www.gstatic.com/generate_204", Interval: 300, Tolerance: 50}
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
	checkCfg := config.Check{OutsideURL: "http://www.gstatic.com/generate_204", Interval: 300, Tolerance: 50}
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
		health:           make(map[string]*RelayHealth),
		mode:             "manual",
		outsideURL:       target.URL,
		selectedInterval: 10 * time.Millisecond,
		store:            st,
		bus:              eventbus.New(),
		stopCh:           make(chan struct{}),
		selectedConfigCh: make(chan struct{}, 1),
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

func TestSelectedCheckLoopChecksDomesticConnectivity(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	selector := &Selector{
		domesticURL:      target.URL,
		selectedInterval: 10 * time.Millisecond,
		stopCh:           make(chan struct{}),
		selectedConfigCh: make(chan struct{}, 1),
		bus:              eventbus.New(),
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
	if status.Domestic.URL != target.URL || status.Domestic.Status != HealthHealthy || status.Domestic.Latency <= 0 || status.Domestic.TCPConnectLatency <= 0 {
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

	blocker := &blockingTCPDialer{
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
		h.TCPConnectLatency = int64(i + 100)
		appendRelayHealthRecord(h)
	}

	if len(h.History) != maxHealthRecords {
		t.Fatalf("history length = %d, want %d", len(h.History), maxHealthRecords)
	}
	first := h.History[0]
	if first.Time != time.Unix(2, 0) || first.Latency != 2 || first.TCPConnectLatency != 102 {
		t.Fatalf("oldest kept record = %#v, want check 2", first)
	}
	last := h.History[len(h.History)-1]
	wantLast := int64(maxHealthRecords + 1)
	if last.Time != time.Unix(wantLast, 0) || last.Latency != wantLast || last.TCPConnectLatency != wantLast+100 {
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

func (d *testDialer) TCPConnectLatency(ctx context.Context) (time.Duration, error) {
	return time.Millisecond, nil
}

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

type meteredDialer struct {
	testDialer
	active    *atomic.Int64
	maxActive *atomic.Int64
}

func (d *meteredDialer) TCPConnectLatency(ctx context.Context) (time.Duration, error) {
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
		return 0, ctx.Err()
	}
	d.active.Add(-1)
	return time.Millisecond, nil
}

type countingDialer struct {
	testDialer
	checks atomic.Int64
}

func (d *countingDialer) TCPConnectLatency(ctx context.Context) (time.Duration, error) {
	d.checks.Add(1)
	return time.Millisecond, nil
}

type blockingTCPDialer struct {
	testDialer
	started chan struct{}
	release chan struct{}
}

func (d *blockingTCPDialer) TCPConnectLatency(ctx context.Context) (time.Duration, error) {
	close(d.started)
	select {
	case <-d.release:
		return time.Millisecond, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
