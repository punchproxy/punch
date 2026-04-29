package dns

import "github.com/punchproxy/punch/internal/config"

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
	defer s.mu.Unlock()
	for i := len(s.queryLog) - 1; i >= 0; i-- {
		entry := s.queryLog[i]
		switch string(entry.Decision) {
		case string(DecisionRelay), config.DecisionRelay:
			if stats.Relay.LastDomain == "" {
				stats.Relay.LastDomain = entry.Domain
			}
		case string(DecisionDirect), config.DecisionDirect:
			if stats.Direct.LastDomain == "" {
				stats.Direct.LastDomain = entry.Domain
			}
		case string(DecisionReject), config.DecisionReject:
			if stats.Reject.LastDomain == "" {
				stats.Reject.LastDomain = entry.Domain
			}
		}
		if stats.Relay.LastDomain != "" && stats.Direct.LastDomain != "" && stats.Reject.LastDomain != "" {
			break
		}
	}
	return stats
}
