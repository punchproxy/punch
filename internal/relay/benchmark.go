package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) Benchmark() {
	// A running benchmark already covers recovery for all groups. Avoid
	// starting another one when several callers observe the same failure.
	if !s.fullBenchmarkMu.TryLock() {
		return
	}
	defer s.fullBenchmarkMu.Unlock()
	s.mu.RLock()
	mode := s.mode
	outsideURL := s.outsideURL
	groups := make([]benchmarkGroup, 0, len(s.groups))
	var targets []benchmarkTarget
	for _, g := range s.groups {
		groups = append(groups, benchmarkGroup{
			group:   g,
			dialers: append([]Dialer(nil), g.dialers...),
		})
		if g.name == directGroupName {
			continue
		}
		for di, d := range g.dialers {
			targets = append(targets, benchmarkTarget{group: g, index: di, dialer: d})
		}
	}
	s.mu.RUnlock()
	prevActive := s.ActiveName()

	slog.Debug("start relay benchmark", "mode", mode, "groups", len(groups), "url", outsideURL)

	results := s.runDependencyChecks(targets)

	s.mu.Lock()
	for _, result := range results {
		h := s.health[s.healthKey(result.target.group.name, result.target.dialer.Name())]
		if h == nil {
			continue
		}
		if result.check.err != nil {
			slog.Debug("relay health check failed", "group", result.target.group.name, "relay", result.target.dialer.Name(), "error", result.check.err)
		} else {
			slog.Debug("relay health check result", "group", result.target.group.name, "relay", result.target.dialer.Name(), "url_test_latency_ms", h.URLTestLatency, "status", h.Status)
		}
	}
	s.resetSelectedCheckFailuresLocked()
	s.mu.Unlock()

	s.reevaluateAutoSelections()

	slog.Debug("relay benchmark completed", "active", s.ActiveName())
	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
}

func (s *Selector) BenchmarkTarget(name string) error {
	s.mu.RLock()
	var targets []benchmarkTarget
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		if g.name == name {
			if g.name == directGroupName {
				s.mu.RUnlock()
				return nil
			}
			for di, d := range g.dialers {
				targets = append(targets, benchmarkTarget{group: g, index: di, dialer: d})
			}
			break
		}
		for di, d := range g.dialers {
			if s.displayName(g.name, d.Name()) == name || d.Name() == name {
				targets = append(targets, benchmarkTarget{group: g, index: di, dialer: d})
				break
			}
		}
		if len(targets) > 0 {
			break
		}
	}
	s.mu.RUnlock()
	if len(targets) == 0 {
		return fmt.Errorf("relay %q not found", name)
	}
	return s.benchmarkTargets(targets)
}

// triggerFullBenchmarkAfterSelectedCheck is the fail-over path: once the
// selected relay accumulates enough consecutive check failures, a full relay
// benchmark re-tests every relay and reevaluation switches to the best one.
func (s *Selector) triggerFullBenchmarkAfterSelectedCheck(target benchmarkTarget, failed bool, internetDown bool) {
	if failed && internetDown {
		slog.Debug("selected relay check failed while internet check is down; skipping failure count", "group", target.group.name, "relay", target.dialer.Name())
		return
	}
	failures, trigger := s.recordSelectedCheckResult(target, failed)
	if !trigger {
		return
	}
	slog.Warn("selected relay failed repeatedly; running full relay benchmark to fail over", "group", target.group.name, "relay", target.dialer.Name(), "failures", failures)
	if s.recoverSelectedDependencies(target) {
		return
	}
	s.Benchmark()
}

func (s *Selector) recordSelectedCheckResult(target benchmarkTarget, failed bool) (int, bool) {
	key := s.healthKey(target.group.name, target.dialer.Name())
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.benchmarkTargetCurrentLocked(target) {
		return 0, false
	}
	if target.pathKey != "" {
		_, activeKey := s.activePathLocked()
		if activeKey != target.pathKey {
			return 0, false
		}
		key = target.pathKey
	}
	if !failed {
		s.resetSelectedCheckFailuresLocked()
		return 0, false
	}
	if s.selectedCheckFailureKey != key {
		s.selectedCheckFailureKey = key
		s.selectedCheckFailures = 0
	}
	s.selectedCheckFailures++
	failures := s.selectedCheckFailures
	threshold := normalizeFullTriggerFailures(s.fullTriggerFailures)
	if failures < threshold {
		return failures, false
	}
	s.selectedCheckFailures = 0
	s.selectedCheckFailureKey = ""
	return failures, true
}

