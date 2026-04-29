//go:build darwin

package tun

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

func cleanupRoutes(routes []netip.Prefix, _ netip.Prefix) error {
	if len(routes) == 0 {
		return nil
	}

	var firstErr error
	for _, prefix := range routes {
		cmd := darwinDeleteRouteCommand(prefix)
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

func configureInterfaceRoutes(routes []netip.Prefix, _ netip.Prefix, iface string) error {
	if iface == "" || len(routes) == 0 {
		return nil
	}

	if out, err := exec.Command("ifconfig", iface, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring interface %s up: %s: %w", iface, string(out), err)
	}

	if err := cleanupRoutes(routes, netip.Prefix{}); err != nil {
		return err
	}

	var firstErr error
	for _, prefix := range routes {
		cmd := darwinAddInterfaceRouteCommand(prefix, iface)
		if out, err := cmd.CombinedOutput(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("add route %s via %s: %s: %w", prefix, iface, string(out), err)
			}
		}
	}
	return firstErr
}

func darwinDeleteRouteCommand(prefix netip.Prefix) *exec.Cmd {
	if prefix.Addr().Is6() {
		return exec.Command("route", "-n", "delete", "-inet6", prefix.String())
	}
	return exec.Command("route", "-n", "delete", "-net", prefix.String())
}

func darwinAddInterfaceRouteCommand(prefix netip.Prefix, iface string) *exec.Cmd {
	if prefix.Addr().Is6() {
		return exec.Command("route", "-n", "add", "-inet6", prefix.String(), "-interface", iface)
	}
	return exec.Command("route", "-n", "add", "-net", prefix.String(), "-interface", iface)
}

func isIgnorableRouteDeleteError(output string, err error) bool {
	msg := strings.ToLower(output + " " + err.Error())
	return strings.Contains(msg, "not in table") ||
		strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "no such route")
}
