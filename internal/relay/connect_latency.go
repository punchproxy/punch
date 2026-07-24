package relay

import (
	"sync"
	"time"
)

const (
	// connectSampleWindow is the fixed history shown by the dashboard.
	connectSampleWindow = 10 * time.Minute

	// maxConnectSamples bounds memory and response size within that window.
	maxConnectSamples = 300
)

// ConnectSample is the observed connect latency of one real proxied request:
// the time from starting the relay dial until the connection (including the
// relay protocol handshake) was ready. Unlike a synthetic probe, it reflects
// live traffic and sends nothing extra to the relay.
type ConnectSample struct {
	At    time.Time `json:"at"`
	Relay string    `json:"relay,omitempty"`
	MS    int64     `json:"ms"`
}

type connectSamples struct {
	mu      sync.Mutex
	samples []ConnectSample
	next    int
	full    bool
}

func (c *connectSamples) add(sample ConnectSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) < maxConnectSamples {
		c.samples = append(c.samples, sample)
		return
	}
	c.samples[c.next] = sample
	c.next = (c.next + 1) % maxConnectSamples
	c.full = true
}

// snapshot returns samples at or after cutoff in chronological order.
func (c *connectSamples) snapshot(cutoff time.Time) []ConnectSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) == 0 {
		return nil
	}
	ordered := make([]ConnectSample, 0, len(c.samples))
	if c.full {
		ordered = append(ordered, c.samples[c.next:]...)
		ordered = append(ordered, c.samples[:c.next]...)
	} else {
		ordered = append(ordered, c.samples...)
	}
	out := make([]ConnectSample, 0, len(ordered))
	for _, sample := range ordered {
		if sample.At.Before(cutoff) || sample.MS <= 1 {
			continue
		}
		out = append(out, sample)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RecordConnectLatency records the dial latency of a real proxied connection
// through relay (display name, or DIRECT).
func (s *Selector) RecordConnectLatency(relay string, d time.Duration) {
	ms := durationMillis(d)
	if ms <= 1 {
		return
	}
	s.connects.add(ConnectSample{At: time.Now(), Relay: relay, MS: ms})
}

// ConnectLatencySamples returns the last ten minutes of per-request connect
// latency samples in chronological order.
func (s *Selector) ConnectLatencySamples() []ConnectSample {
	return s.connects.snapshot(time.Now().Add(-connectSampleWindow))
}
