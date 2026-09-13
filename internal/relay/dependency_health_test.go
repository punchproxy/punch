package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

// memoryHealthDialer serves health responses over a pipe without network I/O.
type memoryHealthDialer struct {
	testDialer
	checks atomic.Int64
	err    error
}

func (d *memoryHealthDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.checks.Add(1)
	if d.err != nil {
		return nil, d.err
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		for {
			req, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			req.Body.Close()
			if _, err := fmt.Fprint(server, "HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n"); err != nil {
				return
			}
		}
	}()
	return client, nil
}

type healthDependencyDialer struct {
	testDialer
	dependency string
	checks     atomic.Int64
	beforeDial func(Dialer) error
}

func (d *healthDependencyDialer) DependencyGroup() string { return d.dependency }
func (d *healthDependencyDialer) BindDependency(upstream Dialer, _ string) Dialer {
	return &healthBoundDependency{healthDependencyDialer: d, upstream: upstream}
}
func (d *healthDependencyDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unbound dependency")
}

type healthBoundDependency struct {
	*healthDependencyDialer
	upstream Dialer
}

func (d *healthBoundDependency) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.checks.Add(1)
	if d.beforeDial != nil {
		if err := d.beforeDial(d.upstream); err != nil {
			return nil, err
		}
	}
	return d.upstream.DialContext(ctx, network, address)
}

func newDependencyHealthSelector(t *testing.T, groups ...*group) *Selector {
	t.Helper()
	store, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := &Selector{
		groups:              groups,
		health:              make(map[string]*RelayHealth),
		mode:                "manual",
		outsideURL:          "http://health.test/check",
		store:               store,
		bus:                 eventbus.New(),
		fullTriggerFailures: 1,
	}
	s.populateHealthLocked(nil)
	s.invalidateChangedPathsLocked()
	return s
}

func TestSelectedConnectivityChecksDependencyPath(t *testing.T) {
	up := &memoryHealthDialer{testDialer: testDialer{name: "up"}}
	spare := &memoryHealthDialer{testDialer: testDialer{name: "spare"}}
	unused := &memoryHealthDialer{testDialer: testDialer{name: "unused"}}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit"}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "manual", dialers: []Dialer{up, spare}},
		&group{name: "unused", mode: "manual", dialers: []Dialer{unused}},
	)
	s.CheckSelectedConnectivity()
	if exit.checks.Load() != 1 || up.checks.Load() != 2 || spare.checks.Load() != 0 || unused.checks.Load() != 0 {
		t.Fatalf("checks exit=%d upstream=%d spare=%d unused=%d", exit.checks.Load(), up.checks.Load(), spare.checks.Load(), unused.checks.Load())
	}
	status := s.ConnectivityStatus()
	if status.Outside.Status != HealthHealthy || len(status.Outside.Path) != 2 || status.Outside.Path[1].Group != "egress" {
		t.Fatalf("outside status = %#v", status.Outside)
	}
	if h := s.health[s.healthKey("transit", "up")]; h.LastCheckedAt.IsZero() || h.Status != HealthHealthy {
		t.Fatalf("upstream health = %#v", h)
	}
}

func TestBenchmarkSelectsUpstreamBeforeDependentChecks(t *testing.T) {
	bad := &memoryHealthDialer{testDialer: testDialer{name: "bad"}, err: errors.New("unreachable")}
	good := &memoryHealthDialer{testDialer: testDialer{name: "good"}}
	var wrongUpstream atomic.Bool
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit", beforeDial: func(up Dialer) error {
		if up.Name() != "good" {
			wrongUpstream.Store(true)
		}
		return nil
	}}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "auto", dialers: []Dialer{bad, good}},
	)
	s.Benchmark()
	if wrongUpstream.Load() || exit.checks.Load() != 1 || s.groups[1].active.Load() != 1 {
		t.Fatalf("dependent checked before upstream selection: wrong=%v checks=%d selected=%d", wrongUpstream.Load(), exit.checks.Load(), s.groups[1].active.Load())
	}
	if h := s.health[s.healthKey("egress", "exit")]; h.Status != HealthHealthy || h.Path[0].Relay != "good" {
		t.Fatalf("exit health = %#v", h)
	}
}

