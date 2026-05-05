package dns

type DecisionStats struct {
	Requests   int64  `json:"requests"`
	LastDomain string `json:"last_domain,omitempty"`
}

type Stats struct {
	TotalQueries int64         `json:"total_queries"`
	Relay        DecisionStats `json:"relay"`
	Direct       DecisionStats `json:"direct"`
	Reject       DecisionStats `json:"reject"`
	CacheEntries int           `json:"cache_entries"`
	CacheHits    int64         `json:"cache_hits"`
}

func (s *Server) Stats() Stats {
	stats := Stats{
		TotalQueries: s.totalQueries.Load(),
		Relay:        DecisionStats{Requests: s.relayDecisions.Load()},
		Direct:       DecisionStats{Requests: s.directDecisions.Load()},
		Reject:       DecisionStats{Requests: s.rejectDecisions.Load()},
		CacheEntries: s.cache.Size(),
		CacheHits:    s.cacheHits.Load(),
	}

	s.mu.Lock()
	stats.Relay.LastDomain = s.lastRelayDomain
	stats.Direct.LastDomain = s.lastDirectDomain
	stats.Reject.LastDomain = s.lastRejectDomain
	s.mu.Unlock()
	return stats
}