func (s *Selector) HasBenchmarkTarget(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		if g.name == name {
			return true
		}
		for _, d := range g.dialers {
			if s.displayName(g.name, d.Name()) == name || d.Name() == name {
				return true
			}
		}
	}
	return false
}

func (s *Selector) BenchmarkRelay(name, groupName string) (string, error) {
	resolved, targets, err := s.lookupBenchmarkRelay(name, groupName)
	if err != nil {
		return "", err
	}
	if err := s.benchmarkTargets(targets); err != nil {
		return "", err
	}
	return resolved, nil
}

func (s *Selector) ResolveBenchmarkRelay(name, groupName string) (string, error) {
	resolved, _, err := s.lookupBenchmarkRelay(name, groupName)
	return resolved, err
}

func (s *Selector) BenchmarkRelayAsync(name, groupName string) (string, error) {
	resolved, targets, err := s.lookupBenchmarkRelay(name, groupName)
	if err != nil {
		return "", err
	}
	go func() {
		if err := s.benchmarkTargets(targets); err != nil {
			slog.Warn("relay benchmark failed", "relay", resolved, "error", err)
		}
	}()
	return resolved, nil
}

func (s *Selector) lookupBenchmarkRelay(name, groupName string) (string, []benchmarkTarget, error) {
	s.mu.RLock()
	var targets []benchmarkTarget
	for _, g := range s.groups {
		if groupName != "" && g.name != groupName {
			continue
		}
		if g.name == directGroupName {
			continue
		}
		for di, d := range g.dialers {
			if d.Name() == name || s.displayName(g.name, d.Name()) == name {
				targets = append(targets, benchmarkTarget{group: g, index: di, dialer: d})
			}
		}
	}
	s.mu.RUnlock()
	if len(targets) == 0 {
		if groupName != "" {
			return "", nil, fmt.Errorf("relay %q in group %q not found", name, groupName)
		}
		return "", nil, fmt.Errorf("relay %q not found", name)
	}
	if len(targets) > 1 {
		names := make([]string, 0, len(targets))
		for _, target := range targets {
			names = append(names, s.displayName(target.group.name, target.dialer.Name()))
		}
		return "", nil, fmt.Errorf("%w: %s", ErrRelaySelectionAmbiguous, strings.Join(names, ", "))
	}
	target := targets[0]
	return s.displayName(target.group.name, target.dialer.Name()), targets, nil
}

func (s *Selector) selectedBenchmarkTarget() (benchmarkTarget, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.groups) == 0 {
		return benchmarkTarget{}, false
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	if g.name == directGroupName || len(g.dialers) == 0 {
		return benchmarkTarget{}, false
	}
	idx := s.activeDialerIndexLocked(g)
	return s.snapshotBenchmarkTargetLocked(benchmarkTarget{group: g, index: idx, dialer: g.dialers[idx]}), true
}

func (s *Selector) benchmarkTargets(targets []benchmarkTarget) error {
	_, err := s.benchmarkTargetsWithResults(targets)
	return err
}

func (s *Selector) benchmarkTargetsWithResults(targets []benchmarkTarget) ([]benchmarkTargetResult, error) {
	s.fullBenchmarkMu.Lock()
	defer s.fullBenchmarkMu.Unlock()
	prevActive := s.ActiveName()

	results := s.runDependencyChecks(targets)

	// Auto-group selection over the fresh health results (including picking the
	// best relay after a whole-group benchmark) happens in reevaluation.
	s.reevaluateAutoSelections()

	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	return results, nil
}

type benchmarkTargetResult struct {
	target benchmarkTarget
	check  relayCheckResult
}

func (s *Selector) runDependencyChecks(targets []benchmarkTarget) []benchmarkTargetResult {
	var results []benchmarkTargetResult
	for _, level := range s.dependencyLevels(targets) {
		results = append(results, s.runRelayChecks(level)...)
		// Settle upstream choices before testing their consumers. A failed
		// standalone URL check does not prevent the consumer's actual dial.
		s.reevaluateGroupSelections(level)
	}
	// The root may have looked healthy when its dependencies were checked,
	// causing their choices to be held for confirmation. If its subsequent
	// check failed, repair that chain before global selection can choose DIRECT.
	s.mu.RLock()
	_, activeKey := s.activePathLocked()
	s.mu.RUnlock()
	for _, result := range results {
		if result.check.err != nil && result.target.pathKey == activeKey && len(result.target.path) > 1 {
			s.recoverSelectedDependenciesDuringBenchmark(result.target)
			break
		}
	}
	return results
}

