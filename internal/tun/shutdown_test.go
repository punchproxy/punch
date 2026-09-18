package tun

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/config"
	singtun "github.com/sagernet/sing-tun"
)

type shutdownTun struct {
	singtun.Tun
	closes int
}

func (t *shutdownTun) Close() error {
	t.closes++
	return nil
}

func TestRestoreSystemNetworkDoesNotWaitForHandlers(t *testing.T) {
	for _, restoreErr := range []error{nil, errors.New("DNS restore failed")} {
		h := newHandler(nil, nil, nil, nil, nil)
		if !h.beginActivity() {
			t.Fatal("could not start handler")
		}
		device := &shutdownTun{}
		restores := 0
		routeMonitor := newInterfaceRouteMonitor(nil, netip.Prefix{}, "", nil, nil)
		dnsOverride := newSystemDNSOverride("198.18.0.2", nil,
			func() ([]systemDNSState, error) { return nil, nil },
			func([]systemDNSState, string) error { return nil },
			func([]systemDNSState) error {
				select {
				case <-routeMonitor.done:
				default:
					t.Error("route monitor still running during restoration")
				}
				restores++
				return restoreErr
			},
		)
		e := &Engine{started: true, tunnel: h, tunIf: device, dnsOverride: dnsOverride, routeMonitor: routeMonitor}
		done := make(chan error, 1)
		go func() { done <- e.RestoreSystemNetwork() }()
		select {
		case err := <-done:
			if !errors.Is(err, restoreErr) {
				t.Errorf("restore error = %v, want %v", err, restoreErr)
			}
		case <-time.After(time.Second):
			h.endActivity()
			t.Fatal("network restoration waited for an active handler")
		}
		// Repeated cleanup must not close monitor channels or the device twice.
		if err := e.RestoreSystemNetwork(); err != nil {
			t.Errorf("repeated restoration: %v", err)
		}
		if restores != 1 || device.closes != 1 {
			t.Errorf("DNS restores = %d, TUN closes = %d, want one each", restores, device.closes)
		}
		if err := e.ApplyConfig(config.TUN{}); !errors.Is(err, net.ErrClosed) {
			t.Errorf("configuration accepted during shutdown: %v", err)
		}
		if err := e.Start(); !errors.Is(err, net.ErrClosed) {
			t.Errorf("restart accepted during shutdown: %v", err)
		}
		if h.beginActivity() {
			h.endActivity()
			t.Error("new handler accepted during shutdown")
		}
		h.endActivity()
		if err := e.Stop(); err != nil {
			t.Errorf("stop after restoration: %v", err)
		}
	}
}
