//go:build darwin

package tun

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
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

func missingInterfaceRoutes(routes []netip.Prefix, iface string) ([]interfaceRouteMismatch, error) {
	if iface == "" || len(routes) == 0 {
		return nil, nil
	}

	current, err := darwinRouteInterfaces(routes, iface)
	if err != nil {
		return nil, err
	}

	var mismatches []interfaceRouteMismatch
	for _, prefix := range routes {
		prefix = prefix.Masked()
		currentIface, ok := current[prefix]
		if !ok {
			currentIface = "missing"
		}
		if !ok || currentIface != iface {
			mismatches = append(mismatches, interfaceRouteMismatch{
				route:         prefix,
				fromInterface: currentIface,
				toInterface:   iface,
			})
		}
	}
	return mismatches, nil
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

func darwinRouteInterfaces(routes []netip.Prefix, iface string) (map[netip.Prefix]string, error) {
	wanted := make(map[netip.Prefix]struct{}, len(routes))
	needInet4 := false
	needInet6 := false
	for _, prefix := range routes {
		prefix = prefix.Masked()
		wanted[prefix] = struct{}{}
		if prefix.Addr().Is6() {
			needInet6 = true
		} else {
			needInet4 = true
		}
	}

	current := make(map[netip.Prefix]string, len(routes))
	if needInet4 {
		if err := readDarwinRouteInterfaces("inet", wanted, iface, current); err != nil {
			return nil, err
		}
	}
	if needInet6 {
		if err := readDarwinRouteInterfaces("inet6", wanted, iface, current); err != nil {
			return nil, err
		}
	}
	return current, nil
}

func readDarwinRouteInterfaces(family string, wanted map[netip.Prefix]struct{}, iface string, current map[netip.Prefix]string) error {
	cmd := darwinListRoutesCommand(family)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("list %s routes: %s: %w", family, strings.TrimSpace(string(out)), err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		prefix, routeIface, ok := parseDarwinNetstatRouteLine(line)
		if !ok {
			continue
		}
		if _, ok := wanted[prefix]; !ok {
			continue
		}
		if _, exists := current[prefix]; !exists || routeIface == iface {
			current[prefix] = routeIface
		}
	}
	return nil
}

func parseDarwinNetstatRouteLine(line string) (netip.Prefix, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return netip.Prefix{}, "", false
	}
	prefix, ok := parseDarwinNetstatDestination(fields[0])
	if !ok {
		return netip.Prefix{}, "", false
	}
	return prefix, fields[3], true
}

func parseDarwinNetstatDestination(destination string) (netip.Prefix, bool) {
	addrText, bitsText, hasBits := strings.Cut(destination, "/")
	if zoneIndex := strings.IndexByte(addrText, '%'); zoneIndex >= 0 {
		addrText = addrText[:zoneIndex]
	}

	bitLen := 128
	defaultBits := bitLen
	if !strings.Contains(addrText, ":") {
		expanded, octets, ok := expandDarwinIPv4Address(addrText)
		if !ok {
			return netip.Prefix{}, false
		}
		addrText = expanded
		bitLen = 32
		defaultBits = octets * 8
	}

	bits := defaultBits
	if hasBits {
		parsedBits, err := strconv.Atoi(bitsText)
		if err != nil || parsedBits < 0 || parsedBits > bitLen {
			return netip.Prefix{}, false
		}
		bits = parsedBits
	}
	addr, err := netip.ParseAddr(addrText)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, bits).Masked(), true
}

func expandDarwinIPv4Address(addr string) (string, int, bool) {
	parts := strings.Split(addr, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return "", 0, false
	}
	octets := len(parts)
	for i, part := range parts {
		if part == "" {
			return "", 0, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return "", 0, false
		}
		parts[i] = strconv.Itoa(n)
	}
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, "."), octets, true
}

func darwinListRoutesCommand(family string) *exec.Cmd {
	return exec.Command("netstat", "-rn", "-f", family)
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
