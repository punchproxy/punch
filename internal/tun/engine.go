package tun

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/constant"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/listener/sing_tun"
	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
)

// Engine manages the TUN interface and proxies traffic through it.
type Engine struct {
	mu sync.RWMutex

	cfg       config.TUN
	dnsServer *pdns.Server
	selector  *relay.Selector
	sessions  *session.Manager
	assets    *assets.Manager

	listener     *sing_tun.Listener
	tunnel       *handler
	tunAddress   netip.Prefix
	tun6Address  netip.Prefix
	routeAddress []netip.Prefix
	ifaceName    string
	dnsOverride  *systemDNSOverride
	routeMonitor *interfaceRouteMonitor
	started      bool
}

type SystemInfo struct {
	TUNInterfaceName    string          `json:"tun_interface_name"`
	TUNAddress          string          `json:"tun_address"`
	TUNIPv6Address      string          `json:"tun_ipv6_address,omitempty"`
	ExtraTUNRoutesCount int             `json:"extra_tun_routes_count"`
	SystemDNS           []SystemDNSInfo `json:"system_dns"`
}

// RouteResolution describes a single configured TUN route entry, the
// concrete CIDR prefixes it resolves to, and whether those prefixes are
// currently applied to the interface.
type RouteResolution struct {
	Route    string
	Prefixes []netip.Prefix
	Applied  bool
	Err      error
}

func NewEngine(cfg config.TUN, dns *pdns.Server, sel *relay.Selector, sess *session.Manager, assetManager *assets.Manager) *Engine {
	e := &Engine{
		cfg:       cloneTUNConfig(cfg),
		dnsServer: dns,
		selector:  sel,
		sessions:  sess,
		assets:    assetManager,
	}
	if assetManager != nil {
		assetManager.OnReady(e.onAssetReady)
	}
	return e
}

func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.startLocked()
}

func (e *Engine) startLocked() error {
	if e.started {
		return nil
	}

	opts, fakeRange, dnsServerAddress, err := e.buildTunOptions()
	if err != nil {
		return err
	}

	e.tunnel = newHandler(e.dnsServer, e.selector, e.sessions)

	listener, err := sing_tun.New(opts, e.tunnel)
	if err != nil {
		if isRouteExistsError(err) {
			slog.Warn("TUN route already exists, cleaning stale routes and retrying", "error", err)
			cleanupTargets := buildCleanupRoutes(opts.RouteAddress, tunOptionAddresses(opts))
			if cleanupErr := cleanupRoutes(cleanupTargets, opts.Inet4Address[0]); cleanupErr != nil {
				e.tunnel.Close()
				e.tunnel = nil
				return fmt.Errorf("start TUN listener: %w (cleanup failed: %v)", err, cleanupErr)
			}
			listener, err = sing_tun.New(opts, e.tunnel)
		}
		if err != nil {
			e.tunnel.Close()
			e.tunnel = nil
			return fmt.Errorf("start TUN listener: %w", err)
		}
	}

	e.listener = listener
	e.tunAddress = opts.Inet4Address[0]
	e.tun6Address = firstPrefix(opts.Inet6Address)
	e.routeAddress = buildCleanupRoutes(opts.RouteAddress, tunOptionAddresses(opts))
	e.ifaceName = parseTunInterfaceName(listener.Address())
	if err := configureInterfaceRoutes(e.routeAddress, e.tunAddress, e.ifaceName); err != nil {
		_ = listener.Close()
		e.tunnel.Close()
		e.listener = nil
		e.tunnel = nil
		e.tunAddress = netip.Prefix{}
		e.tun6Address = netip.Prefix{}
		e.routeAddress = nil
		e.ifaceName = ""
		return fmt.Errorf("configure interface routes: %w", err)
	}
	dnsOverride, err := overrideSystemDNS(dnsServerAddress.String(), e.ifaceName)
	if err != nil {
		slog.Warn("failed to override system DNS", "dns", dnsServerAddress.String(), "interface", e.ifaceName, "error", err)
	} else if dnsOverride != nil {
		e.dnsOverride = dnsOverride
		slog.Info("system DNS overridden", "dns", dnsServerAddress.String(), "interface", e.ifaceName)
	}
	e.routeMonitor = newInterfaceRouteMonitor(e.routeAddress, e.tunAddress, e.ifaceName, missingInterfaceRoutes, configureInterfaceRoutes)
	e.started = true

	slog.Info("TUN engine started",
		"device", opts.Device,
		"stack", opts.Stack.String(),
		"address", listener.Address(),
		"fake-ip-range", fakeRange.String(),
	)
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.stopLocked()
}