func TestDependencyProbeResultRejectedAfterUpstreamSwitch(t *testing.T) {
	first := &memoryHealthDialer{testDialer: testDialer{name: "first"}}
	second := &memoryHealthDialer{testDialer: testDialer{name: "second"}}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit"}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "manual", dialers: []Dialer{first, second}},
	)
	target, _ := s.selectedBenchmarkTarget()
	result := s.testRelay(target.dialer)
	s.mu.Lock()
	s.groups[1].active.Store(1)
	s.invalidateChangedPathsLocked()
	s.mu.Unlock()
	s.finishRelayCheck(target, result)
	h := s.health[s.healthKey("egress", "exit")]
	if h.Status != HealthPending || !h.LastCheckedAt.IsZero() || h.Path[0].Relay != "second" {
		t.Fatalf("stale probe changed current health: %#v", h)
	}
	if failures, trigger := s.recordSelectedCheckResult(target, true); failures != 0 || trigger {
		t.Fatalf("stale path counted as failure: %d, %v", failures, trigger)
	}
}

func TestDependencyRecoveryUsesExitPathAndKeepsManualGroups(t *testing.T) {
	for _, mode := range []string{"auto", "manual"} {
		t.Run(mode, func(t *testing.T) {
			bad := &memoryHealthDialer{testDialer: testDialer{name: "bad"}}
			good := &memoryHealthDialer{testDialer: testDialer{name: "good"}}
			exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit", beforeDial: func(up Dialer) error {
				if up.Name() == "bad" {
					return errors.New("upstream cannot reach exit server")
				}
				return nil
			}}
			s := newDependencyHealthSelector(t,
				&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
				&group{name: "transit", mode: mode, dialers: []Dialer{bad, good}},
			)
			// Both upstreams can reach the ordinary URL. The faster one cannot
			// reach this exit, so its standalone latency must not undo recovery.
			for i, name := range []string{"bad", "good"} {
				h := s.health[s.healthKey("transit", name)]
				h.Status, h.URLTestLatency = HealthHealthy, int64(10+i*100)
			}
			s.CheckSelectedConnectivity()
			if mode == "manual" {
				if s.groups[1].active.Load() != 0 || s.health[s.healthKey("egress", "exit")].Status != HealthDown {
					t.Fatal("recovery changed manual upstream selection")
				}
				return
			}
			if s.groups[1].active.Load() != 1 || s.health[s.healthKey("egress", "exit")].Status != HealthHealthy {
				t.Fatalf("exit path did not recover: selected=%d health=%#v", s.groups[1].active.Load(), s.health[s.healthKey("egress", "exit")])
			}
			s.health[s.healthKey("transit", "bad")].URLTestLatency = 1
			s.health[s.healthKey("transit", "good")].URLTestLatency = 100
			s.reevaluateAutoSelections()
			if s.groups[1].active.Load() != 1 {
				t.Fatal("standalone URL latency undid successful exit-path recovery")
			}
		})
	}
}

func TestDependencyFailureCooldownExpires(t *testing.T) {
	up := &memoryHealthDialer{testDialer: testDialer{name: "up"}}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit"}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "auto", dialers: []Dialer{up}},
	)
	target, _ := s.selectedBenchmarkTarget()
	s.mu.Lock()
	s.rememberDependencyFailureLocked(target.pathKey)
	if s.dependencyCandidateAllowedLocked(s.groups[1], 0) {
		t.Fatal("failed path immediately became eligible")
	}
	s.dependencyFailures[target.pathKey] = time.Now().Add(-time.Second)
	allowed := s.dependencyCandidateAllowedLocked(s.groups[1], 0)
	s.mu.Unlock()
	if !allowed {
		t.Fatal("failed path was not retried after cooldown")
	}
}

func TestUpstreamLatencySwitchConfirmsCompleteLivePath(t *testing.T) {
	for _, tc := range []struct {
		name           string
		candidateFails bool
		currentLatency int64
		wantIndex      int32
	}{
		{name: "failed exit path", candidateFails: true, currentLatency: 200},
		{name: "gain within tolerance", currentLatency: 20},
		{name: "confirmed faster path", currentLatency: 200, wantIndex: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := &memoryHealthDialer{testDialer: testDialer{name: "current"}}
			candidate := &memoryHealthDialer{testDialer: testDialer{name: "candidate"}}
			exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit", beforeDial: func(up Dialer) error {
				if tc.candidateFails && up.Name() == "candidate" {
					return errors.New("candidate cannot reach exit")
				}
				return nil
			}}
			s := newDependencyHealthSelector(t,
				&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
				&group{name: "transit", mode: "auto", dialers: []Dialer{current, candidate}},
			)
			s.tolerance = 50 * time.Millisecond
			h := s.health[s.healthKey("egress", "exit")]
			h.Status, h.URLTestLatency = HealthHealthy, tc.currentLatency
			for i, name := range []string{"current", "candidate"} {
				h := s.health[s.healthKey("transit", name)]
				h.Status, h.URLTestLatency = HealthHealthy, int64(200-i*190)
			}
			s.reevaluateGroupSelections([]benchmarkTarget{{group: s.groups[1], index: 1, dialer: candidate}})
			if s.groups[1].active.Load() != 0 {
				t.Fatal("benchmark level changed a healthy live path before confirmation")
			}
			s.reevaluateAutoSelections()
			if got := s.groups[1].active.Load(); got != tc.wantIndex {
				t.Fatalf("selected upstream = %d, want %d", got, tc.wantIndex)
			}
			if exit.checks.Load() != 1 {
				t.Fatalf("complete path confirmation checks = %d, want 1", exit.checks.Load())
			}
			if h.Status != HealthHealthy || h.Path[0].Relay != s.groups[1].dialers[tc.wantIndex].Name() {
				t.Fatalf("root health attributed to wrong path: %#v", h)
			}
		})
	}
}

