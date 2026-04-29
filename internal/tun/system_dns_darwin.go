//go:build darwin

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func overrideSystemDNS(serverIP, _ string) (func() error, error) {
	if _, err := exec.LookPath("networksetup"); err != nil {
		return nil, fmt.Errorf("networksetup not found: %w", err)
	}

	services, err := darwinNetworkServices()
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no active network services found")
	}

	snapshots := make([]darwinDNSState, 0, len(services))
	for _, service := range services {
		servers, empty, err := darwinGetDNSServers(service)
		if err != nil {
			return nil, fmt.Errorf("get dns servers for %s: %w", service, err)
		}
		snapshots = append(snapshots, darwinDNSState{
			Service: service,
			Servers: servers,
			Empty:   empty,
		})
		if out, err := exec.Command("networksetup", "-setdnsservers", service, serverIP).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("set dns servers for %s: %s: %w", service, strings.TrimSpace(string(out)), err)
		}
	}

	return func() error {
		var firstErr error
		for _, state := range snapshots {
			args := []string{"-setdnsservers", state.Service}
			if state.Empty || len(state.Servers) == 0 {
				args = append(args, "empty")
			} else {
				args = append(args, state.Servers...)
			}
			if out, err := exec.Command("networksetup", args...).CombinedOutput(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("restore dns servers for %s: %s: %w", state.Service, strings.TrimSpace(string(out)), err)
			}
		}
		return firstErr
	}, nil
}

type darwinDNSState struct {
	Service string
	Servers []string
	Empty   bool
}

func darwinNetworkServices() ([]string, error) {
	out, err := exec.Command("networksetup", "-listallnetworkservices").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list network services: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var services []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" ||
			strings.HasPrefix(line, "An asterisk") ||
			strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	return services, nil
}

func darwinGetDNSServers(service string) ([]string, bool, error) {
	out, err := exec.Command("networksetup", "-getdnsservers", service).CombinedOutput()
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(text, "There aren't any DNS Servers set") {
		return nil, true, nil
	}

	var servers []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			servers = append(servers, line)
		}
	}
	return servers, false, nil
}
