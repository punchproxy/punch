package relay

import (
	"time"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) ApplyConfig(relayCfg config.Relay, checkCfg config.Check) error {
	groups, groupCfgs, err := s.buildGroups(relayCfg)
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
	s.mode = normalizeSelectMode(relayCfg.Select)
	if s.domesticURL != checkCfg.DomesticURL {
		s.domesticHealth = ConnectivityCheck{URL: checkCfg.DomesticURL}
	}
	s.outsideURL = checkCfg.OutsideURL
	s.domesticURL = checkCfg.DomesticURL
	s.fullCheckInterval = normalizeFullCheckInterval(checkCfg.FullInterval)
	s.selectedCheckInterval = normalizeSelectedCheckInterval(checkCfg.Interval)
	s.fullTriggerFailures = normalizeFullTriggerFailures(checkCfg.FullTriggerFailures)
	s.resetSelectedCheckFailuresLocked()
	s.tolerance = time.Duration(checkCfg.Tolerance) * time.Millisecond
	checkConcurrency := normalizeCheckConcurrency(checkCfg.Concurrency)
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

	s.notifyCheckConfigChanged()
	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	return nil
}

func (s *Selector) notifyCheckConfigChanged() {
	select {
	case s.benchmarkConfigCh <- struct{}{}:
	default:
	}
	select {
	case s.selectedConfigCh <- struct{}{}:
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
	if len(out) > maxHealthRecords {
		out = out[len(out)-maxHealthRecords:]
	}
	return out
}
