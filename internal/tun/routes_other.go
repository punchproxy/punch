//go:build !darwin && !linux && !windows

package tun

import "net/netip"

func cleanupRoutes(_ []netip.Prefix, _ netip.Prefix) error {
	return nil
}

func missingInterfaceRoutes(_ []netip.Prefix, _ string) ([]interfaceRouteMismatch, error) {
	return nil, nil
}

func configureInterfaceRoutes(_ []netip.Prefix, _ netip.Prefix, _ string) error {
	return nil
}
