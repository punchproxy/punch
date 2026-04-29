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

func configureInterfaceRoutes(_ []netip.Prefix, _ netip.Prefix, _ string) error {
	return nil
}

func isIgnorableRouteDeleteError(output string, err error) bool {
	msg := strings.ToLower(output + " " + err.Error())
	return strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "cannot find device") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "not found")
}
