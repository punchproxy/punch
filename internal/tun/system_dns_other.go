//go:build !darwin && !linux && !windows

package tun

import "fmt"

func overrideSystemDNS(_, _ string) (*systemDNSOverride, error) {
	return nil, fmt.Errorf("automatic system DNS override is not supported on this platform")
}

func currentSystemDNS(_ string) ([]systemDNSState, error) {
	return nil, fmt.Errorf("system DNS inspection is not supported on this platform")
}
