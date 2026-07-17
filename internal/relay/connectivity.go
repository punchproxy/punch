package relay

import (
	"log/slog"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

// ConnectivityCheck is a point-in-time URL reachability check.
type ConnectivityCheck struct {
	URL               string         `json:"url"`
	Status            HealthStatus   `json:"status,omitempty"`
	Latency           int64          `json:"latency_ms,omitempty"`
	TCPConnectLatency int64          `json:"tcp_connect_latency_ms,omitempty"`
	LastCheckedAt     time.Time      `json:"last_checked_at,omitempty"`
	History           []HealthRecord `json:"history,omitempty"`
	Error             string         `json:"error,omitempty"`
}

// ConnectivityStatus describes direct domestic reachability and selected
// outside reachability through the active relay path.
type ConnectivityStatus struct {
	CheckIntervalMS int64             `json:"check_interval_ms,omitempty"`
	Domestic        ConnectivityCheck `json:"domestic"`
	Outside         ConnectivityCheck `json:"outside"`
}

// CheckSelectedConnectivity runs the domestic ("Internet") and outside
// ("Relayed") reachability checks in parallel. Both checks run on every tick
// regardless of the per-relay benchmark state — the outside check always uses
// whatever relay is currently active at the moment of the check.
func (s *Selector) CheckSelectedConnectivity() {
	var outsideTarget benchmarkTarget
	var outsideChecked bool
	var outsideFailed bool
	var domesticChecked bool
	var domesticFailed bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		outsideTarget, outsideChecked, outsideFailed = s.CheckOutsideConnectivity()
	}()
	go func() {
		defer wg.Done()
		domesticChecked, domesticFailed = s.CheckDomesticConnectivity()
	}()
	wg.Wait()
	if outsideChecked {
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
		s.reevaluateAutoSelections()
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
	s.outsideHealth.TCPConnectLatency = 0
	s.outsideHealth.LastCheckedAt = time.Now()
	s.outsideHealth.Error = reason
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
		slog.Debug("domestic connectivity check result", "url", url, "tcp_connect_latency_ms", check.TCPConnectLatency, "latency_ms", check.Latency, "status", check.Status)
	}
	return true, result.err != nil
}

// applyOutsideConnectivityCheckResult unconditionally records the result of
// an outside check against the relay we just tested. The result reflects the
// relay that was active at the moment the check started; we do not discard it
// even if the active selection changed during the check.
func (s *Selector) applyOutsideConnectivityCheckResult(target benchmarkTarget, result relayCheckResult) {
	key := s.healthKey(target.group.name, target.dialer.Name())
	s.mu.Lock()
	defer s.mu.Unlock()
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
	status.Outside.URL = s.outsideURL
	status.Outside.History = cloneHealthRecords(status.Outside.History)
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
	check.TCPConnectLatency = durationMillis(result.tcpLatency)
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
		Time:              check.LastCheckedAt,
		Status:            check.Status,
		Latency:           check.Latency,
		TCPConnectLatency: check.TCPConnectLatency,
		Relay:             relay,
	})
	if len(check.History) > maxHealthRecords {
		check.History = check.History[len(check.History)-maxHealthRecords:]
	}
}
