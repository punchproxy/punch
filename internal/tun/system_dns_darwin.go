//go:build darwin

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func overrideSystemDNS(serverIP, _ string) (*systemDNSOverride, error) {
	if _, err := exec.LookPath("networksetup"); err != nil {
		return nil, fmt.Errorf("networksetup not found: %w", err)
	}

	states, err := currentSystemDNS("")
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("no active network services found")
	}
	if err := darwinApplyDNSOverride(states, serverIP); err != nil {
		return nil, err
	}

	return newSystemDNSOverride(serverIP, states, func() ([]systemDNSState, error) {
		return currentSystemDNS("")
	}, darwinApplyDNSOverride, darwinRestoreDNS), nil
}

func currentSystemDNS(_ string) ([]systemDNSState, error) {
	services, err := darwinNetworkServices()
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no active network services found")
	}

	states := make([]systemDNSState, 0, len(services))
	for _, service := range services {
		servers, empty, err := darwinGetDNSServers(service)
		if err != nil {
			return nil, fmt.Errorf("get dns servers for %s: %w", service, err)
		}
		states = append(states, systemDNSState{
			Name:    service,
			Servers: servers,
			Empty:   empty,
		})
	}
	return states, nil
}

func darwinApplyDNSOverride(states []systemDNSState, serverIP string) error {
	for _, state := range states {
		if out, err := exec.Command("networksetup", "-setdnsservers", state.Name, serverIP).CombinedOutput(); err != nil {
			return fmt.Errorf("set dns servers for %s: %s: %w", state.Name, strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func darwinRestoreDNS(states []systemDNSState) error {
	var firstErr error
	for _, state := range states {
		args := []string{"-setdnsservers", state.Name}
		if state.Empty || len(state.Servers) == 0 {
			args = append(args, "empty")
		} else {
			args = append(args, state.Servers...)
		}
		if out, err := exec.Command("networksetup", args...).CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore dns servers for %s: %s: %w", state.Name, strings.TrimSpace(string(out)), err)
		}
	}
	return firstErr
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
