package relay

import (
	"log/slog"
	"time"
)

func (s *Selector) Start() {
	go s.Benchmark()
	go s.CheckSelectedConnectivity()
	go s.refreshLoop()
	go s.selectedCheckLoop()
	go func() {
		for {
			interval, enabled := s.benchmarkLoopConfig()
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
				if enabled {
					s.Benchmark()
				}
			case <-s.benchmarkConfigCh:
				timer.Stop()
				continue
			case <-s.stopCh:
				timer.Stop()
				return
			}
		}
	}()
}

func (s *Selector) selectedCheckLoop() {
	for {
		interval := s.selectedCheckLoopInterval()
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
			s.CheckSelectedConnectivity()
		case <-s.selectedConfigCh:
			timer.Stop()
			continue
		case <-s.stopCh:
			timer.Stop()
			return
		}
	}
}

func (s *Selector) refreshLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, name := range s.dueRefreshGroups() {
				go func(groupName string) {
					if err := s.RefreshGroup(groupName); err != nil {
						slog.Warn("relay group auto refresh failed", "name", groupName, "error", err, "retry_in", s.refreshRetryDelay(groupName))
					}
				}(name)
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *Selector) dueRefreshGroups() []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, g := range s.groups {
		if g.sourceURL == "" || g.refreshEvery <= 0 || g.nextRefreshAt.IsZero() || g.refreshing {
			continue
		}
		if !now.Before(g.nextRefreshAt) {
			g.refreshing = true
			names = append(names, g.name)
		}
	}
	return names
}

// Stop halts the background loops and closes every dialer. It is safe to call
// more than once (shutdown paths call it from several places).
func (s *Selector) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)

		// Snapshot the dialers before closing them: an adapter's Close can
		// block on live connections, and holding the selector lock across that
		// would stall every DialContext and ApplyConfig in the process.
		var dialers []Dialer
		s.mu.RLock()
		for _, g := range s.groups {
			dialers = append(dialers, g.dialers...)
		}
		s.mu.RUnlock()

		for _, d := range dialers {
			_ = d.Close()
		}
	})
}

func (s *Selector) anyAutoGroupLocked() bool {
	for _, g := range s.groups {
		if g.mode == "auto" {
			return true
		}
	}
	return false
}

func (s *Selector) benchmarkLoopConfig() (time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	interval := s.fullCheckInterval
	if interval <= 0 {
		interval = defaultFullCheckInterval
	}
	return interval, s.mode == "auto" || s.anyAutoGroupLocked()
}

func (s *Selector) selectedCheckLoopInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selectedCheckIntervalLocked()
}

func (s *Selector) benchmarkTargetAsync(name string) {
	go func() {
		if err := s.BenchmarkTarget(name); err != nil {
			slog.Warn("relay group health check after reload failed", "group", name, "error", err)
		}
	}()
}
