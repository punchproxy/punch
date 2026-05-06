package relay

import (
	"log/slog"
	"time"
)

func (s *Selector) Start() {
	go s.Benchmark()
	go s.CheckDomesticConnectivity()
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
						slog.Warn("relay group auto refresh failed", "name", groupName, "error", err)
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

func (s *Selector) Stop() {
	close(s.stopCh)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.groups {
		for _, d := range g.dialers {
			_ = d.Close()
		}
	}
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
	interval := s.checkInterval
	if interval <= 0 {
		interval = 300 * time.Second
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
