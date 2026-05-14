//go:build windows

package tun

import (
	"reflect"
	"strings"
	"testing"
)

func TestWindowsParseDNSStates(t *testing.T) {
	got := windowsParseDNSStates("Wi-Fi|1.1.1.1,8.8.8.8\r\nEthernet|\r\nignored\r\n")
	want := []systemDNSState{
		{Name: "Wi-Fi", Servers: []string{"1.1.1.1", "8.8.8.8"}},
		{Name: "Ethernet", Empty: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %#v, want %#v", got, want)
	}
}

func TestWindowsCurrentDNSScriptUsesActiveAdapters(t *testing.T) {
	script := windowsCurrentDNSScript("punch0")
	for _, want := range []string{
		"Get-NetAdapter",
		"$_.Status -eq 'Up'",
		"Get-DnsClientServerAddress -InterfaceAlias $_.Name",
		"$_.Name -ine $excluded",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
	if strings.Contains(script, "InterfaceOperationalStatus") {
		t.Fatalf("script still depends on DNS client InterfaceOperationalStatus: %s", script)
	}
}

func TestWindowsPowerShellCommandForcesUTF8Output(t *testing.T) {
	alias := "\u4ee5\u592a\u7f51 2"
	cmd := windowsPowerShellCommand("Write-Output '" + alias + "'")
	script := cmd.Args[len(cmd.Args)-1]
	for _, want := range []string{
		"$OutputEncoding = [System.Text.UTF8Encoding]::new()",
		"[Console]::OutputEncoding = $OutputEncoding",
		"Write-Output '" + alias + "'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
}
