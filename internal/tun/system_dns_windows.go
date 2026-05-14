//go:build windows

package tun

import (
	"fmt"
	"strings"
)

func overrideSystemDNS(serverIP, iface string) (*systemDNSOverride, error) {
	states, err := currentSystemDNS(iface)
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
		return currentSystemDNS(iface)
	}, windowsApplyDNSOverride, windowsRestoreDNS), nil
}

func currentSystemDNS(iface string) ([]systemDNSState, error) {
	out, err := windowsPowerShellCommand(windowsCurrentDNSScript(iface)).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list dns client server addresses: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return windowsParseDNSStates(string(out)), nil
}

func windowsCurrentDNSScript(excludedIface string) string {
	return fmt.Sprintf(
		`$ErrorActionPreference='Stop'; $excluded = %s; Get-NetAdapter | Where-Object { $_.Status -eq 'Up' -and $_.Name -notlike 'Loopback*' -and ($excluded -eq '' -or $_.Name -ine $excluded) } | ForEach-Object { $dns = Get-DnsClientServerAddress -InterfaceAlias $_.Name -AddressFamily IPv4 -ErrorAction Stop; $servers = if ($dns.ServerAddresses) { $dns.ServerAddresses -join ',' } else { '' }; Write-Output ($_.Name + '|' + $servers) }`,
		windowsPowerShellQuote(excludedIface),
	)
}

func windowsParseDNSStates(output string) []systemDNSState {
	var states []systemDNSState
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
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
	return states
}

func windowsApplyDNSOverride(states []systemDNSState, serverIP string) error {
	for _, state := range states {
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ServerAddresses @(%q)`, state.Name, serverIP)
		if out, err := windowsPowerShellCommand(script).CombinedOutput(); err != nil {
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
		if out, err := windowsPowerShellCommand(script).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore dns servers for %s: %s: %w", state.Name, strings.TrimSpace(string(out)), err)
		}
	}
	return firstErr
}
