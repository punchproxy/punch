package relay

import (
	"sync"
	"time"
)

// maxConnectSamples bounds the in-memory ring of per-request connect
// latency samples exposed to the dashboard.
const maxConnectSamples = 300

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

// snapshot returns the samples in chronological order.
func (c *connectSamples) snapshot() []ConnectSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) == 0 {
		return nil
	}
	out := make([]ConnectSample, 0, len(c.samples))
	if c.full {
		out = append(out, c.samples[c.next:]...)
		out = append(out, c.samples[:c.next]...)
	} else {
		out = append(out, c.samples...)
	}
	return out
}

// RecordConnectLatency records the dial latency of a real proxied connection
// through relay (display name, or DIRECT).
func (s *Selector) RecordConnectLatency(relay string, d time.Duration) {
	if d <= 0 {
		return
	}
	s.connects.add(ConnectSample{At: time.Now(), Relay: relay, MS: durationMillis(d)})
}

// ConnectLatencySamples returns recent per-request connect latency samples in
// chronological order.
func (s *Selector) ConnectLatencySamples() []ConnectSample {
	return s.connects.snapshot()
}
