//go:build linux

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
		cmd := exec.Command("ip", "route", "del", prefix.String())
		if out, err := cmd.CombinedOutput(); err != nil {
			if isIgnorableRouteDeleteError(string(out), err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("delete route %s: %s: %w", prefix, string(out), err)
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
		currentIface, ok, err := linuxRouteInterface(prefix)
		if err != nil {
			return nil, err
		}
		if !ok {
			currentIface = "missing"
		}
		if !ok || currentIface != iface {
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
		cmd := linuxReplaceRouteCommand(prefix, iface)
		if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("replace route %s via %s: %s: %w", prefix, iface, string(out), err)
		}
	}
	return firstErr
}

func linuxRouteInterface(prefix netip.Prefix) (string, bool, error) {
	cmd := linuxShowRouteCommand(prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if isIgnorableRouteDeleteError(string(out), err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("show route %s: %s: %w", prefix, string(out), err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", false, nil
	}
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "dev" {
			return fields[i+1], true, nil
		}
	}
	return "", false, nil
}

func linuxShowRouteCommand(prefix netip.Prefix) *exec.Cmd {
	if prefix.Addr().Is6() {
		return exec.Command("ip", "-6", "route", "show", prefix.String())
	}
	return exec.Command("ip", "-4", "route", "show", prefix.String())
}

func linuxReplaceRouteCommand(prefix netip.Prefix, iface string) *exec.Cmd {
	if prefix.Addr().Is6() {
		return exec.Command("ip", "-6", "route", "replace", prefix.String(), "dev", iface)
	}
	return exec.Command("ip", "-4", "route", "replace", prefix.String(), "dev", iface)
}

func isIgnorableRouteDeleteError(output string, err error) bool {
	msg := strings.ToLower(output + " " + err.Error())
	return strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "cannot find device") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "not found")
}
