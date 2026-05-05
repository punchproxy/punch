//go:build linux

package tun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	linuxResolvConfPath = "/etc/resolv.conf"
	linuxLookPath       = exec.LookPath
	linuxRunCommand     = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
)

type linuxResolvectlError struct {
	args   []string
	output string
	err    error
}

func (e *linuxResolvectlError) Error() string {
	if e.output == "" {
		return fmt.Sprintf("resolvectl %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("resolvectl %s: %s: %v", strings.Join(e.args, " "), e.output, e.err)
}

func (e *linuxResolvectlError) Unwrap() error {
	return e.err
}

func overrideSystemDNS(serverIP, iface string) (*systemDNSOverride, error) {
	if iface == "" {
		return nil, fmt.Errorf("missing tun interface name")
	}
	useResolved, err := linuxHasResolvectl()
	if err != nil {
		return nil, err
	}
	if !useResolved {
		return linuxOverrideResolvConfDNS(serverIP)
	}

	states, err := linuxCurrentResolvedDNS(iface)
	if err != nil {
		if linuxShouldUseResolvConfFallback(err) {
			return linuxOverrideResolvConfDNS(serverIP)
		}
		return nil, err
	}
	if len(states) == 0 {
		states = []systemDNSState{{Name: iface, Empty: true}}
	}
	if err := linuxApplyDNSOverride(states, serverIP); err != nil {
		if linuxShouldUseResolvConfFallback(err) {
			return linuxOverrideResolvConfDNS(serverIP)
		}
		return nil, err
	}

	return newSystemDNSOverride(serverIP, states, func() ([]systemDNSState, error) {
		return linuxCurrentResolvedDNS(iface)
	}, linuxApplyDNSOverride, linuxRestoreDNS), nil
}

func currentSystemDNS(iface string) ([]systemDNSState, error) {
	if iface == "" {
		return nil, nil
	}
	useResolved, err := linuxHasResolvectl()
	if err != nil {
		return nil, err
	}
	if !useResolved {
		return linuxCurrentResolvConfDNS()
	}
	states, err := linuxCurrentResolvedDNS(iface)
	if err != nil {
		if linuxShouldUseResolvConfFallback(err) {
			return linuxCurrentResolvConfDNS()
		}
		return nil, err
	}
	return states, nil
}

func linuxHasResolvectl() (bool, error) {
	if _, err := linuxLookPath("resolvectl"); err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			return false, fmt.Errorf("look up resolvectl: %w", err)
		}
		return false, nil
	}
	return true, nil
}

func linuxCurrentResolvedDNS(iface string) ([]systemDNSState, error) {
	text, err := linuxRunResolvectl("dns", iface)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return []systemDNSState{{Name: iface, Empty: true}}, nil
	}
	var servers []string
	for _, line := range strings.Split(text, "\n") {
		if _, rest, ok := strings.Cut(line, ":"); ok {
			for _, field := range strings.Fields(rest) {
				servers = append(servers, field)
			}
		}
	}
	return []systemDNSState{{Name: iface, Servers: servers, Empty: len(servers) == 0}}, nil
}

func linuxOverrideResolvConfDNS(serverIP string) (*systemDNSOverride, error) {
	states, err := linuxCurrentResolvConfDNS()
	if err != nil {
		return nil, err
	}
	if err := linuxApplyResolvConfDNSOverride(states, serverIP); err != nil {
		return nil, err
	}
	return newSystemDNSOverride(serverIP, states, func() ([]systemDNSState, error) {
		return linuxCurrentResolvConfDNS()
	}, linuxApplyResolvConfDNSOverride, linuxRestoreResolvConfDNS), nil
}

func linuxApplyDNSOverride(states []systemDNSState, serverIP string) error {
	for _, state := range states {
		if state.Name == "" {
			continue
		}
		if err := linuxSetPunchDNS(state.Name, serverIP); err != nil {
			return err
		}
	}
	return nil
}

func linuxSetPunchDNS(iface, serverIP string) error {
	steps := [][]string{
		{"dns", iface, serverIP},
		{"domain", iface, "~."},
		{"default-route", iface, "yes"},
	}
	for _, args := range steps {
		if _, err := linuxRunResolvectl(args...); err != nil {
			return err
		}
	}
	return nil
}

func linuxRestoreDNS(states []systemDNSState) error {
	var firstErr error
	for _, state := range states {
		if state.Name == "" {
			continue
		}
		if state.Empty || len(state.Servers) == 0 {
			if _, err := linuxRunResolvectl("revert", state.Name); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		args := append([]string{"dns", state.Name}, state.Servers...)
		if _, err := linuxRunResolvectl(args...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func linuxRunResolvectl(args ...string) (string, error) {
	out, err := linuxRunCommand("resolvectl", args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", &linuxResolvectlError{
			args:   append([]string(nil), args...),
			output: text,
			err:    err,
		}
	}
	return text, nil
}

func linuxShouldUseResolvConfFallback(err error) bool {
	var resolvectlErr *linuxResolvectlError
	if !errors.As(err, &resolvectlErr) {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	return linuxResolvectlServiceUnavailableOutput(resolvectlErr.output)
}

func linuxResolvectlServiceUnavailableOutput(output string) bool {
	text := strings.ToLower(output)
	if strings.Contains(text, "/run/systemd/resolve") ||
		strings.Contains(text, "systemd-resolved is not running") {
		return true
	}
	if strings.Contains(text, "sd_bus_open_system") && strings.Contains(text, "no such file") {
		return true
	}
	if strings.Contains(text, "org.freedesktop.resolve1") || strings.Contains(text, "resolve1.service") {
		return strings.Contains(text, "not found") ||
			strings.Contains(text, "not provided") ||
			strings.Contains(text, "could not activate") ||
			strings.Contains(text, "activation request failed")
	}
	return false
}

func linuxCurrentResolvConfDNS() ([]systemDNSState, error) {
	content, err := os.ReadFile(linuxResolvConfPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", linuxResolvConfPath, err)
	}
	servers := linuxParseResolvConfNameservers(string(content))
	return []systemDNSState{{
		Name:    linuxResolvConfPath,
		Servers: servers,
		Empty:   len(servers) == 0,
		Content: string(content),
	}}, nil
}

func linuxApplyResolvConfDNSOverride(states []systemDNSState, serverIP string) error {
	content := "# Generated by Punch. Original resolv.conf will be restored when Punch stops.\n" +
		"nameserver " + serverIP + "\n"
	if err := os.WriteFile(linuxResolvConfPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", linuxResolvConfPath, err)
	}
	return nil
}

func linuxRestoreResolvConfDNS(states []systemDNSState) error {
	content := ""
	for _, state := range states {
		if state.Name == linuxResolvConfPath {
			content = state.Content
			break
		}
	}
	if err := os.WriteFile(linuxResolvConfPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("restore %s: %w", linuxResolvConfPath, err)
	}
	return nil
}

func linuxParseResolvConfNameservers(content string) []string {
	var servers []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers
}
