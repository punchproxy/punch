//go:build darwin

package tun

import (
	"net/netip"
	"testing"
)

func TestParseDarwinNetstatRouteLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantPrefix netip.Prefix
		wantIface  string
		wantOK     bool
	}{
		{
			name:       "abbreviated IPv4 prefix",
			line:       "198.18.0/15        utun13             USc                utun13",
			wantPrefix: netip.MustParsePrefix("198.18.0.0/15"),
			wantIface:  "utun13",
			wantOK:     true,
		},
		{
			name:       "abbreviated IPv4 connected route",
			line:       "198.18.0/30        utun13             USc                utun13",
			wantPrefix: netip.MustParsePrefix("198.18.0.0/30"),
			wantIface:  "utun13",
			wantOK:     true,
		},
		{
			name:       "IPv6 prefix",
			line:       "2001:67c:4e8::/48                  link#42                                 UCS                utun13",
			wantPrefix: netip.MustParsePrefix("2001:67c:4e8::/48"),
			wantIface:  "utun13",
			wantOK:     true,
		},
		{
			name:       "implicit IPv4 /24",
			line:       "192.168.0          link#13            UCS                  en14",
			wantPrefix: netip.MustParsePrefix("192.168.0.0/24"),
			wantIface:  "en14",
			wantOK:     true,
		},
		{
			name:       "implicit IPv4 /32",
			line:       "192.168.0.1        24:5a:5f:71:3f:55  UHLWIir              en14   1177",
			wantPrefix: netip.MustParsePrefix("192.168.0.1/32"),
			wantIface:  "en14",
			wantOK:     true,
		},
		{
			name:   "header",
			line:   "Destination        Gateway            Flags               Netif Expire",
			wantOK: false,
		},
		{
			name:   "default",
			line:   "default            192.168.0.1        UGScg                en14",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrefix, gotIface, gotOK := parseDarwinNetstatRouteLine(tt.line)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotPrefix != tt.wantPrefix {
				t.Fatalf("prefix = %s, want %s", gotPrefix, tt.wantPrefix)
			}
			if gotIface != tt.wantIface {
				t.Fatalf("iface = %q, want %q", gotIface, tt.wantIface)
			}
		})
	}
}
