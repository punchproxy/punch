//go:build !darwin && !linux && !windows

package tun

import "fmt"

func overrideSystemDNS(_, _ string) (func() error, error) {
	return nil, fmt.Errorf("automatic system DNS override is not supported on this platform")
}
