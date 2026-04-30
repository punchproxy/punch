package tun

import (
	"net/netip"
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
}
