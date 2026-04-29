package relay

import (
	"time"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) ApplyConfig(cfg config.Relay) error {
	groups, groupCfgs, err := s.buildGroups(cfg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	prevActive := s.activeNameLocked()
	oldGroups := s.groups
	oldHealth := s.health
	selections := s.snapshotSelectionsLocked()
	if selections.ActiveGroup == "" {
		selections = s.loadSelections()
	}
	s.mode = normalizeSelectMode(cfg.Select)
	s.testURL = cfg.AutoStrategy.URL
	s.interval = time.Duration(cfg.AutoStrategy.Interval) * time.Second
	s.tolerance = time.Duration(cfg.AutoStrategy.Tolerance) * time.Millisecond
	checkConcurrency := normalizeRelayCheckConcurrency(cfg.AutoStrategy.CheckConcurrency)
	if s.checkSem == nil || cap(s.checkSem) != checkConcurrency {
		s.checkSem = make(chan struct{}, checkConcurrency)
	}
	s.groupCfgs = groupCfgs
	s.groups = groups
	s.active.Store(0)
	s.populateHealthLocked(oldHealth)
	s.restoreSelections(selections)
	s.saveSelectionsLocked()
	s.mu.Unlock()

	for _, g := range oldGroups {
		for _, d := range g.dialers {
			_ = d.Close()
		}
	}

	s.notifyBenchmarkConfigChanged()
	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	return nil
}

func (s *Selector) notifyBenchmarkConfigChanged() {
	select {
	case s.benchmarkConfigCh <- struct{}{}:
	default:
	}
}

func (s *Selector) populateHealthLocked(previous map[string]*RelayHealth) {
	s.health = make(map[string]*RelayHealth)
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			s.health[s.healthKey(g.name, "")] = &RelayHealth{
				Name:           g.name,
				Group:          g.name,
				Type:           "group",
				Addr:           g.sourceURL,
				Status:         HealthDown,
				GroupMode:      g.mode,
				GroupSourceURL: g.sourceURL,
				Error:          g.loadError,
			}
			continue
		}
		for _, d := range g.dialers {
			status := HealthPending
			if g.name == directGroupName {
				status = HealthHealthy
			}
			key := s.healthKey(g.name, d.Name())
			h := &RelayHealth{
				Name:           s.displayName(g.name, d.Name()),
				Group:          g.name,
				Type:           d.Type(),
				Addr:           d.Addr(),
				Status:         status,
				GroupMode:      g.mode,
				GroupSourceURL: g.sourceURL,
				Spec:           cloneRelaySpec(g.specs[d.Name()]),
			}
			if old := previous[key]; old != nil && old.Type == h.Type && old.Addr == h.Addr {
				h.Status = old.Status
				h.Latency = old.Latency
				h.TCPConnectLatency = old.TCPConnectLatency
				h.URLTestLatency = old.URLTestLatency
				h.LastCheckedAt = old.LastCheckedAt
				h.History = cloneHealthRecords(old.History)
				h.Error = old.Error
			}
			s.health[key] = h
		}
	}
}

func cloneHealthRecords(records []HealthRecord) []HealthRecord {
	if len(records) == 0 {
		return nil
	}
	out := append([]HealthRecord(nil), records...)
	if len(out) > maxRelayHealthRecords {
		out = out[len(out)-maxRelayHealthRecords:]
	}
	return out
}