func TestFullBenchmarkRepairsPreviouslyHealthyPathBeforeDirectFallback(t *testing.T) {
	bad := &memoryHealthDialer{testDialer: testDialer{name: "bad"}, err: errors.New("upstream went down")}
	good := &memoryHealthDialer{testDialer: testDialer{name: "good"}}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit"}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "auto", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "auto", dialers: []Dialer{bad, good}},
		&group{name: directGroupName, mode: "manual", dialers: []Dialer{NewDirectDialer(nil)}},
	)
	s.mode = "auto"
	transitOnly := false
	s.groupCfgs = map[string]config.RelayGroup{"transit": {ExitEligible: &transitOnly}}
	for _, h := range s.health {
		h.Status, h.URLTestLatency = HealthHealthy, 50
	}
	s.Benchmark()
	if s.ActiveName() != "egress / exit" || s.groups[1].active.Load() != 1 {
		t.Fatalf("full benchmark abandoned recoverable chain: active=%q upstream=%d", s.ActiveName(), s.groups[1].active.Load())
	}
	if h := s.health[s.healthKey("egress", "exit")]; h.Status != HealthHealthy || h.Path[0].Relay != "good" {
		t.Fatalf("recovered path health = %#v", h)
	}
}

func TestObsoleteUpstreamCheckOnlyUpdatesOutsideHistory(t *testing.T) {
	first := &memoryHealthDialer{testDialer: testDialer{name: "first"}}
	second := &memoryHealthDialer{testDialer: testDialer{name: "second"}}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit"}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "manual", dialers: []Dialer{first, second}},
	)
	old, _ := s.selectedBenchmarkTarget()
	s.applyOutsideConnectivityCheckResult(old, relayCheckResult{urlLatency: 20 * time.Millisecond})
	s.mu.Lock()
	s.groups[1].active.Store(1)
	s.invalidateChangedPathsLocked()
	s.mu.Unlock()
	if status := s.ConnectivityStatus(); status.Outside.Status != HealthPending || status.Outside.Path[0].Relay != "second" {
		t.Fatalf("obsolete upstream result still shown as current: %#v", status.Outside)
	}
	current, _ := s.selectedBenchmarkTarget()
	s.applyOutsideConnectivityCheckResult(current, relayCheckResult{urlLatency: 30 * time.Millisecond})
	s.applyOutsideConnectivityCheckResult(old, relayCheckResult{err: errors.New("old upstream failed")})
	status := s.ConnectivityStatus()
	if status.Outside.Status != HealthHealthy || status.Outside.Latency != 30 || status.Outside.Path[0].Relay != "second" {
		t.Fatalf("old check overwrote current result: %#v", status.Outside)
	}
	if len(status.Outside.History) != 3 || status.Outside.History[2].Path[0].Relay != "first" {
		t.Fatalf("obsolete check missing from history: %#v", status.Outside.History)
	}
}

func TestDependencyRecoveryLimitsCandidateChecks(t *testing.T) {
	var upstreams []Dialer
	for i := 0; i < 20; i++ {
		upstreams = append(upstreams, &memoryHealthDialer{testDialer: testDialer{name: fmt.Sprintf("up-%d", i)}})
	}
	exit := &healthDependencyDialer{testDialer: testDialer{name: "exit"}, dependency: "transit", beforeDial: func(Dialer) error {
		return errors.New("exit unreachable")
	}}
	s := newDependencyHealthSelector(t,
		&group{name: "egress", mode: "manual", dialers: []Dialer{exit}},
		&group{name: "transit", mode: "auto", dialers: upstreams},
	)
	target, _ := s.selectedBenchmarkTarget()
	for round := 1; round <= 2; round++ {
		if s.recoverSelectedDependencies(target) {
			t.Fatal("unreachable exit unexpectedly recovered")
		}
		if got := exit.checks.Load(); got != int64(round*dependencyRecoveryCandidates) {
			t.Fatalf("round %d total candidate checks = %d, want %d", round, got, round*dependencyRecoveryCandidates)
		}
	}
}
