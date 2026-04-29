//go:build linux

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func overrideSystemDNS(serverIP, iface string) (func() error, error) {
	if iface == "" {
		return nil, fmt.Errorf("missing tun interface name")
	}
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return nil, fmt.Errorf("resolvectl not found: %w", err)
	}

	steps := [][]string{
		{"dns", iface, serverIP},
		{"domain", iface, "~."},
		{"default-route", iface, "yes"},
	}
	for _, args := range steps {
		if out, err := exec.Command("resolvectl", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("resolvectl %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
		}
	}

	return func() error {
		if out, err := exec.Command("resolvectl", "revert", iface).CombinedOutput(); err != nil {
			return fmt.Errorf("resolvectl revert %s: %s: %w", iface, strings.TrimSpace(string(out)), err)
		}
		return nil
	}, nil
}
