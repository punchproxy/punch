package relay

import (
	"fmt"
	"strings"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) RefreshGroup(name string) error {
	return s.reloadGroup(name, true)
}

// abortRefresh unwinds a failed reload: the group becomes eligible for auto
// refresh again, but only after an exponential backoff so a broken
// subscription URL is not hammered on every refresh-loop tick.
func (s *Selector) abortRefresh(name string) {
	now := time.Now()
	s.mu.Lock()
	for _, g := range s.groups {
		if g.name == name {
			g.refreshing = false
			g.scheduleRefreshRetryLocked(now)
			break
		}
	}
	s.mu.Unlock()
}

// refreshRetryDelay reports how long until the group's next auto refresh
// attempt, for logging after a failure.
func (s *Selector) refreshRetryDelay(name string) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.groups {
		if g.name == name {
			if delay := time.Until(g.nextRefreshAt); delay > 0 {
				return delay.Round(time.Second)
			}
			return 0
		}
	}
	return 0
}

// ReloadGroup rebuilds a relay group from the currently cached asset (no
// remote fetch). Intended for use after an async asset download completes.
func (s *Selector) ReloadGroup(name string) error {
	return s.reloadGroup(name, false)
}

func (s *Selector) reloadGroup(name string, fetch bool) error {
	s.mu.Lock()
	cfg, ok := s.groupCfgs[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("relay group %q not found", name)
	}
	if cfg.Type != "remote" || cfg.URL == "" {
		s.mu.Unlock()
		return fmt.Errorf("relay group %q is not remote", name)
	}
	for _, g := range s.groups {
		if g.name == name {
			g.refreshing = true
			break
		}
	}
	s.mu.Unlock()
	if fetch {
		if err := s.assets.Refresh(cfg.URL, true); err != nil {
			s.abortRefresh(name)
			return err
		}
	}

	urlCache := make(map[string]relayPayload)
	newGroup, err := s.buildGroup(cfg, s.assets, urlCache)
	if err != nil {
		s.abortRefresh(name)
		return err
	}

	s.mu.Lock()
	prevActive := s.activeNameLocked()
	idx := -1
	var oldSelected string
	var oldLastChecked time.Time
	oldHealth := make(map[string]*RelayHealth)
	for i, g := range s.groups {
		if g.name != name {
			continue
		}
		idx = i
		oldLastChecked = g.lastRefreshedAt
		if len(g.dialers) > 0 {
			oldSelected = g.dialers[s.activeDialerIndexLocked(g)].Name()
		}
		break
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("relay group %q not found", name)
	}
	for key := range s.health {
		if strings.HasPrefix(key, name+"\x00") {
			oldHealth[key] = s.health[key]
			delete(s.health, key)
		}
	}
	if status, ok := s.assets.Status(cfg.URL); ok {
		newGroup.lastRefreshedAt = status.LastUpdated
		if newGroup.refreshEvery > 0 {
			newGroup.nextRefreshAt = status.LastUpdated.Add(newGroup.refreshEvery)
		}
	} else {
		newGroup.lastRefreshedAt = oldLastChecked
		if newGroup.refreshEvery > 0 && !oldLastChecked.IsZero() {
			newGroup.nextRefreshAt = oldLastChecked.Add(newGroup.refreshEvery)
		}
	}
	s.groups[idx] = newGroup
	if len(newGroup.dialers) == 0 {
		s.health[s.healthKey(newGroup.name, "")] = &RelayHealth{
			Name:           newGroup.name,
			Group:          newGroup.name,
			Type:           "group",
			Addr:           newGroup.sourceURL,
			Status:         HealthDown,
			GroupMode:      newGroup.mode,
			GroupSourceURL: newGroup.sourceURL,
			Error:          newGroup.loadError,
		}
	} else {
		selectedIdx := 0
		for i, d := range newGroup.dialers {
			if d.Name() == oldSelected {
				selectedIdx = i
			}
			key := s.healthKey(newGroup.name, d.Name())
			h := &RelayHealth{
				Name:           s.displayName(newGroup.name, d.Name()),
				Group:          newGroup.name,
				Type:           d.Type(),
				Addr:           d.Addr(),
				Status:         HealthPending,
				GroupMode:      newGroup.mode,
				GroupSourceURL: newGroup.sourceURL,
				Spec:           cloneRelaySpec(newGroup.specs[d.Name()]),
			}
			if old := oldHealth[key]; old != nil && old.Type == h.Type && old.Addr == h.Addr {
				h.Status = old.Status
				h.Latency = old.Latency
				h.TCPConnectLatency = old.TCPConnectLatency
				h.URLTestLatency = old.URLTestLatency
				h.LastCheckedAt = old.LastCheckedAt
				h.Error = old.Error
			}
			s.health[key] = h
		}
		newGroup.active.Store(int32(selectedIdx))
	}
	s.saveSelectionsLocked()
	s.mu.Unlock()

	s.publishRelayChange(prevActive)
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayHealth, Data: s.HealthList()})
	s.benchmarkTargetAsync(name)
	return nil
}
