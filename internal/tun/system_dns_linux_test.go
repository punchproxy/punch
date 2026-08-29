//go:build linux

package tun

import (
	"errors"
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

// The resolv.conf fallback runs exactly when systemd-resolved is broken, which
// is when /etc/resolv.conf is most likely a symlink into /run/systemd/resolve.
// Writing through that link would truncate a systemd-owned file.
func TestLinuxResolvConfDNSOverrideReplacesSymlinkInsteadOfWritingThrough(t *testing.T) {
	dir := t.TempDir()
	oldPath := linuxResolvConfPath
	linuxResolvConfPath = filepath.Join(dir, "resolv.conf")
	t.Cleanup(func() {
		linuxResolvConfPath = oldPath
	})

	target := filepath.Join(dir, "stub-resolv.conf")
	original := "nameserver 127.0.0.53\noptions edns0\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("write link target: %v", err)
	}
	if err := os.Symlink(target, linuxResolvConfPath); err != nil {
		t.Fatalf("symlink resolv.conf: %v", err)
	}

	states, err := linuxCurrentResolvConfDNS()
	if err != nil {
		t.Fatalf("linuxCurrentResolvConfDNS() error = %v", err)
	}
	if states[0].Link != target {
		t.Fatalf("recorded link = %q, want %q", states[0].Link, target)
	}

	if err := linuxApplyResolvConfDNSOverride(states, "198.18.0.2"); err != nil {
		t.Fatalf("linuxApplyResolvConfDNSOverride() error = %v", err)
	}
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read link target: %v", err)
	}
	if string(kept) != original {
		t.Fatalf("systemd-owned link target was overwritten: %q, want %q", string(kept), original)
	}
	if info, err := os.Lstat(linuxResolvConfPath); err != nil {
		t.Fatalf("lstat resolv.conf: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("resolv.conf is still a symlink after the override")
	}

	if err := linuxRestoreResolvConfDNS(states); err != nil {
		t.Fatalf("linuxRestoreResolvConfDNS() error = %v", err)
	}
	restored, err := os.Readlink(linuxResolvConfPath)
	if err != nil {
		t.Fatalf("resolv.conf was not restored as a symlink: %v", err)
	}
	if restored != target {
		t.Fatalf("restored link = %q, want %q", restored, target)
	}
}

func TestLinuxOverrideSystemDNSFallsBackToResolvConfWhenResolvedUnavailable(t *testing.T) {
	oldPath := linuxResolvConfPath
	oldLookPath := linuxLookPath
	oldRunCommand := linuxRunCommand
	linuxResolvConfPath = filepath.Join(t.TempDir(), "resolv.conf")
	linuxLookPath = func(file string) (string, error) {
		if file == "resolvectl" {
			return "/usr/bin/resolvectl", nil
		}
		return "", errors.New("unexpected binary lookup")
	}
	var calls [][]string
	linuxRunCommand = func(name string, args ...string) ([]byte, error) {
		if name != "resolvectl" {
			t.Fatalf("command = %q, want resolvectl", name)
		}
		calls = append(calls, append([]string{name}, args...))
		return []byte("Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: No such file or directory"), errors.New("exit status 1")
	}
	t.Cleanup(func() {
		linuxResolvConfPath = oldPath
		linuxLookPath = oldLookPath
		linuxRunCommand = oldRunCommand
	})

	original := "search example.test\nnameserver 1.1.1.1\noptions edns0\n"
	if err := os.WriteFile(linuxResolvConfPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write test resolv.conf: %v", err)
	}

	override, err := overrideSystemDNS("198.18.0.2", "punch0")
	if err != nil {
		t.Fatalf("overrideSystemDNS() error = %v", err)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"resolvectl", "dns", "punch0"}) {
		t.Fatalf("resolvectl calls = %#v, want initial dns inspection only", calls)
	}

	overridden, err := os.ReadFile(linuxResolvConfPath)
	if err != nil {
		t.Fatalf("read overridden resolv.conf: %v", err)
	}
	if got, want := linuxParseResolvConfNameservers(string(overridden)), []string{"198.18.0.2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overridden nameservers = %#v, want %#v", got, want)
	}

	if err := override.StopAndRestore(); err != nil {
		t.Fatalf("StopAndRestore() error = %v", err)
	}
	restored, err := os.ReadFile(linuxResolvConfPath)
	if err != nil {
		t.Fatalf("read restored resolv.conf: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("restored content = %q, want %q", string(restored), original)
	}
}

func TestLinuxCurrentSystemDNSFallsBackToResolvConfWhenResolvedUnavailable(t *testing.T) {
	oldPath := linuxResolvConfPath
	oldLookPath := linuxLookPath
	oldRunCommand := linuxRunCommand
	linuxResolvConfPath = filepath.Join(t.TempDir(), "resolv.conf")
	linuxLookPath = func(file string) (string, error) {
		if file == "resolvectl" {
			return "/usr/bin/resolvectl", nil
		}
		return "", errors.New("unexpected binary lookup")
	}
	linuxRunCommand = func(name string, args ...string) ([]byte, error) {
		if name != "resolvectl" || !reflect.DeepEqual(args, []string{"dns", "punch0"}) {
			t.Fatalf("command = %s %#v, want resolvectl dns punch0", name, args)
		}
		return []byte("Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: No such file or directory"), errors.New("exit status 1")
	}
	t.Cleanup(func() {
		linuxResolvConfPath = oldPath
		linuxLookPath = oldLookPath
		linuxRunCommand = oldRunCommand
	})

	if err := os.WriteFile(linuxResolvConfPath, []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatalf("write test resolv.conf: %v", err)
	}

	states, err := currentSystemDNS("punch0")
	if err != nil {
		t.Fatalf("currentSystemDNS() error = %v", err)
	}
	if len(states) != 1 || states[0].Name != linuxResolvConfPath {
		t.Fatalf("states = %#v, want resolv.conf state", states)
	}
	if got, want := states[0].Servers, []string{"9.9.9.9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("servers = %#v, want %#v", got, want)
	}
}

func TestLinuxResolvectlServiceUnavailableOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "missing resolve socket",
			output: "Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: No such file or directory",
			want:   true,
		},
		{
			name:   "missing resolve service",
			output: "Unit dbus-org.freedesktop.resolve1.service not found.",
			want:   true,
		},
		{
			name:   "link error",
			output: "Failed to set DNS configuration: Link punch0 not known",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxResolvectlServiceUnavailableOutput(tt.output); got != tt.want {
				t.Fatalf("linuxResolvectlServiceUnavailableOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}
