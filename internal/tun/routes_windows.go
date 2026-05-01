//go:build windows

package tun

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

func cleanupRoutes(routes []netip.Prefix, _ netip.Prefix) error {
	var firstErr error
	for _, prefix := range routes {
		cmd := windowsRemoveRouteCommand(prefix)
		if out, err := cmd.CombinedOutput(); err != nil {
			if isIgnorableRouteDeleteError(string(out), err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("delete route %s: %s: %w", prefix, strings.TrimSpace(string(out)), err)
			}
		}
	}
	return firstErr
}

func missingInterfaceRoutes(routes []netip.Prefix, iface string) ([]interfaceRouteMismatch, error) {
	if iface == "" || len(routes) == 0 {
		return nil, nil
	}

	var mismatches []interfaceRouteMismatch
	for _, prefix := range routes {
		currentIface, ok, err := windowsRouteInterface(prefix)
		if err != nil {
			return nil, err
		}
		if !ok {
			currentIface = "missing"
		}
		if !ok || !strings.EqualFold(currentIface, iface) {
			mismatches = append(mismatches, interfaceRouteMismatch{
				route:         prefix.Masked(),
				fromInterface: currentIface,
				toInterface:   iface,
			})
		}
	}
	return mismatches, nil
}

func configureInterfaceRoutes(routes []netip.Prefix, _ netip.Prefix, iface string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}

	var firstErr error
	for _, prefix := range routes {
		cmd := windowsReplaceRouteCommand(prefix, iface)
		if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("replace route %s via %s: %s: %w", prefix, iface, strings.TrimSpace(string(out)), err)
		}
	}
	return firstErr
}

func windowsRouteInterface(prefix netip.Prefix) (string, bool, error) {
	cmd := windowsGetRouteCommand(prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if isIgnorableRouteDeleteError(string(out), err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get route %s: %s: %w", prefix, strings.TrimSpace(string(out)), err)
	}
	iface := strings.TrimSpace(string(out))
	if iface == "" {
		return "", false, nil
	}
	return iface, true, nil
}

func windowsGetRouteCommand(prefix netip.Prefix) *exec.Cmd {
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; $route = Get-NetRoute -DestinationPrefix %s -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Sort-Object -Property RouteMetric,InterfaceMetric | Select-Object -First 1; if ($null -ne $route) { Write-Output $route.InterfaceAlias }`,
		windowsPowerShellQuote(prefix.String()),
	)
	return windowsPowerShellCommand(script)
}

func windowsRemoveRouteCommand(prefix netip.Prefix) *exec.Cmd {
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; Get-NetRoute -DestinationPrefix %s -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false`,
		windowsPowerShellQuote(prefix.String()),
	)
	return windowsPowerShellCommand(script)
}

func windowsReplaceRouteCommand(prefix netip.Prefix, iface string) *exec.Cmd {
	destination := windowsPowerShellQuote(prefix.String())
	script := fmt.Sprintf(
		`$ErrorActionPreference='Stop'; $destination = %s; Get-NetRoute -DestinationPrefix $destination -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false; New-NetRoute -DestinationPrefix $destination -InterfaceAlias %s -NextHop %s -RouteMetric 1 -PolicyStore ActiveStore | Out-Null`,
		destination,
		windowsPowerShellQuote(iface),
		windowsPowerShellQuote(windowsRouteNextHop(prefix)),
	)
	return windowsPowerShellCommand(script)
}

func windowsRouteNextHop(prefix netip.Prefix) string {
	if prefix.Addr().Is6() {
		return "::"
	}
	return "0.0.0.0"
}

func windowsPowerShellCommand(script string) *exec.Cmd {
	return exec.Command("powershell", "-NoProfile", "-Command", script)
}

func windowsPowerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func isIgnorableRouteDeleteError(output string, err error) bool {
	msg := strings.ToLower(output + " " + err.Error())
	return strings.Contains(msg, "no matching") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "cannot find") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "not exist")
}
