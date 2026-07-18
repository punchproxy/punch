package relay

import (
	"fmt"
	"testing"
	"time"
)

func TestConnectLatencySamplesChronologicalRing(t *testing.T) {
	s := &Selector{}

	if got := s.ConnectLatencySamples(); got != nil {
		t.Fatalf("empty samples = %v, want nil", got)
	}

	s.RecordConnectLatency("US / relay-1", 20*time.Millisecond)
	s.RecordConnectLatency("US / relay-1", 0) // ignored
	s.RecordConnectLatency("DIRECT", 5*time.Millisecond)

	got := s.ConnectLatencySamples()
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2 (zero-duration sample must be dropped)", len(got))
	}
	if got[0].Relay != "US / relay-1" || got[0].MS != 20 || got[1].Relay != "DIRECT" || got[1].MS != 5 {
		t.Fatalf("samples = %+v", got)
	}
	if got[0].At.After(got[1].At) {
		t.Fatal("samples not chronological")
	}

	// Overflow the ring and confirm the oldest samples fall off in order.
	for i := 0; i < maxConnectSamples+10; i++ {
		s.RecordConnectLatency(fmt.Sprintf("r%d", i), time.Duration(i+1)*time.Millisecond)
	}
	got = s.ConnectLatencySamples()
	if len(got) != maxConnectSamples {
		t.Fatalf("ring size = %d, want %d", len(got), maxConnectSamples)
	}
	if got[len(got)-1].Relay != fmt.Sprintf("r%d", maxConnectSamples+9) {
		t.Fatalf("newest sample = %+v, want last recorded", got[len(got)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.Before(got[i-1].At) {
			t.Fatalf("sample %d out of order", i)
		}
	}
}
