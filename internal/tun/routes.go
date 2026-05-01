package tun

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

const routeMonitorInterval = 10 * time.Second

type interfaceRouteMonitor struct {
	read  func([]netip.Prefix, string) ([]interfaceRouteMismatch, error)
	apply func([]netip.Prefix, netip.Prefix, string) error

	mu         sync.RWMutex
	routes     []netip.Prefix
	tunAddress netip.Prefix
	iface      string
	stop       chan struct{}
	done       chan struct{}
}

type interfaceRouteMismatch struct {
	route         netip.Prefix
	fromInterface string
	toInterface   string
}

func newInterfaceRouteMonitor(routes []netip.Prefix, tunAddress netip.Prefix, iface string, read func([]netip.Prefix, string) ([]interfaceRouteMismatch, error), apply func([]netip.Prefix, netip.Prefix, string) error) *interfaceRouteMonitor {
	m := &interfaceRouteMonitor{
		read:       read,
		apply:      apply,
		routes:     clonePrefixes(routes),
		tunAddress: tunAddress,
		iface:      iface,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go m.monitor()
	return m
}

func (m *interfaceRouteMonitor) Stop() {
	close(m.stop)
	<-m.done
}

func (m *interfaceRouteMonitor) Update(routes []netip.Prefix, tunAddress netip.Prefix, iface string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = clonePrefixes(routes)
	m.tunAddress = tunAddress
	m.iface = iface
}

func (m *interfaceRouteMonitor) monitor() {
	defer close(m.done)

	ticker := time.NewTicker(routeMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.checkOnce()
		}
	}
}

func (m *interfaceRouteMonitor) checkOnce() {
	m.mu.RLock()
	routes := clonePrefixes(m.routes)
	tunAddress := m.tunAddress
	iface := m.iface
	m.mu.RUnlock()

	if len(routes) == 0 || iface == "" {
		return
	}
	mismatches, err := m.read(routes, iface)
	if err != nil {
		slog.Warn("failed to monitor TUN routes", "error", err)
		return
	}
	if len(mismatches) == 0 {
		return
	}
	for _, mismatch := range mismatches {
		slog.Warn("TUN route is missing or points to a different interface; reapplying route",
			"route", mismatch.route.String(),
			"from_interface", mismatch.fromInterface,
			"to_interface", mismatch.toInterface,
		)
	}
	if err := m.apply(routes, tunAddress, iface); err != nil {
		slog.Warn("failed to reapply TUN routes", "interface", iface, "error", err)
	}
}

func clonePrefixes(routes []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, len(routes))
	copy(out, routes)
	return out
}