func (s *Selector) reevaluateGroupSelections(targets []benchmarkTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make(map[string]bool, len(targets))
	for _, target := range targets {
		names[target.group.name] = true
	}
	activeGroup := s.activeUsableGroupIndexLocked()
	livePath := make(map[string]bool)
	if h := s.health[s.activeHealthKeyLocked()]; h != nil && h.Status != HealthDown && h.URLTestLatency > 0 {
		path, _ := s.activePathLocked()
		for _, hop := range path {
			livePath[hop.Group] = true
		}
	}
	changed := false
	for gi, g := range s.groups {
		// Leave the live root's member to the existing final confirmation
		// check before a latency-driven switch.
		if gi == activeGroup || livePath[g.name] || !names[g.name] || g.mode != "auto" || len(g.dialers) == 0 {
			continue
		}
		cur := s.activeDialerIndexLocked(g)
		best := cur
		bestLatency, currentUsable := s.usableLatencyLocked(g.name, g.dialers[cur].Name())
		currentUsable = currentUsable && s.dependencyCandidateAllowedLocked(g, cur)
		found := currentUsable
		for di, d := range g.dialers {
			if !s.dependencyCandidateAllowedLocked(g, di) {
				continue
			}
			latency, ok := s.usableLatencyLocked(g.name, d.Name())
			if ok && (!found || latency < bestLatency) {
				best, bestLatency, found = di, latency, true
			}
		}
		if !found || best == cur {
			continue
		}
		if latency, ok := s.usableLatencyLocked(g.name, g.dialers[cur].Name()); ok && currentUsable && latency-bestLatency <= s.tolerance {
			continue
		}
		g.active.Store(int32(best))
		changed = true
	}
	if changed {
		s.invalidateChangedPathsLocked()
		s.saveSelectionsLocked()
	}
}

func (s *Selector) runRelayChecks(targets []benchmarkTarget) []benchmarkTargetResult {
	if len(targets) == 0 {
		return nil
	}
	snapshots := make([]benchmarkTarget, len(targets))
	s.mu.RLock()
	for i, target := range targets {
		snapshots[i] = s.snapshotBenchmarkTargetLocked(target)
	}
	s.mu.RUnlock()
	targets = snapshots
	s.setRelayCheckStatus(targets, HealthPending)
	results := make([]benchmarkTargetResult, len(targets))
	sem := s.relayCheckSemaphore()
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(idx int, t benchmarkTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.setRelayCheckStatus([]benchmarkTarget{t}, HealthChecking)
			check := s.testRelay(t.dialer)
			s.finishRelayCheck(t, check)
			results[idx] = benchmarkTargetResult{target: t, check: check}
		}(i, target)
	}
	wg.Wait()
	return results
}

func (s *Selector) finishRelayCheck(target benchmarkTarget, result relayCheckResult) {
	s.mu.Lock()
	changed := false
	if h := s.health[s.healthKey(target.group.name, target.dialer.Name())]; h != nil && s.benchmarkTargetCurrentLocked(target) {
		s.applyRelayCheckResultLocked(h, result)
		h.pathKey = target.pathKey
		h.Path = append([]RelayHop(nil), target.path...)
		appendRelayHealthRecord(h)
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	}
}

func (s *Selector) relayCheckSemaphore() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkSem == nil {
		s.checkSem = make(chan struct{}, defaultCheckConcurrency)
	}
	return s.checkSem
}

func normalizeCheckConcurrency(n int) int {
	if n <= 0 {
		return defaultCheckConcurrency
	}
	return n
}

func normalizeFullCheckInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultFullCheckInterval
	}
	return time.Duration(seconds) * time.Second
}

func normalizeSelectedCheckInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultSelectedCheckInterval
	}
	return time.Duration(seconds) * time.Second
}

func normalizeFullTriggerFailures(n int) int {
	if n <= 0 {
		return defaultFullTriggerFailures
	}
	return n
}

