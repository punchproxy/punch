package tun

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestInterfaceRouteMonitorReappliesMissingRoutes(t *testing.T) {
	routes := []netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("1.1.1.0/24"),
	}
	var appliedRoutes []netip.Prefix
	var appliedIface string

	monitor := newInterfaceRouteMonitor(
		routes,
		netip.MustParsePrefix("198.18.0.0/30"),
		"utun9",
		func(gotRoutes []netip.Prefix, iface string) ([]interfaceRouteMismatch, error) {
			if iface != "utun9" {
				t.Fatalf("iface = %q, want utun9", iface)
			}
			if !reflect.DeepEqual(gotRoutes, routes) {
				t.Fatalf("routes = %#v, want %#v", gotRoutes, routes)
			}
			return []interfaceRouteMismatch{{
				route:         gotRoutes[1],
				fromInterface: "en0",
				toInterface:   iface,
			}}, nil
		},
		func(gotRoutes []netip.Prefix, _ netip.Prefix, iface string) error {
			appliedRoutes = clonePrefixes(gotRoutes)
			appliedIface = iface
			return nil
		},
	)
	defer monitor.Stop()

	monitor.checkOnce()
	if !reflect.DeepEqual(appliedRoutes, routes) {
		t.Fatalf("applied routes = %#v, want %#v", appliedRoutes, routes)
	}
	if appliedIface != "utun9" {
		t.Fatalf("applied iface = %q, want utun9", appliedIface)
	}
}
