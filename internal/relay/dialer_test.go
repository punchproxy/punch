package relay

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type closeWriteTrackingConn struct {
	net.Conn
	called atomic.Bool
}

func (c *closeWriteTrackingConn) CloseWrite() error {
	c.called.Store(true)
	return nil
}

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
	if got := d.ResolvedAddr(); got != "192.0.2.2:443" {
		t.Fatalf("ResolvedAddr() = %q, want %q", got, "192.0.2.2:443")
	}
}

func TestLazyRelayResolvedAddrDoesNotTriggerResolution(t *testing.T) {
	var resolutions atomic.Int64
	d := &LazyRelayDialer{
		groupName: "main",
		name:      "relay",
		relayType: "trojan",
		addr:      "relay.example:443",
		resolver: func(context.Context, string, string) ([]netip.Addr, time.Time, error) {
			resolutions.Add(1)
			return []netip.Addr{netip.MustParseAddr("192.0.2.10")}, time.Now().Add(time.Minute), nil
		},
	}

	if got := d.ResolvedAddr(); got != "" {
		t.Fatalf("ResolvedAddr() before resolution = %q, want empty", got)
	}
	if got := resolutions.Load(); got != 0 {
		t.Fatalf("resolution count = %d, want 0", got)
	}
}

func TestRelayConnectionWrappersForwardCloseWrite(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	inner := &closeWriteTrackingConn{Conn: conn}
	wrapped := &relayTrackedConn{
		Conn:   &connWrapper{Conn: inner},
		health: &RelayHealth{},
	}

	if err := wrapped.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if !inner.called.Load() {
		t.Fatal("CloseWrite was not forwarded to the underlying relay connection")
	}
}

func TestRelayConnectionWrapperReportsUnsupportedCloseWrite(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	if err := (&connWrapper{Conn: conn}).CloseWrite(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("CloseWrite() error = %v, want errors.ErrUnsupported", err)
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