func (s *Selector) setRelayCheckStatus(targets []benchmarkTarget, status HealthStatus) {
	s.mu.Lock()
	changed := false
	for _, target := range targets {
		if h := s.health[s.healthKey(target.group.name, target.dialer.Name())]; h != nil && s.benchmarkTargetCurrentLocked(target) {
			h.Status = status
			h.Error = ""
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	}
}

type benchmarkTarget struct {
	group   *group
	index   int
	dialer  Dialer
	pathKey string
	path    []RelayHop
}

type benchmarkGroup struct {
	group   *group
	dialers []Dialer
}

type relayCheckResult struct {
	urlLatency time.Duration
	err        error
}

func (s *Selector) testRelay(d Dialer) relayCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	urlLatency, err := s.testRelayURL(ctx, d)
	if err != nil {
		return relayCheckResult{err: err}
	}
	return relayCheckResult{urlLatency: urlLatency}
}

func (s *Selector) testRelayURL(ctx context.Context, d Dialer) (time.Duration, error) {
	s.mu.RLock()
	url := s.outsideURL
	s.mu.RUnlock()
	return testURLLatency(ctx, url, d.DialContext)
}

func testURLConnectivity(rawURL string, dialContext DialContextFunc) relayCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	urlLatency, err := testURLLatency(ctx, rawURL, dialContext)
	if err != nil {
		return relayCheckResult{err: err}
	}
	return relayCheckResult{urlLatency: urlLatency}
}

func testURLLatency(ctx context.Context, rawURL string, dialContext DialContextFunc) (time.Duration, error) {
	if _, err := parseCheckURL(rawURL); err != nil {
		return 0, err
	}
	transport := &http.Transport{}
	if dialContext != nil {
		transport.DialContext = dialContext
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	latency, err := testURLLatencyOnce(ctx, client, rawURL)
	if err != nil {
		return 0, err
	}
	if secondLatency, err := testURLLatencyOnce(ctx, client, rawURL); err == nil {
		return secondLatency, nil
	}
	return latency, nil
}

func parseCheckURL(rawURL string) (*url.URL, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse test url: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("unsupported test url scheme %q", target.Scheme)
	}
	if target.Hostname() == "" {
		return nil, fmt.Errorf("test url missing host")
	}
	return target, nil
}

func testURLLatencyOnce(ctx context.Context, client *http.Client, rawURL string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("test url round trip: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return 0, fmt.Errorf("test url returned HTTP %d", resp.StatusCode)
	}
	latency := time.Since(start)
	if latency <= 0 {
		latency = time.Nanosecond
	}
	return latency, nil
}

func (s *Selector) applyRelayCheckResultLocked(h *RelayHealth, result relayCheckResult) {
	h.LastCheckedAt = time.Now()
	h.URLTestLatency = durationMillis(result.urlLatency)
	if result.err != nil {
		h.Status = HealthDown
		h.Latency = 0
		h.Error = result.err.Error()
		return
	}
	h.Latency = h.URLTestLatency
	h.Error = ""
	if result.urlLatency > 500*time.Millisecond {
		h.Status = HealthDegraded
	} else {
		h.Status = HealthHealthy
	}
}

func appendRelayHealthRecord(h *RelayHealth) {
	h.History = append(h.History, HealthRecord{
		Time:    h.LastCheckedAt,
		Status:  h.Status,
		Latency: h.Latency,
		Path:    append([]RelayHop(nil), h.Path...),
	})
	if len(h.History) > maxHealthRecords {
		h.History = h.History[len(h.History)-maxHealthRecords:]
	}
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms == 0 {
		return 1
	}
	return ms
}

// autoSelections is a plan of what automatic selection would pick: the active
// dialer index per group and the selector-level active group index.
type autoSelections struct {
	groupActive map[*group]int
	activeGroup int
}

