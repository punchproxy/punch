package relay

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type closeTrackingDialer struct {
	closed atomic.Bool
}

func (d *closeTrackingDialer) Name() string     { return "old" }
func (d *closeTrackingDialer) Type() string     { return "AnyTLS" }
func (d *closeTrackingDialer) Addr() string     { return "192.0.2.1:443" }
func (d *closeTrackingDialer) SupportUDP() bool { return true }
func (d *closeTrackingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (d *closeTrackingDialer) Close() error {
	d.closed.Store(true)
	return nil
}

func TestExpiredRelayDNSRetiresAdapterWithoutClosingActiveStreams(t *testing.T) {
	old := &closeTrackingDialer{}
	d := &LazyRelayDialer{
		groupName: "main",
		name:      "relay",
		relayType: "anytls",
		mapping: map[string]any{
			"name":   "replacement",
			"type":   "direct",
			"server": "relay.example",
			"port":   443,
		},
		resolver: func(context.Context, string, string) ([]netip.Addr, time.Time, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, time.Now().Add(time.Minute), nil
		},
		resolved:  old,
		expiresAt: time.Now().Add(-time.Second),
	}
	t.Cleanup(func() {
		_ = old.Close()
		_ = d.Close()
	})

	next, err := d.getDialer(context.Background(), true)
	if err != nil {
		t.Fatalf("refresh expired dialer: %v", err)
	}
	if next == old {
		t.Fatal("expired adapter was not replaced")
	}
	if old.closed.Load() {
		t.Fatal("expired adapter was closed while live streams may still reference it")
	}
}

func TestPreserveImplicitAnyTLSServerName(t *testing.T) {
	tests := []struct {
		name      string
		relayType string
		sni       string
		want      string
	}{
		{name: "implicit AnyTLS SNI", relayType: "anytls", want: "relay.example"},
		{name: "mixed-case AnyTLS type", relayType: "AnyTLS", want: "relay.example"},
		{name: "explicit AnyTLS SNI", relayType: "anytls", sni: "front.example", want: "front.example"},
		{name: "other relay type", relayType: "trojan", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapping := map[string]any{}
			if tt.sni != "" {
				mapping["sni"] = tt.sni
			}
			preserveImplicitAnyTLSServerName(mapping, tt.relayType, "relay.example")
			got, _ := mapping["sni"].(string)
			if got != tt.want {
				t.Fatalf("sni = %q, want %q", got, tt.want)
			}
		})
	}
}
