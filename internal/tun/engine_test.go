package tun

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildTunAndDNSServerAddresses(t *testing.T) {
	fakeRange := netip.MustParsePrefix("198.18.0.0/15")

	tunAddress, err := buildTunAddress(fakeRange)
	if err != nil {
		t.Fatalf("buildTunAddress() error = %v", err)
	}
	if want := netip.MustParsePrefix("198.18.0.1/30"); tunAddress != want {
		t.Fatalf("tun address = %s, want %s", tunAddress, want)
	}

	dnsAddress, err := buildDNSServerAddress(fakeRange)
	if err != nil {
		t.Fatalf("buildDNSServerAddress() error = %v", err)
	}
	if want := netip.MustParseAddr("198.18.0.2"); dnsAddress != want {
		t.Fatalf("dns server address = %s, want %s", dnsAddress, want)
	}

	tun6Address, err := buildTunAddress(netip.MustParsePrefix("fdfe:dcba:9876::/64"))
	if err != nil {
		t.Fatalf("buildTunAddress(IPv6) error = %v", err)
	}
	if want := netip.MustParsePrefix("fdfe:dcba:9876::1/126"); tun6Address != want {
		t.Fatalf("tun ipv6 address = %s, want %s", tun6Address, want)
	}
}

func TestResolveTunDeviceNameFixedByPlatform(t *testing.T) {
	got := resolveTunDeviceName()
	if runtime.GOOS == "darwin" {
		if !strings.HasPrefix(got, "utun") {
			t.Fatalf("resolveTunDeviceName() = %q, want auto-selected utun name", got)
		}
		return
	}
	if got != "punch0" {
		t.Fatalf("resolveTunDeviceName() = %q, want punch0", got)
	}
}

func TestIgnorableTunCloseError(t *testing.T) {
	if !isIgnorableTunCloseError(errors.New("(no such process | delete route: 198.18.0.0/30: no such process)")) {
		t.Fatal("missing route during TUN close should be ignorable")
	}
	if isIgnorableTunCloseError(errors.New("delete route: permission denied")) {
		t.Fatal("permission errors during TUN close should not be ignorable")
	}
	if isIgnorableTunCloseError(errors.New("no such process")) {
		t.Fatal("unrelated no such process errors should not be ignorable")
	}
}

func TestBuildRouteAddressUsesProvidedRoutes(t *testing.T) {
	engine := &Engine{}
	got := engine.buildRouteAddress(
		[]string{"10.0.0.1/8"},
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fdfe:dcba:9876::/64"),
	)
	want := []netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fdfe:dcba:9876::/64"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestBuildCleanupRoutesNormalizesConfiguredRoutes(t *testing.T) {
	got := buildCleanupRoutes(
		[]netip.Prefix{
			netip.MustParsePrefix("2001:b28:f23d::1/48"),
			netip.MustParsePrefix("2001:b28:f23d::/48"),
			netip.MustParsePrefix("198.18.0.0/15"),
		},
	)
	want := []netip.Prefix{
		netip.MustParsePrefix("2001:b28:f23d::/48"),
		netip.MustParsePrefix("198.18.0.0/15"),
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestHasIPv6Route(t *testing.T) {
	if hasIPv6Route([]netip.Prefix{netip.MustParsePrefix("198.18.0.0/15")}) {
		t.Fatal("hasIPv6Route returned true for IPv4-only routes")
	}
	if !hasIPv6Route([]netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("2001:b28:f23d::/48"),
	}) {
		t.Fatal("hasIPv6Route returned false for routes containing IPv6")
	}
}

func TestBuildRouteAddressLoadsWhitespaceSeparatedRouteSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "routes.txt")
	if err := os.WriteFile(source, []byte("91.108.56.0/22 91.108.4.0/22 2001:b28:f23d::/48\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	engine := &Engine{}
	got := engine.buildRouteAddress(
		[]string{source},
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fdfe:dcba:9876::/64"),
	)
	want := []netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fdfe:dcba:9876::/64"),
		netip.MustParsePrefix("91.108.4.0/22"),
		netip.MustParsePrefix("91.108.56.0/22"),
		netip.MustParsePrefix("2001:b28:f23d::/48"),
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