// computeAutoSelectionsLocked plans automatic selection from current health
// data without mutating any state. A live selection is only displaced when a
// candidate beats it by more than the configured switch tolerance; fail-overs
// away from dead relays ignore the tolerance.
func (s *Selector) computeAutoSelectionsLocked() autoSelections {
	sel := autoSelections{groupActive: make(map[*group]int, len(s.groups)), activeGroup: s.activeGroupIndexLocked()}
	for _, g := range s.groups {
		cur := s.activeDialerIndexLocked(g)
		sel.groupActive[g] = cur
		if g.mode != "auto" || len(g.dialers) == 0 {
			continue
		}
		bestIdx := -1
		bestLatency := time.Duration(1<<63 - 1)
		for di, d := range g.dialers {
			if !s.dependencyCandidateAllowedLocked(g, di) {
				continue
			}
			latency, ok := s.usableLatencyLocked(g.name, d.Name())
			if !ok {
				continue
			}
			if latency < bestLatency {
				bestLatency = latency
				bestIdx = di
			}
		}
		if bestIdx < 0 || bestIdx == cur {
			continue
		}
		if curLatency, ok := s.usableLatencyLocked(g.name, g.dialers[cur].Name()); ok && s.dependencyCandidateAllowedLocked(g, cur) && curLatency-bestLatency <= s.tolerance {
			continue
		}
		sel.groupActive[g] = bestIdx
	}

	if s.mode != "auto" {
		return sel
	}
	bestGroupIdx := -1
	bestGroupLatency := time.Duration(1<<63 - 1)
	directGroupIdx := -1
	for gi, g := range s.groups {
		if g.name == directGroupName {
			directGroupIdx = gi
			continue
		}
		if !s.exitEligibleLocked(g) {
			continue
		}
		if len(g.dialers) == 0 {
			continue
		}
		latency, ok := s.usableLatencyLocked(g.name, g.dialers[sel.groupActive[g]].Name())
		if !ok {
			continue
		}
		if latency < bestGroupLatency {
			bestGroupLatency = latency
			bestGroupIdx = gi
		}
	}
	if bestGroupIdx < 0 {
		bestGroupIdx = directGroupIdx
	}
	if bestGroupIdx < 0 || bestGroupIdx == sel.activeGroup {
		return sel
	}
	if cg := s.groups[sel.activeGroup]; cg.name != directGroupName && len(cg.dialers) > 0 {
		if curLatency, ok := s.usableLatencyLocked(cg.name, cg.dialers[sel.groupActive[cg]].Name()); ok && curLatency-bestGroupLatency <= s.tolerance {
			return sel
		}
	}
	sel.activeGroup = bestGroupIdx
	return sel
}

// usableLatencyLocked returns the relay's last URL test latency when the relay
// is a viable selection target (checked, not down).
func (s *Selector) usableLatencyLocked(groupName, relayName string) (time.Duration, bool) {
	h := s.health[s.healthKey(groupName, relayName)]
	if h == nil || h.Status == HealthDown || h.URLTestLatency <= 0 {
		return 0, false
	}
	return time.Duration(h.URLTestLatency) * time.Millisecond, true
}

func (s *Selector) applyAutoSelectionsLocked(sel autoSelections) {
	prevName := s.activeNameLocked()
	prevKey := s.activeHealthKeyLocked()
	for g, idx := range sel.groupActive {
		g.active.Store(int32(idx))
	}
	if len(s.groups) > 0 {
		s.active.Store(int32(sel.activeGroup))
	}
	s.invalidateChangedPathsLocked()
	s.reportAutoRelaySwitchLocked(prevName, prevKey)
}

// pendingLatencySwitchLocked snapshots the complete proposed path, including
// upstream-only changes, when switching away from a live path needs confirmation.
func (s *Selector) pendingLatencySwitchLocked(sel autoSelections) (benchmarkTarget, bool) {
	if len(s.groups) == 0 {
		return benchmarkTarget{}, false
	}
	gi := sel.activeGroup
	if gi < 0 || gi >= len(s.groups) || len(s.groups[gi].dialers) == 0 {
		gi = 0
		for i, g := range s.groups {
			if len(g.dialers) > 0 {
				gi = i
				break
			}
		}
	}
	g := s.groups[gi]
	if g.name == directGroupName || len(g.dialers) == 0 {
		return benchmarkTarget{}, false
	}
	idx := sel.groupActive[g]
	if idx < 0 || idx >= len(g.dialers) {
		idx = 0
	}
	d := g.dialers[idx]
	prevKey := s.activeHealthKeyLocked()
	choices := make(map[string]int, len(sel.groupActive))
	for candidateGroup, choice := range sel.groupActive {
		choices[candidateGroup.name] = choice
	}
	_, _, nextPathKey := s.resolvePathWithChoicesLocked(g, d, make(map[string]bool), false, choices)
	_, currentPathKey := s.activePathLocked()
	if nextPathKey == currentPathKey {
		return benchmarkTarget{}, false
	}
	if h := s.health[prevKey]; prevKey == "" || h == nil || h.Status == HealthDown {
		return benchmarkTarget{}, false
	}
	return s.snapshotBenchmarkTargetWithOverridesLocked(benchmarkTarget{group: g, index: idx, dialer: d}, choices), true
}

