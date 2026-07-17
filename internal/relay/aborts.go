package relay

import (
	"time"
)

// streamAbortWindow is the sliding window for "recent" mid-stream abort
// counts, surfaced next to health results that may still be green.
const streamAbortWindow = time.Minute

type abortStats struct {
	windowStart time.Time
	windowCount int
	total       int64
}

// ReportStreamAbort records that the relay side terminated an established
// stream abnormally (reset or unexpected error mid-transfer). name is the
// relay display name as returned by ActiveName. Health checks probe fresh
// connections and can stay green through this, so these counters are the
// signal that a relay is unstable for live traffic.
func (s *Selector) ReportStreamAbort(name string) {
	if name == "" {
		return
	}
	now := time.Now()
	s.abortMu.Lock()
	defer s.abortMu.Unlock()
	st := s.aborts[name]
	if st == nil {
		st = &abortStats{windowStart: now}
		s.aborts[name] = st
	}
	if now.Sub(st.windowStart) >= streamAbortWindow {
		st.windowStart = now
		st.windowCount = 0
	}
	st.windowCount++
	st.total++
}

// StreamAbortStats returns how many relay-side stream aborts were recorded
// for name within the current window and since the daemon started.
func (s *Selector) StreamAbortStats(name string) (recent int, total int64) {
	now := time.Now()
	s.abortMu.Lock()
	defer s.abortMu.Unlock()
	st := s.aborts[name]
	if st == nil {
		return 0, 0
	}
	if now.Sub(st.windowStart) >= streamAbortWindow {
		return 0, st.total
	}
	return st.windowCount, st.total
}
