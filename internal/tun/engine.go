package tun

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

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
	cfg       config.TUN
	dnsServer *pdns.Server
	selector  *relay.Selector
	sessions  *session.Manager
	assets    *assets.Manager

	listener     *sing_tun.Listener
	tunnel       *handler
	tunAddress   netip.Prefix
	routeAddress []netip.Prefix
	ifaceName    string
	restoreDNS   func() error
	started      bool
}

func NewEngine(cfg config.TUN, dns *pdns.Server, sel *relay.Selector, sess *session.Manager, assetManager *assets.Manager) *Engine {
	return &Engine{
		cfg:       cfg,
		dnsServer: dns,
		selector:  sel,
		sessions:  sess,
		assets:    assetManager,
	}
}

func (e *Engine) Start() error {
	if e.started {
		return nil
	}

	opts, fakeRange, err := e.buildTunOptions()
	if err != nil {
		return err
	}

	e.tunnel = newHandler(e.dnsServer, e.selector, e.sessions)

	listener, err := sing_tun.New(opts, e.tunnel)
	if err != nil {
		if isRouteExistsError(err) {
			slog.Warn("TUN route already exists, cleaning stale routes and retrying", "error", err)
			cleanupTargets := buildCleanupRoutes(opts.RouteAddress, opts.Inet4Address)
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
	e.routeAddress = buildCleanupRoutes(opts.RouteAddress, opts.Inet4Address)
	e.ifaceName = parseTunInterfaceName(listener.Address())
	if err := configureInterfaceRoutes(e.routeAddress, e.tunAddress, e.ifaceName); err != nil {
		_ = listener.Close()
		e.tunnel.Close()
		e.listener = nil
		e.tunnel = nil
		e.tunAddress = netip.Prefix{}
		e.routeAddress = nil
		e.ifaceName = ""
		return fmt.Errorf("configure interface routes: %w", err)
	}
	restoreDNS, err := overrideSystemDNS(e.tunAddress.Addr().String(), e.ifaceName)
	if err != nil {
		slog.Warn("failed to override system DNS", "dns", e.tunAddress.Addr().String(), "interface", e.ifaceName, "error", err)
	} else if restoreDNS != nil {
		e.restoreDNS = restoreDNS
		slog.Info("system DNS overridden", "dns", e.tunAddress.Addr().String(), "interface", e.ifaceName)
	}
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
	if !e.started {
		return nil
	}

	var firstErr error
	if e.restoreDNS != nil {
		if err := e.restoreDNS(); err != nil {
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
	e.routeAddress = nil
	e.ifaceName = ""
	e.restoreDNS = nil
	e.started = false
	slog.Info("TUN engine stopped")
	return firstErr
}

// IsRunning returns whether the TUN engine is currently active.
func (e *Engine) IsRunning() bool {
	return e.started
}

func (e *Engine) buildTunOptions() (LC.Tun, netip.Prefix, error) {
	fakeRange := e.dnsServer.FakeIPPool().IPNet()
	tunAddress, err := buildTunAddress(fakeRange)
	if err != nil {
		return LC.Tun{}, netip.Prefix{}, err
	}

	routes := []netip.Prefix{fakeRange}
	for _, entry := range e.cfg.Routes {
		if isSource(entry) {
			set := pdns.NewIPSet()
			n, err := pdns.LoadIPSet(entry, set, e.assets)
			if err != nil {
				slog.Warn("ignoring invalid tun route source", "source", entry, "error", err)
				continue
			}
			routes = append(routes, set.Prefixes()...)
			slog.Info("loaded tun route source", "source", entry, "count", n)
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			slog.Warn("ignoring invalid tun route", "cidr", entry, "error", err)
			continue
		}
		routes = append(routes, prefix.Masked())
	}

	return LC.Tun{
		Enable:              true,
		Device:              e.cfg.Device,
		Stack:               constant.TunSystem,
		AutoRoute:           true,
		AutoDetectInterface: true,
		DNSHijack:           []string{"any:53"},
		MTU:                 1500,
		Inet4Address:        []netip.Prefix{tunAddress},
		RouteAddress:        routes,
	}, fakeRange, nil
}

func buildTunAddress(fakeRange netip.Prefix) (netip.Prefix, error) {
	if !fakeRange.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("fake-ip range must be IPv4, got %s", fakeRange)
	}
	return netip.PrefixFrom(fakeRange.Addr().Next(), 30), nil
}

func buildCleanupRoutes(routeAddress []netip.Prefix, inet4Address []netip.Prefix) []netip.Prefix {
	seen := make(map[netip.Prefix]struct{}, len(routeAddress)+len(inet4Address))
	routes := make([]netip.Prefix, 0, len(routeAddress)+len(inet4Address))

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
	for _, prefix := range inet4Address {
		if prefix.Bits() < prefix.Addr().BitLen() {
			add(prefix)
		}
	}
	return routes
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
