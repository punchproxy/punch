package relay

import (
	"log/slog"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

// ConnectivityCheck is a point-in-time URL reachability check.
type ConnectivityCheck struct {
	URL           string         `json:"url"`
	Status        HealthStatus   `json:"status,omitempty"`
	Latency       int64          `json:"latency_ms,omitempty"`
	LastCheckedAt time.Time      `json:"last_checked_at,omitempty"`
	History       []HealthRecord `json:"history,omitempty"`
	Error         string         `json:"error,omitempty"`
	Path          []RelayHop     `json:"path,omitempty"`
	pathKey       string
}

// ConnectivityStatus describes direct domestic reachability and selected
// outside reachability through the active relay path, plus per-request
// connect latency observed on live traffic.
type ConnectivityStatus struct {
	CheckIntervalMS int64             `json:"check_interval_ms,omitempty"`
	Domestic        ConnectivityCheck `json:"domestic"`
	Outside         ConnectivityCheck `json:"outside"`
	ConnectSamples  []ConnectSample   `json:"connect_samples,omitempty"`
}

// CheckSelectedConnectivity checks the selected path, its selected upstream
// relays, and direct domestic connectivity. Only the final path updates the
// outside connectivity result.
func (s *Selector) CheckSelectedConnectivity() {
	if !s.selectedChecksMu.TryLock() {
		return
	}
	defer s.selectedChecksMu.Unlock()
	s.mu.RLock()
	dependencies := s.selectedDependencyTargetsLocked()
	s.mu.RUnlock()
	var outsideTarget benchmarkTarget
	var outsideChecked bool
	var outsideFailed bool
	var domesticChecked bool
	var domesticFailed bool
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		outsideTarget, outsideChecked, outsideFailed = s.checkOutsideConnectivity(false)
	}()
	go func() {
		defer wg.Done()
		domesticChecked, domesticFailed = s.CheckDomesticConnectivity()
	}()
	go func() {
		defer wg.Done()
		s.runRelayChecks(dependencies)
	}()
	wg.Wait()
	if outsideChecked {
		if !outsideFailed {
			prevActive := s.ActiveName()
			s.reevaluateAutoSelections()
			s.publishRelayChange(prevActive)
		}
		s.triggerFullBenchmarkAfterSelectedCheck(outsideTarget, outsideFailed, domesticChecked && domesticFailed)
	}
}

// CheckOutsideConnectivity tests outside reachability through whatever relay
// is currently active. It always writes the result to outsideHealth, even if
// the active relay changes mid-flight. It also folds the result into the
// active relay's per-relay health so that the active row stays fresh.
//
// A failed check never switches relays by itself: the selection holds until
// the consecutive-failure threshold triggers a full benchmark (see
// triggerFullBenchmarkAfterSelectedCheck), which re-tests every relay and
// fails over to the best one.
func (s *Selector) CheckOutsideConnectivity() (benchmarkTarget, bool, bool) {
	return s.checkOutsideConnectivity(true)
}

func (s *Selector) checkOutsideConnectivity(reevaluate bool) (benchmarkTarget, bool, bool) {
	target, ok := s.selectedBenchmarkTarget()
	if !ok {
		s.markOutsideUnavailableLocked("no active relay")
		return benchmarkTarget{}, false, false
	}
	prevActive := s.ActiveName()
	s.setRelayCheckStatus([]benchmarkTarget{target}, HealthChecking)
	result := s.testRelay(target.dialer)
	if result.err != nil {
		slog.Warn("selected relay check failed", "group", target.group.name, "relay", target.dialer.Name(), "error", result.err)
	}
	s.finishRelayCheck(target, result)
	s.applyOutsideConnectivityCheckResult(target, result)

	if result.err == nil {
		if reevaluate {
			s.reevaluateAutoSelections()
		}
		// A green probe on a fresh connection can coexist with live streams
		// being reset; surface that contrast so unstable relays are visible.
		if recent, total := s.StreamAbortStats(prevActive); recent > 0 {
			slog.Warn("relay passed connectivity check but recently aborted live streams",
				"relay", prevActive, "aborts_last_minute", recent, "aborts_total", total)
		}
	}

	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	return target, true, result.err != nil
}

func (s *Selector) markOutsideUnavailableLocked(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outsideHealth.URL = s.outsideURL
	s.outsideHealth.Status = HealthDown
	s.outsideHealth.Latency = 0
	s.outsideHealth.LastCheckedAt = time.Now()
	s.outsideHealth.Error = reason
	s.outsideHealth.Path = nil
	s.outsideHealth.pathKey = ""
	s.outsideHealthKey = ""
	appendConnectivityHealthRecord(&s.outsideHealth, "")
}

