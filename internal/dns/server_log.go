package dns

import (
	"sync"
	"time"
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
	s.queryLog = append(s.queryLog, ql)
	if len(s.queryLog) > s.maxLog {
		s.queryLog = s.queryLog[len(s.queryLog)-s.maxLog:]
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
