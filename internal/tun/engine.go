package tun

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime"
	"strings"
	"sync"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
	src "github.com/punchproxy/punch/internal/source"
	singtun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
)

// Engine manages the TUN interface and proxies traffic through it.
type Engine struct {
	mu sync.RWMutex

	cfg       config.TUN
	dnsServer *pdns.Server
	selector  *relay.Selector
	sessions  *session.Manager
	assets    *assets.Manager

	tunIf                   singtun.Tun
	tunStack                singtun.Stack
	tunnel                  *handler
	networkUpdateMonitor    singtun.NetworkUpdateMonitor
	defaultInterfaceMonitor singtun.DefaultInterfaceMonitor
	tunAddress              netip.Prefix
	tun6Address             netip.Prefix
	routeAddress            []netip.Prefix
	ifaceName               string
	dnsOverride             *systemDNSOverride
	routeMonitor            *interfaceRouteMonitor
	started                 bool
}

type runtimeTunOptions struct {
	options          singtun.Options
	routeAddress     []netip.Prefix
	fakeRange        netip.Prefix
	dnsServerAddress netip.Addr
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

// UDPStats reports TUN UDP queue counters for the running engine.
func (e *Engine) UDPStats() UDPStats {
	if e == nil {
		return UDPStats{}
	}
	e.mu.RLock()
	tunnel := e.tunnel
	e.mu.RUnlock()
	return tunnel.UDPStats()
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

	runtimeOpts, err := e.buildTunOptions()
	if err != nil {
		return err
	}
	opts := runtimeOpts.options

	logger := singLogger{}
	networkMonitor, defaultInterfaceMonitor, interfaceFinder, err := newTunInterfaceMonitors(logger)
	if err != nil {
		return err
	}
	opts.InterfaceMonitor = defaultInterfaceMonitor
	opts.InterfaceFinder = interfaceFinder

	tunnel := newHandler(e.dnsServer, e.selector, e.sessions, opts.Inet4Address, opts.Inet6Address)
	tunIf, err := singtun.New(opts)
	if err != nil {
		closeTunStartResources(nil, nil, tunnel, defaultInterfaceMonitor, networkMonitor)
		return fmt.Errorf("configure TUN interface: %w", err)
	}
	if ifaceName, nameErr := tunIf.Name(); nameErr == nil && ifaceName != "" {
		opts.Name = ifaceName
	}
	if err := tunIf.Start(); err != nil {
		closeTunStartResources(nil, tunIf, tunnel, defaultInterfaceMonitor, networkMonitor)
		return fmt.Errorf("start TUN interface: %w", err)
	}

	tunStack, err := singtun.NewStack("system", singtun.StackOptions{
		Context:         context.Background(),
		Tun:             tunIf,
		TunOptions:      opts,
		UDPTimeout:      udpAssociationTimeout,
		Handler:         tunnel,
		Logger:          logger,
		InterfaceFinder: interfaceFinder,
	})
	if err != nil {
		closeTunStartResources(nil, tunIf, tunnel, defaultInterfaceMonitor, networkMonitor)
		return fmt.Errorf("create TUN stack: %w", err)
	}
	if err := tunStack.Start(); err != nil {
		closeTunStartResources(tunStack, tunIf, tunnel, defaultInterfaceMonitor, networkMonitor)
		return fmt.Errorf("start TUN stack: %w", err)
	}

	routeAddress := buildCleanupRoutes(runtimeOpts.routeAddress)
	ifaceName := opts.Name
	if err := configureInterfaceRoutes(routeAddress, opts.Inet4Address[0], ifaceName); err != nil {
		closeTunStartResources(tunStack, tunIf, tunnel, defaultInterfaceMonitor, networkMonitor)
		return fmt.Errorf("configure interface routes: %w", err)
	}

	e.tunIf = tunIf
	e.tunStack = tunStack
	e.tunnel = tunnel
	e.networkUpdateMonitor = networkMonitor
	e.defaultInterfaceMonitor = defaultInterfaceMonitor
	e.tunAddress = opts.Inet4Address[0]
	e.tun6Address = firstPrefix(opts.Inet6Address)
	e.routeAddress = routeAddress
	e.ifaceName = ifaceName
	dnsOverride, err := overrideSystemDNS(runtimeOpts.dnsServerAddress.String(), e.ifaceName)
	if err != nil {
		slog.Warn("failed to override system DNS", "dns", runtimeOpts.dnsServerAddress.String(), "interface", e.ifaceName, "error", err)
	} else if dnsOverride != nil {
		e.dnsOverride = dnsOverride
		slog.Info("system DNS overridden", "dns", runtimeOpts.dnsServerAddress.String(), "interface", e.ifaceName)
	}
	e.routeMonitor = newInterfaceRouteMonitor(e.routeAddress, e.tunAddress, e.ifaceName, missingInterfaceRoutes, configureInterfaceRoutes)
	e.started = true

	slog.Info("TUN engine started",
		"device", opts.Name,
		"stack", "system",
		"fake-ip-range", runtimeOpts.fakeRange.String(),
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
	if e.tunStack != nil {
		if err := e.tunStack.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.tunIf != nil {
		if err := e.tunIf.Close(); err != nil {
			if isIgnorableTunCloseError(err) {
				slog.Debug("ignored TUN close cleanup error", "error", err)
			} else if firstErr == nil {
				firstErr = err
			}
		}
	}
	if e.tunnel != nil {
		if err := e.tunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.defaultInterfaceMonitor != nil {
		if err := e.defaultInterfaceMonitor.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.networkUpdateMonitor != nil {
		if err := e.networkUpdateMonitor.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	e.tunIf = nil
	e.tunStack = nil
	e.tunnel = nil
	e.networkUpdateMonitor = nil
	e.defaultInterfaceMonitor = nil
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

	routeAddress := buildCleanupRoutes(routes)
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

func (e *Engine) buildTunOptions() (runtimeTunOptions, error) {
	fakeRange := e.dnsServer.FakeIPPool().IPNet()
	tunAddress, err := buildTunAddress(fakeRange)
	if err != nil {
		return runtimeTunOptions{}, err
	}
	dnsServerAddress, err := buildDNSServerAddress(fakeRange)
	if err != nil {
		return runtimeTunOptions{}, err
	}

	var tun6Address netip.Prefix
	var inet6Address []netip.Prefix
	fakeRange6, hasFakeRange6 := e.dnsServer.FakeIPPool().IPNet6()
	if hasFakeRange6 {
		tun6Address, err = buildTunAddress(fakeRange6)
		if err != nil {
			return runtimeTunOptions{}, err
		}
		inet6Address = []netip.Prefix{tun6Address}
	}
	routes := e.buildRouteAddress(e.cfg.Routes, fakeRange, fakeRange6)

	return runtimeTunOptions{
		options: singtun.Options{
			Name:                 resolveTunDeviceName(),
			MTU:                  1500,
			Inet4Address:         []netip.Prefix{tunAddress},
			Inet6Address:         inet6Address,
			AutoRoute:            false,
			EXP_DisableDNSHijack: true,
			Logger:               singLogger{},
		},
		routeAddress:     routes,
		fakeRange:        fakeRange,
		dnsServerAddress: dnsServerAddress,
	}, nil
}

// ResolveRoute parses a single route entry (a CIDR or a URL/file source)
// and returns the concrete prefixes it expands to, along with whether
// every prefix is currently applied to the TUN interface.
func (e *Engine) ResolveRoute(entry string) RouteResolution {
	res := RouteResolution{Route: entry}
	if src.IsSource(entry) {
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
		if src.IsSource(entry) {
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

func buildCleanupRoutes(routeAddress []netip.Prefix) []netip.Prefix {
	seen := make(map[netip.Prefix]struct{}, len(routeAddress))
	routes := make([]netip.Prefix, 0, len(routeAddress))

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

func newTunInterfaceMonitors(logger singLogger) (singtun.NetworkUpdateMonitor, singtun.DefaultInterfaceMonitor, control.InterfaceFinder, error) {
	interfaceFinder := control.NewDefaultInterfaceFinder()
	networkMonitor, err := singtun.NewNetworkUpdateMonitor(logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create network update monitor: %w", err)
	}
	if err := networkMonitor.Start(); err != nil {
		_ = networkMonitor.Close()
		return nil, nil, nil, fmt.Errorf("start network update monitor: %w", err)
	}
	defaultInterfaceMonitor, err := singtun.NewDefaultInterfaceMonitor(networkMonitor, logger, singtun.DefaultInterfaceMonitorOptions{
		InterfaceFinder:    interfaceFinder,
		OverrideAndroidVPN: true,
	})
	if err != nil {
		_ = networkMonitor.Close()
		return nil, nil, nil, fmt.Errorf("create default interface monitor: %w", err)
	}
	if err := defaultInterfaceMonitor.Start(); err != nil {
		_ = defaultInterfaceMonitor.Close()
		_ = networkMonitor.Close()
		return nil, nil, nil, fmt.Errorf("start default interface monitor: %w", err)
	}
	return networkMonitor, defaultInterfaceMonitor, interfaceFinder, nil
}

func closeTunStartResources(tunStack singtun.Stack, tunIf singtun.Tun, tunnel *handler, defaultInterfaceMonitor singtun.DefaultInterfaceMonitor, networkMonitor singtun.NetworkUpdateMonitor) {
	if tunStack != nil {
		_ = tunStack.Close()
	}
	if tunIf != nil {
		_ = tunIf.Close()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
	if defaultInterfaceMonitor != nil {
		_ = defaultInterfaceMonitor.Close()
	}
	if networkMonitor != nil {
		_ = networkMonitor.Close()
	}
}

func resolveTunDeviceName() string {
	if runtime.GOOS == "darwin" {
		return singtun.CalculateInterfaceName("")
	}
	return "punch0"
}

func isIgnorableTunCloseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "delete route") {
		return false
	}
	return strings.Contains(msg, "not in table") ||
		strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "no such route")
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
