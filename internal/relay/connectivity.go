package relay

import (
	"log/slog"
	"sync"
	"time"
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

func (s *Selector) CheckSelectedConnectivity() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.BenchmarkSelected()
	}()
	go func() {
		defer wg.Done()
		s.CheckDomesticConnectivity()
	}()
	wg.Wait()
}

func (s *Selector) CheckDomesticConnectivity() {
	url := s.domesticURLSnapshot()
	if url == "" {
		return
	}
	result := testURLConnectivity(url, s.directDialContext)

	s.mu.Lock()
	if s.domesticURL != url {
		s.mu.Unlock()
		return
	}
	applyConnectivityCheckResult(&s.domesticHealth, url, result)
	check := s.domesticHealth
	s.mu.Unlock()

	if result.err != nil {
		slog.Debug("domestic connectivity check failed", "url", url, "error", result.err)
	} else {
		slog.Debug("domestic connectivity check result", "url", url, "tcp_connect_latency_ms", check.TCPConnectLatency, "latency_ms", check.Latency, "status", check.Status)
	}
}

func (s *Selector) ConnectivityStatus() ConnectivityStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := ConnectivityStatus{
		CheckIntervalMS: s.selectedCheckIntervalLocked().Milliseconds(),
		Domestic:        s.domesticHealth,
		Outside: ConnectivityCheck{
			URL: s.outsideURL,
		},
	}
	status.Domestic.URL = s.domesticURL
	status.Domestic.History = cloneHealthRecords(status.Domestic.History)

	if len(s.groups) == 0 {
		return status
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	if len(g.dialers) == 0 {
		return status
	}
	d := g.dialers[s.activeDialerIndexLocked(g)]
	if h := s.health[s.healthKey(g.name, d.Name())]; h != nil {
		status.Outside.Status = h.Status
		status.Outside.Latency = h.Latency
		status.Outside.TCPConnectLatency = h.TCPConnectLatency
		status.Outside.LastCheckedAt = h.LastCheckedAt
		status.Outside.History = cloneHealthRecords(h.History)
		status.Outside.Error = h.Error
	}
	return status
}

func (s *Selector) selectedCheckIntervalLocked() time.Duration {
	if s.selectedInterval <= 0 {
		return defaultSelectedCheckInterval
	}
	return s.selectedInterval
}

func (s *Selector) domesticURLSnapshot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.domesticURL
}

func applyConnectivityCheckResult(check *ConnectivityCheck, url string, result relayCheckResult) {
	check.URL = url
	check.LastCheckedAt = time.Now()
	check.TCPConnectLatency = durationMillis(result.tcpLatency)
	check.Latency = durationMillis(result.urlLatency)
	if result.err != nil {
		check.Status = HealthDown
		check.Error = result.err.Error()
		appendConnectivityHealthRecord(check)
		return
	}
	check.Error = ""
	if result.urlLatency > 500*time.Millisecond {
		check.Status = HealthDegraded
	} else {
		check.Status = HealthHealthy
	}
	appendConnectivityHealthRecord(check)
}

func appendConnectivityHealthRecord(check *ConnectivityCheck) {
	check.History = append(check.History, HealthRecord{
		Time:              check.LastCheckedAt,
		Status:            check.Status,
		Latency:           check.Latency,
		TCPConnectLatency: check.TCPConnectLatency,
	})
	if len(check.History) > maxHealthRecords {
		check.History = check.History[len(check.History)-maxHealthRecords:]
	}
}
