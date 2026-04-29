//go:build windows

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func overrideSystemDNS(serverIP, _ string) (func() error, error) {
	const listScript = `$ErrorActionPreference='Stop'; Get-DnsClientServerAddress -AddressFamily IPv4 | Where-Object { $_.InterfaceOperationalStatus -eq 'Up' -and $_.InterfaceAlias -notlike 'Loopback*' } | ForEach-Object { $servers = if ($_.ServerAddresses) { $_.ServerAddresses -join ',' } else { '' }; Write-Output ($_.InterfaceAlias + '|' + $servers) }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", listScript).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list dns client server addresses: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var states []windowsDNSState
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		alias, servers, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		state := windowsDNSState{Alias: alias}
		if strings.TrimSpace(servers) != "" {
			state.Servers = strings.Split(servers, ",")
		}
		states = append(states, state)
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("no active adapters found")
	}

	for _, state := range states {
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ServerAddresses @(%q)`, state.Alias, serverIP)
		if out, err := exec.Command("powershell", "-NoProfile", "-Command", script).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("set dns servers for %s: %s: %w", state.Alias, strings.TrimSpace(string(out)), err)
		}
	}

	return func() error {
		var firstErr error
		for _, state := range states {
			var script string
			if len(state.Servers) == 0 {
				script = fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ResetServerAddresses`, state.Alias)
			} else {
				quoted := make([]string, 0, len(state.Servers))
				for _, server := range state.Servers {
					quoted = append(quoted, fmt.Sprintf("%q", server))
				}
				script = fmt.Sprintf(`$ErrorActionPreference='Stop'; Set-DnsClientServerAddress -InterfaceAlias %q -ServerAddresses @(%s)`, state.Alias, strings.Join(quoted, ","))
			}
			if out, err := exec.Command("powershell", "-NoProfile", "-Command", script).CombinedOutput(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("restore dns servers for %s: %s: %w", state.Alias, strings.TrimSpace(string(out)), err)
			}
		}
		return firstErr
	}, nil
}

type windowsDNSState struct {
	Alias   string
	Servers []string
}
