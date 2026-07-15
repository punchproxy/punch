package relay

import (
	"context"
	"testing"
)

func TestUDPTransportSkipsTCPProbe(t *testing.T) {
	// No mapping and no resolver: any attempt to actually probe would error,
	// so a nil error proves the probe was skipped.
	for _, relayType := range []string{"hysteria", "hysteria2", "tuic", "wireguard", "Hysteria2", "Tuic"} {
		d := &LazyRelayDialer{name: "udp-relay", relayType: relayType}
		latency, err := d.TCPConnectLatency(context.Background())
		if err != nil || latency != 0 {
			t.Fatalf("TCPConnectLatency(%s) = %v, %v, want 0, nil", relayType, latency, err)
		}
	}
	d := &LazyRelayDialer{name: "tcp-relay", relayType: "trojan"}
	if _, err := d.TCPConnectLatency(context.Background()); err == nil {
		t.Fatal("trojan relay without mapping should attempt the probe and fail")
	}
}

func TestResolveProbeAddr(t *testing.T) {
	ctx := context.Background()
	if got, err := resolveProbeAddr(ctx, "203.0.113.7:443"); err != nil || got != "203.0.113.7:443" {
		t.Fatalf("resolveProbeAddr(ip) = %q, %v, want passthrough", got, err)
	}
	if got, err := resolveProbeAddr(ctx, "[2001:db8::1]:443"); err != nil || got != "[2001:db8::1]:443" {
		t.Fatalf("resolveProbeAddr(ipv6) = %q, %v, want passthrough", got, err)
	}
	got, err := resolveProbeAddr(ctx, "localhost:80")
	if err != nil {
		t.Fatalf("resolveProbeAddr(localhost) error = %v", err)
	}
	if got != "127.0.0.1:80" && got != "[::1]:80" {
		t.Fatalf("resolveProbeAddr(localhost) = %q, want loopback with port 80", got)
	}
	if _, err := resolveProbeAddr(ctx, "no-port-here"); err == nil {
		t.Fatal("address without port should error")
	}
}
