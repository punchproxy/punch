package dns

import (
	"io"
	"net/netip"
	"strings"
	"testing"
)

type stringSourceOpener string

func (o stringSourceOpener) Open(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(o))), nil
}

func TestIPSetContainsSourceUsesMostSpecificCIDR(t *testing.T) {
	set := NewIPSet()
	set.AddWithSource(netip.MustParsePrefix("10.0.0.0/8"), "wide")
	set.AddWithSource(netip.MustParsePrefix("10.1.0.0/16"), "specific")

	if source := set.ContainsSource(netip.MustParseAddr("10.1.2.3")); source != "specific" {
		t.Fatalf("ContainsSource() = %q, want specific", source)
	}
	if source := set.ContainsSource(netip.MustParseAddr("10.2.3.4")); source != "wide" {
		t.Fatalf("ContainsSource() = %q, want wide", source)
	}
	if set.Contains(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("Contains() matched IP outside configured CIDRs")
	}
}

func TestLoadIPSetLoadsCIDRsAndSingleIPs(t *testing.T) {
	set := NewIPSet()
	count, err := LoadIPSet("test-source", set, stringSourceOpener(`
10.0.0.0/8
192.0.2.1
2001:db8::/32 # comment
invalid
`))
	if err != nil {
		t.Fatalf("LoadIPSet() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("LoadIPSet() count = %d, want 3", count)
	}
	if !set.Contains(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("Contains() did not match loaded single IP")
	}
	if !set.Contains(netip.MustParseAddr("2001:db8::1")) {
		t.Fatal("Contains() did not match loaded IPv6 CIDR")
	}
}
