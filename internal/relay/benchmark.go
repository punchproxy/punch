package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) Benchmark() {
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

	results := s.runRelayChecks(targets)

	s.mu.Lock()
	for _, result := range results {
		h := s.health[s.healthKey(result.target.group.name, result.target.dialer.Name())]
		if h == nil {
			continue
		}
		if result.check.err != nil {
			slog.Debug("relay health check failed", "group", result.target.group.name, "relay", result.target.dialer.Name(), "error", result.check.err)
		} else {
			slog.Debug("relay health check result", "group", result.target.group.name, "relay", result.target.dialer.Name(), "tcp_connect_latency_ms", h.TCPConnectLatency, "url_test_latency_ms", h.URLTestLatency, "status", h.Status)
		}
	}
	s.reevaluateAutoSelectionsLocked()
	s.saveSelectionsLocked()
	s.mu.Unlock()

	slog.Debug("relay benchmark completed", "active", s.ActiveName())
	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
}

func (s *Selector) BenchmarkTarget(name string) error {
	s.mu.RLock()
	var targets []benchmarkTarget
	var targetGroup *group
	var benchmarkWholeGroup bool
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		if g.name == name {
			if g.name == directGroupName {
				s.mu.RUnlock()
				return nil
			}
			targetGroup = g
			benchmarkWholeGroup = true
			for di, d := range g.dialers {
				targets = append(targets, benchmarkTarget{group: g, index: di, dialer: d})
			}
			break
		}
		for di, d := range g.dialers {
			if s.displayName(g.name, d.Name()) == name || d.Name() == name {
				targetGroup = g
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
	return s.benchmarkTargets(targets, targetGroup, benchmarkWholeGroup)
}

func (s *Selector) BenchmarkSelected() {
	target, ok := s.selectedBenchmarkTarget()
	if !ok {
		return
	}
	if err := s.benchmarkTargets([]benchmarkTarget{target}, nil, false); err != nil {
		slog.Warn("selected relay health check failed", "error", err)
	}
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
	if err := s.benchmarkTargets(targets, nil, false); err != nil {
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
		if err := s.benchmarkTargets(targets, nil, false); err != nil {
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
	return benchmarkTarget{group: g, index: idx, dialer: g.dialers[idx]}, true
}

func (s *Selector) benchmarkTargets(targets []benchmarkTarget, targetGroup *group, benchmarkWholeGroup bool) error {
	prevActive := s.ActiveName()

	results := s.runRelayChecks(targets)

	s.mu.Lock()
	if benchmarkWholeGroup && targetGroup != nil {
		bestIdx := s.activeDialerIndexLocked(targetGroup)
		bestLatency := time.Duration(1<<63 - 1)
		for _, result := range results {
			if result.check.err != nil {
				continue
			}
			if result.check.urlLatency < bestLatency {
				bestLatency = result.check.urlLatency
				bestIdx = result.target.index
			}
		}
		if targetGroup.mode == "auto" && len(targetGroup.dialers) > 0 && bestLatency != time.Duration(1<<63-1) {
			targetGroup.active.Store(int32(bestIdx))
			s.saveSelectionsLocked()
		}
	}
	s.reevaluateAutoSelectionsLocked()
	s.saveSelectionsLocked()
	s.mu.Unlock()

	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	return nil
}

type benchmarkTargetResult struct {
	target benchmarkTarget
	check  relayCheckResult
}

func (s *Selector) runRelayChecks(targets []benchmarkTarget) []benchmarkTargetResult {
	if len(targets) == 0 {
		return nil
	}
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
	if h := s.health[s.healthKey(target.group.name, target.dialer.Name())]; h != nil {
		s.applyRelayCheckResultLocked(h, result)
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

func normalizeSelectedCheckInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultSelectedCheckInterval
	}
	return time.Duration(seconds) * time.Second
}

func (s *Selector) setRelayCheckStatus(targets []benchmarkTarget, status HealthStatus) {
	s.mu.Lock()
	changed := false
	for _, target := range targets {
		if h := s.health[s.healthKey(target.group.name, target.dialer.Name())]; h != nil {
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
	group  *group
	index  int
	dialer Dialer
}

type benchmarkGroup struct {
	group   *group
	dialers []Dialer
}

type relayCheckResult struct {
	tcpLatency time.Duration
	urlLatency time.Duration
	err        error
}

func (s *Selector) testRelay(d Dialer) relayCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tcpLatency, err := d.TCPConnectLatency(ctx)
	if err != nil {
		return relayCheckResult{err: fmt.Errorf("tcp connect to relay: %w", err)}
	}
	urlLatency, err := s.testRelayURL(ctx, d)
	if err != nil {
		return relayCheckResult{tcpLatency: tcpLatency, err: err}
	}
	return relayCheckResult{tcpLatency: tcpLatency, urlLatency: urlLatency}
}

func (s *Selector) testRelayURL(ctx context.Context, d Dialer) (time.Duration, error) {
	return testURLLatency(ctx, s.outsideURL, d.DialContext)
}

func testURLConnectivity(rawURL string, dialContext DialContextFunc) relayCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	target, err := parseCheckURL(rawURL)
	if err != nil {
		return relayCheckResult{err: err}
	}
	address, err := checkURLAddress(target)
	if err != nil {
		return relayCheckResult{err: err}
	}
	tcpLatency, err := tcpConnectLatencyWithDialer(ctx, dialContext, address)
	if err != nil {
		return relayCheckResult{err: fmt.Errorf("tcp connect to check url: %w", err)}
	}
	urlLatency, err := testURLLatency(ctx, rawURL, dialContext)
	if err != nil {
		return relayCheckResult{tcpLatency: tcpLatency, err: err}
	}
	return relayCheckResult{tcpLatency: tcpLatency, urlLatency: urlLatency}
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

func checkURLAddress(target *url.URL) (string, error) {
	port := target.Port()
	if port == "" {
		switch target.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("unsupported test url scheme %q", target.Scheme)
		}
	}
	return net.JoinHostPort(target.Hostname(), port), nil
}

func tcpConnectLatencyWithDialer(ctx context.Context, dialContext DialContextFunc, address string) (time.Duration, error) {
	start := time.Now()
	var conn net.Conn
	var err error
	if dialContext != nil {
		conn, err = dialContext(ctx, "tcp", address)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return time.Since(start), nil
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
	return time.Since(start), nil
}

func (s *Selector) applyRelayCheckResultLocked(h *RelayHealth, result relayCheckResult) {
	h.LastCheckedAt = time.Now()
	h.TCPConnectLatency = durationMillis(result.tcpLatency)
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
		Time:              h.LastCheckedAt,
		Status:            h.Status,
		Latency:           h.Latency,
		TCPConnectLatency: h.TCPConnectLatency,
	})
	if len(h.History) > maxRelayHealthRecords {
		h.History = h.History[len(h.History)-maxRelayHealthRecords:]
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

func (s *Selector) reevaluateAutoSelectionsLocked() {
	for _, g := range s.groups {
		if g.mode != "auto" || len(g.dialers) == 0 {
			continue
		}
		bestIdx := -1
		bestLatency := time.Duration(1<<63 - 1)
		for di, d := range g.dialers {
			h := s.health[s.healthKey(g.name, d.Name())]
			if h == nil || h.Status == HealthDown || h.URLTestLatency <= 0 {
				continue
			}
			latency := time.Duration(h.URLTestLatency) * time.Millisecond
			if latency < bestLatency {
				bestLatency = latency
				bestIdx = di
			}
		}
		if bestIdx >= 0 {
			g.active.Store(int32(bestIdx))
		}
	}

	if s.mode != "auto" {
		return
	}
	bestGroupIdx := -1
	bestGroupLatency := time.Duration(1<<63 - 1)
	directGroupIdx := -1
	for gi, g := range s.groups {
		if g.name == directGroupName {
			directGroupIdx = gi
			continue
		}
		if len(g.dialers) == 0 {
			continue
		}
		d := g.dialers[s.activeDialerIndexLocked(g)]
		h := s.health[s.healthKey(g.name, d.Name())]
		if h == nil || h.Status == HealthDown || h.URLTestLatency <= 0 {
			continue
		}
		latency := time.Duration(h.URLTestLatency) * time.Millisecond
		if latency < bestGroupLatency {
			bestGroupLatency = latency
			bestGroupIdx = gi
		}
	}
	if bestGroupIdx < 0 {
		bestGroupIdx = directGroupIdx
	}
	if bestGroupIdx < 0 || s.activeGroupIndexLocked() == bestGroupIdx {
		return
	}
	s.active.Store(int32(bestGroupIdx))
}
