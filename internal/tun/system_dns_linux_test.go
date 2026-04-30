//go:build linux

package tun

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLinuxParseResolvConfNameservers(t *testing.T) {
	content := `
# resolv.conf
search example.test
nameserver 1.1.1.1
nameserver 2001:4860:4860::8888 # google
options edns0
; ignored
`
	got := linuxParseResolvConfNameservers(content)
	want := []string{"1.1.1.1", "2001:4860:4860::8888"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %#v, want %#v", got, want)
	}
}

func TestLinuxResolvConfDNSOverrideRestoresOriginalContent(t *testing.T) {
	oldPath := linuxResolvConfPath
	linuxResolvConfPath = filepath.Join(t.TempDir(), "resolv.conf")
	t.Cleanup(func() {
		linuxResolvConfPath = oldPath
	})

	original := "search example.test\nnameserver 1.1.1.1\noptions edns0\n"
	if err := os.WriteFile(linuxResolvConfPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write test resolv.conf: %v", err)
	}

	states, err := linuxCurrentResolvConfDNS()
	if err != nil {
		t.Fatalf("linuxCurrentResolvConfDNS() error = %v", err)
	}
	if got, want := states[0].Servers, []string{"1.1.1.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("servers = %#v, want %#v", got, want)
	}

	if err := linuxApplyResolvConfDNSOverride(states, "198.18.0.2"); err != nil {
		t.Fatalf("linuxApplyResolvConfDNSOverride() error = %v", err)
	}
	overridden, err := os.ReadFile(linuxResolvConfPath)
	if err != nil {
		t.Fatalf("read overridden resolv.conf: %v", err)
	}
	if got, want := linuxParseResolvConfNameservers(string(overridden)), []string{"198.18.0.2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overridden nameservers = %#v, want %#v", got, want)
	}

	if err := linuxRestoreResolvConfDNS(states); err != nil {
		t.Fatalf("linuxRestoreResolvConfDNS() error = %v", err)
	}
	restored, err := os.ReadFile(linuxResolvConfPath)
	if err != nil {
		t.Fatalf("read restored resolv.conf: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("restored content = %q, want %q", string(restored), original)
	}
}