// holdActiveSelectionLocked rewrites sel so the currently active relay stays
// selected, while leaving decisions for other groups intact.
func (s *Selector) holdActiveSelectionLocked(sel *autoSelections) {
	sel.activeGroup = s.activeGroupIndexLocked()
	g := s.groups[s.activeUsableGroupIndexLocked()]
	sel.groupActive[g] = s.activeDialerIndexLocked(g)
	path, _ := s.activePathLocked()
	for _, hop := range path {
		if dependency := s.groupByNameLocked(hop.Group); dependency != nil {
			sel.groupActive[dependency] = s.activeDialerIndexLocked(dependency)
		}
	}
}

// reevaluateAutoSelections recomputes automatic selections from current health
// and persists the outcome. A latency-driven switch away from a live relay is
// first confirmed with a fresh check of the destination relay; the switch only
// happens when the fresh latency still beats the current relay by more than
// the switch tolerance.
func (s *Selector) reevaluateAutoSelections() {
	s.mu.Lock()
	sel := s.computeAutoSelectionsLocked()
	candidate, confirm := s.pendingLatencySwitchLocked(sel)
	if !confirm {
		s.applyAutoSelectionsLocked(sel)
		s.saveSelectionsLocked()
		s.mu.Unlock()
		return
	}
	currentName := s.activeNameLocked()
	_, currentPathKey := s.activePathLocked()
	candidateWasCurrent := s.benchmarkTargetCurrentLocked(candidate)
	var currentLatency int64
	if h := s.health[s.activeHealthKeyLocked()]; h != nil {
		currentLatency = h.URLTestLatency
	}
	candidateName := s.displayName(candidate.group.name, candidate.dialer.Name())
	s.mu.Unlock()

	slog.Debug("checking relay before latency switch", "from", currentName, "from_latency_ms", currentLatency, "to", candidateName)
	s.setRelayCheckStatus([]benchmarkTarget{candidate}, HealthChecking)
	check := s.testRelay(candidate.dialer)
	s.finishRelayCheck(candidate, check)

	s.mu.Lock()
	defer s.mu.Unlock()
	if check.err != nil && !candidateWasCurrent {
		s.rememberDependencyFailureLocked(candidate.pathKey)
	}
	sel = s.computeAutoSelectionsLocked()
	next, again := s.pendingLatencySwitchLocked(sel)
	_, activePathKey := s.activePathLocked()
	improved := check.err == nil && (currentLatency <= 0 || time.Duration(currentLatency)*time.Millisecond-check.urlLatency > s.tolerance)
	accepted := activePathKey == currentPathKey && again && next.pathKey == candidate.pathKey && improved
	switch {
	case accepted:
		// Confirmed: the fresh latency still clears the tolerance; apply switches.
	case again && next.pathKey != candidate.pathKey:
		// A different relay became the best candidate mid-check; hold position
		// until a later pass confirms it.
		s.holdActiveSelectionLocked(&sel)
		slog.Debug("relay switch deferred; best candidate changed during confirmation check", "current", currentName, "checked", candidateName, "next", s.displayName(next.group.name, next.dialer.Name()))
	default:
		s.holdActiveSelectionLocked(&sel)
		slog.Info("relay switch cancelled after confirmation check", "current", currentName, "current_latency_ms", currentLatency, "candidate", candidateName, "candidate_latency_ms", durationMillis(check.urlLatency), "tolerance_ms", s.tolerance.Milliseconds())
	}
	s.applyAutoSelectionsLocked(sel)
	if accepted && !candidateWasCurrent && s.benchmarkTargetCurrentLocked(candidate) {
		if h := s.health[s.healthKey(candidate.group.name, candidate.dialer.Name())]; h != nil {
			s.applyRelayCheckResultLocked(h, check)
			h.pathKey, h.Path = candidate.pathKey, append([]RelayHop(nil), candidate.path...)
			appendRelayHealthRecord(h)
		}
	}
	s.saveSelectionsLocked()
}
