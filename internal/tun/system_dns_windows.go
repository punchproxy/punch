//go:build windows

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func overrideSystemDNS(serverIP, _ string) (*systemDNSOverride, error) {
	states, err := currentSystemDNS("")
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("no active adapters found")
	}
	if err := windowsApplyDNSOverride(states, serverIP); err != nil {
		return nil, err
	}

	return newSystemDNSOverride(serverIP, states, func() ([]systemDNSState, error) {
		return currentSystemDNS("")
	}, windowsApplyDNSOverride, windowsRestoreDNS), nil
}

func currentSystemDNS(_ string) ([]systemDNSState, error) {
	const listScript = `$ErrorActionPreference='Stop'; Get-DnsClientServerAddress -AddressFamily IPv4 | Where-Object { $_.InterfaceOperationalStatus -eq 'Up' -and $_.InterfaceAlias -notlike 'Loopback*' } | ForEach-Object { $servers = if ($_.ServerAddresses) { $_.ServerAddresses -join ',' } else { '' }; Write-Output ($_.InterfaceAlias + '|' + $servers) }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", listScript).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list dns client server addresses: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var states []systemDNSState
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		alias, servers, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		state := systemDNSState{Name: alias, Empty: strings.TrimSpace(servers) == ""}
		if strings.TrimSpace(servers) != "" {
			state.Servers = strings.Split(servers, ",")
		}
		states = append(states, state)
	}
	return states, nil
}

func windowsApplyDNSOverride(states []systemDNSState, serverIP string) error {
	for _, state := range states {
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ServerAddresses @(%q)`, state.Name, serverIP)
		if out, err := exec.Command("powershell", "-NoProfile", "-Command", script).CombinedOutput(); err != nil {
			return fmt.Errorf("set dns servers for %s: %s: %w", state.Name, strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func windowsRestoreDNS(states []systemDNSState) error {
	var firstErr error
	for _, state := range states {
		var script string
		if state.Empty || len(state.Servers) == 0 {
			script = fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ResetServerAddresses`, state.Name)
		} else {
			quoted := make([]string, 0, len(state.Servers))
			for _, server := range state.Servers {
				quoted = append(quoted, fmt.Sprintf("%q", server))
			}
			script = fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ServerAddresses @(%s)`, state.Name, strings.Join(quoted, ","))
		}
		if out, err := exec.Command("powershell", "-NoProfile", "-Command", script).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore dns servers for %s: %s: %w", state.Name, strings.TrimSpace(string(out)), err)
		}
	}
	return firstErr
}
