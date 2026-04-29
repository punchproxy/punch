//go:build !darwin && !linux

package tun

import "net/netip"

func cleanupRoutes(_ []netip.Prefix, _ netip.Prefix) error {
	return nil
}

func configureInterfaceRoutes(_ []netip.Prefix, _ netip.Prefix, _ string) error {
	return nil
}
