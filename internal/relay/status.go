package relay

import "time"

func (s *Selector) HealthList() []RelayHealth {
	// Health snapshots should not trigger hostname re-resolution by themselves.
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RelayHealth, 0, len(s.health))
	activeGroupIdx := s.activeGroupIndexLocked()
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			h := s.health[s.healthKey(g.name, "")]
			if h != nil {
				result = append(result, *h)
			}
			continue
		}
		activeDialerIdx := s.activeDialerIndexLocked(g)
		groupSelected := len(s.groups[activeGroupIdx].dialers) > 0 && s.groups[activeGroupIdx].name == g.name
		for di, d := range g.dialers {
			h := s.health[s.healthKey(g.name, d.Name())]
			if h == nil {
				continue
			}
			selected := groupSelected && di == activeDialerIdx
			result = append(result, RelayHealth{
				Name:              h.Name,
				Group:             h.Group,
				Type:              h.Type,
				Addr:              h.Addr,
				Status:            h.Status,
				Latency:           h.Latency,
				TCPConnectLatency: h.TCPConnectLatency,
				URLTestLatency:    h.URLTestLatency,
				CheckInterval:     int64(s.relayCheckIntervalLocked(selected).Seconds()),
				LastCheckedAt:     h.LastCheckedAt,
				LastRefreshedAt:   g.lastRefreshedAt,
				NextRefreshAt:     g.nextRefreshAt,
				RefreshInterval:   int64(g.refreshEvery.Seconds()),
				Selected:          selected,
				GroupMode:         displaySelectMode(h.GroupMode),
				GroupSourceURL:    h.GroupSourceURL,
				Spec:              cloneRelaySpec(h.Spec),
				History:           cloneHealthRecords(h.History),
				Error:             h.Error,
			})
		}
	}
	return result
}

func cloneRelaySpec(spec map[string]any) map[string]any {
	if len(spec) == 0 {
		return nil
	}
	return cloneRelayMapping(spec)
}

func (s *Selector) GroupList() []GroupStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]GroupStatus, 0, len(s.groups))
	activeGroupIdx := s.activeGroupIndexLocked()
	for gi, g := range s.groups {
		selected := gi == activeGroupIdx && len(g.dialers) > 0
		cfg := s.groupCfgs[g.name]
		groupType := cfg.Type
		if g.name == directGroupName {
			groupType = "direct"
		}
		status := GroupStatus{
			Name:                g.name,
			Type:                groupType,
			RelayCount:          len(g.dialers),
			Selected:            selected,
			Select:              displaySelectMode(g.mode),
			RemoteAddress:       g.sourceURL,
			CheckInterval:       int64(s.checkInterval.Seconds()),
			LastRefreshedAt:     g.lastRefreshedAt,
			NextRefreshAt:       g.nextRefreshAt,
			RefreshInterval:     int64(g.refreshEvery.Seconds()),
			RelayDomainResolver: cloneUpstreams(g.resolvers),
			Error:               g.loadError,
		}
		if len(g.dialers) > 0 {
			d := g.dialers[s.activeDialerIndexLocked(g)]
			status.CurrentRelay = d.Name()
			if h := s.health[s.healthKey(g.name, d.Name())]; h != nil {
				status.CurrentStatus = h.Status
				status.CurrentLatency = h.Latency
				status.CurrentTCPConnectLatency = h.TCPConnectLatency
				status.LastCheckedAt = h.LastCheckedAt
				if s.checkInterval > 0 && !h.LastCheckedAt.IsZero() && g.name != directGroupName {
					status.NextCheckAt = h.LastCheckedAt.Add(s.checkInterval)
				}
				if status.Error == "" {
					status.Error = h.Error
				}
			}
		} else if h := s.health[s.healthKey(g.name, "")]; h != nil {
			status.CurrentStatus = h.Status
			if status.Error == "" {
				status.Error = h.Error
			}
		}
		result = append(result, status)
	}
	return result
}

func (s *Selector) relayCheckIntervalLocked(selected bool) time.Duration {
	if selected && s.selectedInterval > 0 {
		return s.selectedInterval
	}
	return s.checkInterval
}
