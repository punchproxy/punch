package dns

import (
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/config"
)

// QueryLog records information about a DNS query.
type QueryLog struct {
	Time     time.Time `json:"time"`
	Source   string    `json:"source"`
	Domain   string    `json:"domain"`
	QType    string    `json:"qtype"`
	Decision Decision  `json:"decision"`
	Result   string    `json:"result"`
	Upstream string    `json:"upstream"`
	Latency  int64     `json:"latency_ms"`
	Rule     string    `json:"rule"`
	Cached   bool      `json:"cached"`
}

func (s *Server) addQueryLog(ql QueryLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch string(ql.Decision) {
	case string(DecisionRelay), config.DecisionRelay:
		s.lastRelayDomain = ql.Domain
	case string(DecisionDirect), config.DecisionDirect:
		s.lastDirectDomain = ql.Domain
	case string(DecisionReject), config.DecisionReject:
		s.lastRejectDomain = ql.Domain
	}
}

// SubscribeQueryLogs registers a best-effort query log subscriber. Delivery is
// nonblocking; slow subscribers may miss entries rather than delaying DNS.
func (s *Server) SubscribeQueryLogs(ch chan<- QueryLog) func() {
	s.queryStreamMu.Lock()
	s.queryStreamClients[ch] = struct{}{}
	s.queryStreamMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.queryStreamMu.Lock()
			delete(s.queryStreamClients, ch)
			s.queryStreamMu.Unlock()
		})
	}
}

func (s *Server) fanoutQueryLog(ql QueryLog) {
	s.queryStreamMu.Lock()
	defer s.queryStreamMu.Unlock()
	for ch := range s.queryStreamClients {
		select {
		case ch <- ql:
		default:
		}
	}
}