func (e *Engine) stopLocked() error {
	if !e.started {
		return nil
	}

	var firstErr error
	if e.routeMonitor != nil {
		e.routeMonitor.Stop()
	}
	if e.dnsOverride != nil {
		if err := e.dnsOverride.StopAndRestore(); err != nil {
			slog.Warn("failed to restore system DNS", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := cleanupRoutes(e.routeAddress, e.tunAddress); err != nil && firstErr == nil {
		firstErr = err
	}
	if e.listener != nil {
		if err := e.listener.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.tunnel != nil {
		if err := e.tunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	e.listener = nil
	e.tunnel = nil
	e.tunAddress = netip.Prefix{}
	e.tun6Address = netip.Prefix{}
	e.routeAddress = nil
	e.ifaceName = ""
	e.dnsOverride = nil
	e.routeMonitor = nil
	e.started = false
	slog.Info("TUN engine stopped")
	return firstErr
}

func (e *Engine) ApplyConfig(cfg config.TUN) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg = cloneTUNConfig(cfg)
	if !e.started {
		e.cfg = cfg
		return nil
	}

	fakeRange := e.dnsServer.FakeIPPool().IPNet()
	fakeRange6, _ := e.dnsServer.FakeIPPool().IPNet6()
	routes := e.buildRouteAddress(cfg.Routes, fakeRange, fakeRange6)
	if hasIPv6Route(routes) != e.tun6Address.IsValid() {
		e.cfg = cfg
		stopErr := e.stopLocked()
		startErr := e.startLocked()
		if startErr != nil {
			if stopErr != nil {
				return fmt.Errorf("restart TUN after route address family change: stop: %v; start: %w", stopErr, startErr)
			}
			return fmt.Errorf("restart TUN after route address family change: %w", startErr)
		}
		return stopErr
	}

	routeAddress := buildCleanupRoutes(routes, e.tunAddresses())
	if err := configureInterfaceRoutes(routeAddress, e.tunAddress, e.ifaceName); err != nil {
		return fmt.Errorf("configure interface routes: %w", err)
	}
	if err := cleanupRoutes(removedRoutes(e.routeAddress, routeAddress), e.tunAddress); err != nil {
		return fmt.Errorf("cleanup removed routes: %w", err)
	}

	e.cfg = cfg
	e.routeAddress = routeAddress
	if e.routeMonitor != nil {
		e.routeMonitor.Update(routeAddress, e.tunAddress, e.ifaceName)
	}
	return nil
}

// IsRunning returns whether the TUN engine is currently active.
func (e *Engine) IsRunning() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.started
}

func (e *Engine) SystemInfo() SystemInfo {
	e.mu.RLock()
	ifaceName := e.ifaceName
	tunAddress := ""
	if e.tunAddress.IsValid() {
		tunAddress = e.tunAddress.String()
	}
	tun6Address := ""
	if e.tun6Address.IsValid() {
		tun6Address = e.tun6Address.String()
	}
	extraRoutes := append([]string(nil), e.cfg.Routes...)
	dnsOverride := e.dnsOverride
	e.mu.RUnlock()

	resolved := e.buildRouteAddress(extraRoutes)

	info := SystemInfo{
		TUNInterfaceName:    ifaceName,
		TUNAddress:          tunAddress,
		TUNIPv6Address:      tun6Address,
		ExtraTUNRoutesCount: len(resolved),
	}
	if dnsOverride != nil {
		info.SystemDNS = dnsOverride.Snapshot()
		return info
	}
	states, err := currentSystemDNS(ifaceName)
	if err != nil {
		slog.Warn("failed to inspect system DNS", "error", err)
		return info
	}
	info.SystemDNS = make([]SystemDNSInfo, 0, len(states))
	for _, state := range states {
		info.SystemDNS = append(info.SystemDNS, SystemDNSInfo{
			Name:    state.Name,
			Current: cloneStrings(state.Servers),
		})
	}
	return info
}

func (e *Engine) buildTunOptions() (LC.Tun, netip.Prefix, netip.Addr, error) {
	fakeRange := e.dnsServer.FakeIPPool().IPNet()
	tunAddress, err := buildTunAddress(fakeRange)
	if err != nil {
		return LC.Tun{}, netip.Prefix{}, netip.Addr{}, err
	}
	dnsServerAddress, err := buildDNSServerAddress(fakeRange)
	if err != nil {
		return LC.Tun{}, netip.Prefix{}, netip.Addr{}, err
	}

	var tun6Address netip.Prefix
	var inet6Address []netip.Prefix
	fakeRange6, hasFakeRange6 := e.dnsServer.FakeIPPool().IPNet6()
	if hasFakeRange6 {
		tun6Address, err = buildTunAddress(fakeRange6)
		if err != nil {
			return LC.Tun{}, netip.Prefix{}, netip.Addr{}, err
		}
		inet6Address = []netip.Prefix{tun6Address}
	}
	routes := e.buildRouteAddress(e.cfg.Routes, fakeRange, fakeRange6)

	return LC.Tun{
		Enable:              true,
		Device:              e.cfg.Device,
		Stack:               constant.TunSystem,
		AutoRoute:           true,
		AutoDetectInterface: true,
		DNSHijack:           []string{"any:53"},
		MTU:                 1500,
		Inet4Address:        []netip.Prefix{tunAddress},
		Inet6Address:        inet6Address,
		RouteAddress:        routes,
	}, fakeRange, dnsServerAddress, nil
}

// ResolveRoute parses a single route entry (a CIDR or a URL/file source)
// and returns the concrete prefixes it expands to, along with whether
// every prefix is currently applied to the TUN interface.
func (e *Engine) ResolveRoute(entry string) RouteResolution {
	res := RouteResolution{Route: entry}
	if isSource(entry) {
		set := pdns.NewIPSet()
		if _, err := pdns.LoadIPSet(entry, set, e.assets); err != nil {
			res.Err = err
			return res
		}
		res.Prefixes = set.Prefixes()
	} else {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			res.Err = err
			return res
		}
		res.Prefixes = []netip.Prefix{prefix.Masked()}
	}

	e.mu.RLock()
	started := e.started
	current := make(map[netip.Prefix]struct{}, len(e.routeAddress))
	for _, p := range e.routeAddress {
		current[p.Masked()] = struct{}{}
	}
	e.mu.RUnlock()

	if !started || len(res.Prefixes) == 0 {
		return res
	}
	for _, p := range res.Prefixes {
		if _, ok := current[p.Masked()]; !ok {
			return res
		}
	}
	res.Applied = true
	return res
}

func (e *Engine) buildRouteAddress(routeEntries []string, fakeRanges ...netip.Prefix) []netip.Prefix {
	dropIPv6 := e.dnsServer != nil && e.dnsServer.DisableIPv6FakeIP()
	var routes []netip.Prefix
	for _, fakeRange := range fakeRanges {
		if !fakeRange.IsValid() {
			continue
		}
		if dropIPv6 && fakeRange.Addr().Is6() {
			continue
		}
		routes = append(routes, fakeRange.Masked())
	}
	for _, entry := range routeEntries {
		if isSource(entry) {
			set := pdns.NewIPSet()
			n, err := pdns.LoadIPSet(entry, set, e.assets)
			if err != nil {
				slog.Warn("ignoring invalid tun route source", "source", entry, "error", err)
				continue
			}
			for _, p := range set.Prefixes() {
				if dropIPv6 && p.Addr().Is6() {
					continue
				}
				routes = append(routes, p)
			}
			slog.Info("loaded tun route source", "source", entry, "count", n)
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			slog.Warn("ignoring invalid tun route", "cidr", entry, "error", err)
			continue
		}
		if dropIPv6 && prefix.Addr().Is6() {
			slog.Debug("dropping ipv6 tun route while ipv6 fake-ip is disabled", "cidr", entry)
			continue
		}
		routes = append(routes, prefix.Masked())
	}
	return routes
}

func (e *Engine) onAssetReady(source string) {
	e.mu.RLock()
	cfg := cloneTUNConfig(e.cfg)
	started := e.started
	affected := routeSourceConfigured(cfg.Routes, source)
	e.mu.RUnlock()

	if !started || !affected {
		return
	}
	if err := e.ApplyConfig(cfg); err != nil {
		slog.Warn("update TUN routes after async asset download failed", "source", source, "error", err)
		return
	}
	slog.Info("TUN routes updated after async asset download", "source", source)
}

func routeSourceConfigured(routes []string, source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	for _, route := range routes {
		if strings.TrimSpace(route) == source {
			return true
		}
	}
	return false
}

func buildTunAddress(fakeRange netip.Prefix) (netip.Prefix, error) {
	if !fakeRange.IsValid() {
		return netip.Prefix{}, fmt.Errorf("fake-ip range is required")
	}
	if fakeRange.Addr().Is4() {
		return netip.PrefixFrom(fakeRange.Addr().Next(), 30), nil
	}
	return netip.PrefixFrom(fakeRange.Addr().Next(), 126), nil
}

func buildDNSServerAddress(fakeRange netip.Prefix) (netip.Addr, error) {
	if !fakeRange.Addr().Is4() {
		return netip.Addr{}, fmt.Errorf("fake-ip range must be IPv4, got %s", fakeRange)
	}
	addr := fakeRange.Addr().Next().Next()
	if !fakeRange.Contains(addr) {
		return netip.Addr{}, fmt.Errorf("dns server address %s is outside fake-ip range %s", addr, fakeRange)
	}
	return addr, nil
}

func buildCleanupRoutes(routeAddress []netip.Prefix, interfaceAddress []netip.Prefix) []netip.Prefix {
	seen := make(map[netip.Prefix]struct{}, len(routeAddress)+len(interfaceAddress))
	routes := make([]netip.Prefix, 0, len(routeAddress)+len(interfaceAddress))

	add := func(prefix netip.Prefix) {
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			return
		}
		seen[prefix] = struct{}{}
		routes = append(routes, prefix)
	}

	for _, prefix := range routeAddress {
		add(prefix)
	}
	for _, prefix := range interfaceAddress {
		if prefix.Bits() < prefix.Addr().BitLen() {
			add(prefix)
		}
	}
	return routes
}

func hasIPv6Route(routes []netip.Prefix) bool {
	for _, route := range routes {
		if route.Addr().Is6() {
			return true
		}
	}
	return false
}

func firstPrefix(prefixes []netip.Prefix) netip.Prefix {
	if len(prefixes) == 0 {
		return netip.Prefix{}
	}
	return prefixes[0]
}

func tunOptionAddresses(opts LC.Tun) []netip.Prefix {
	addresses := make([]netip.Prefix, 0, len(opts.Inet4Address)+len(opts.Inet6Address))
	addresses = append(addresses, opts.Inet4Address...)
	addresses = append(addresses, opts.Inet6Address...)
	return addresses
}

func (e *Engine) tunAddresses() []netip.Prefix {
	addresses := []netip.Prefix{e.tunAddress}
	if e.tun6Address.IsValid() {
		addresses = append(addresses, e.tun6Address)
	}
	return addresses
}

func removedRoutes(oldRoutes, newRoutes []netip.Prefix) []netip.Prefix {
	next := make(map[netip.Prefix]struct{}, len(newRoutes))
	for _, prefix := range newRoutes {
		next[prefix.Masked()] = struct{}{}
	}
	var removed []netip.Prefix
	for _, prefix := range oldRoutes {
		prefix = prefix.Masked()
		if _, ok := next[prefix]; !ok {
			removed = append(removed, prefix)
		}
	}
	return removed
}

func cloneTUNConfig(cfg config.TUN) config.TUN {
	cfg.Routes = append([]string(nil), cfg.Routes...)
	return cfg
}

func isSource(entry string) bool {
	return strings.HasPrefix(entry, "http://") ||
		strings.HasPrefix(entry, "https://") ||
		strings.HasPrefix(entry, "/") ||
		strings.HasPrefix(entry, "./") ||
		strings.HasPrefix(entry, "../") ||
		strings.HasPrefix(entry, "~/")
}

func isRouteExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file exists") && strings.Contains(msg, "route")
}

func parseTunInterfaceName(addr string) string {
	name, _, ok := strings.Cut(addr, "(")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}