func (s *Selector) CheckDomesticConnectivity() (bool, bool) {
	url := s.domesticURLSnapshot()
	if url == "" {
		return false, false
	}
	result := testURLConnectivity(url, s.directDialContext)

	s.mu.Lock()
	if s.domesticURL != url {
		s.mu.Unlock()
		return false, false
	}
	applyConnectivityCheckResult(&s.domesticHealth, url, result, "")
	check := s.domesticHealth
	s.mu.Unlock()

	if result.err != nil {
		slog.Debug("domestic connectivity check failed", "url", url, "error", result.err)
	} else {
		slog.Debug("domestic connectivity check result", "url", url, "latency_ms", check.Latency, "status", check.Status)
	}
	return true, result.err != nil
}

// applyOutsideConnectivityCheckResult keeps observations from old paths in
// history without replacing the current result with a superseded generation.
func (s *Selector) applyOutsideConnectivityCheckResult(target benchmarkTarget, result relayCheckResult) {
	key := s.healthKey(target.group.name, target.dialer.Name())
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.benchmarkTargetCurrentLocked(target) {
		observation := ConnectivityCheck{Path: append([]RelayHop(nil), target.path...)}
		applyConnectivityCheckResult(&observation, s.outsideURL, result, s.displayName(target.group.name, target.dialer.Name()))
		s.outsideHealth.History = append(s.outsideHealth.History, observation.History...)
		if len(s.outsideHealth.History) > maxHealthRecords {
			s.outsideHealth.History = s.outsideHealth.History[len(s.outsideHealth.History)-maxHealthRecords:]
		}
		return
	}
	s.outsideHealth.Path = append([]RelayHop(nil), target.path...)
	s.outsideHealth.pathKey = target.pathKey
	applyConnectivityCheckResult(&s.outsideHealth, s.outsideURL, result, s.displayName(target.group.name, target.dialer.Name()))
	s.outsideHealthKey = key
}

func (s *Selector) ConnectivityStatus() ConnectivityStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := ConnectivityStatus{
		CheckIntervalMS: s.selectedCheckIntervalLocked().Milliseconds(),
		Domestic:        s.domesticHealth,
		Outside:         s.outsideHealth,
	}
	status.Domestic.URL = s.domesticURL
	status.Domestic.History = cloneHealthRecords(status.Domestic.History)
	status.Domestic.Path = append([]RelayHop(nil), status.Domestic.Path...)
	status.Outside.URL = s.outsideURL
	status.Outside.History = cloneHealthRecords(status.Outside.History)
	status.Outside.Path = append([]RelayHop(nil), status.Outside.Path...)
	// A historical result for a different exit remains visible with its path.
	// The same exit through a new upstream must be checked again.
	if s.outsideHealth.pathKey != "" && s.outsideHealthKey == s.activeHealthKeyLocked() {
		path, key := s.activePathLocked()
		if key != s.outsideHealth.pathKey {
			status.Outside.Status = HealthPending
			status.Outside.Latency = 0
			status.Outside.LastCheckedAt = time.Time{}
			status.Outside.Error = ""
			status.Outside.Path = path
		}
	}
	status.ConnectSamples = s.ConnectLatencySamples()
	return status
}

func (s *Selector) selectedCheckIntervalLocked() time.Duration {
	if s.selectedCheckInterval <= 0 {
		return defaultSelectedCheckInterval
	}
	return s.selectedCheckInterval
}

func (s *Selector) domesticURLSnapshot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.domesticURL
}

func applyConnectivityCheckResult(check *ConnectivityCheck, url string, result relayCheckResult, relay string) {
	check.URL = url
	check.LastCheckedAt = time.Now()
	check.Latency = durationMillis(result.urlLatency)
	if result.err != nil {
		check.Status = HealthDown
		check.Error = result.err.Error()
		appendConnectivityHealthRecord(check, relay)
		return
	}
	check.Error = ""
	if result.urlLatency > 500*time.Millisecond {
		check.Status = HealthDegraded
	} else {
		check.Status = HealthHealthy
	}
	appendConnectivityHealthRecord(check, relay)
}

func appendConnectivityHealthRecord(check *ConnectivityCheck, relay string) {
	check.History = append(check.History, HealthRecord{
		Time:    check.LastCheckedAt,
		Status:  check.Status,
		Latency: check.Latency,
		Relay:   relay,
		Path:    append([]RelayHop(nil), check.Path...),
	})
	if len(check.History) > maxHealthRecords {
		check.History = check.History[len(check.History)-maxHealthRecords:]
	}
}
