package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/punchproxy/punch/internal/api"
	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/fakeip"
	"github.com/punchproxy/punch/internal/logging"
	pmihomo "github.com/punchproxy/punch/internal/mihomo"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
	"github.com/punchproxy/punch/internal/tun"
)

type setFlag []string

func (s *setFlag) String() string { return strings.Join(*s, ",") }
func (s *setFlag) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("expected key=value, got %q", v)
	}
	*s = append(*s, v)
	return nil
}

var (
	version  = "dev"
	dataDir  = flag.String("data-dir", "", "override directory that holds punch.db (default: $PUNCH_DATA_DIR or platform-specific path)")
	showVer  = flag.Bool("version", false, "show version and exit")
	setFlags setFlag
)

func main() {
	flag.Var(&setFlags, "set", "override a config key and persist it (e.g. -set dns.listen_address=0.0.0.0 -set dns.custom_port=53); may be repeated")
	flag.Var(&setFlags, "s", "shorthand for -set")
	flag.Parse()

	if *showVer {
		fmt.Printf("punchd %s\n", version)
		os.Exit(0)
	}

	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: create data dir %s: %v\n", dir, err)
		os.Exit(1)
	}
	dbPath := filepath.Join(dir, "punch.db")

	st, err := config.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	// Load configuration into the process-wide config cache.
	if err := config.Init(st); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := applySetOverrides(setFlags); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Snapshot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	setupLogging(cfg.LogLevel, cfg.LogFile)
	slog.Info("Punch starting", "version", version, "data_dir", dir)

	// Initialize event bus
	bus := eventbus.New()

	// Initialize session manager. Closed sessions spill to the store instead
	// of accumulating in memory; history is per-run, so drop whatever the
	// previous run left behind.
	sessions := session.NewManager(bus, cfg.Sessions.HistoryLimit)
	if cleared, err := st.ClearClosedSessions(); err != nil {
		slog.Warn("clear session history from previous run", "error", err)
	} else if cleared > 0 {
		slog.Info("cleared session history from previous run", "sessions", cleared)
	}
	sessions.SetHistoryStore(st)

	var selector *relay.Selector
	var dnsResolver *pdns.ServerResolver
	directDNSDialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		if dnsResolver == nil {
			return nil, fmt.Errorf("dns resolver is not initialized")
		}
		return dnsResolver.DialContext(ctx, network, address)
	}
	assetDialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		if selector != nil {
			return selector.DialContext(ctx, network, address)
		}
		return directDNSDialContext(ctx, network, address)
	}

	assetManager, err := assets.New(st, time.Duration(cfg.AssetRefreshInterval)*time.Second, assetDialContext, directDNSDialContext)
	if err != nil {
		slog.Error("failed to initialize asset manager", "error", err)
		os.Exit(1)
	}

	// Initialize DNS server
	dnsServer, err := pdns.NewServer(assetManager)
	if err != nil {
		slog.Error("failed to create DNS server", "error", err)
		os.Exit(1)
	}
	dnsResolver = pdns.NewServerResolver(dnsServer)
	unregisterMihomoDNS := pmihomo.RegisterDNS(dnsServer, dnsResolver)

	if err := dnsServer.LoadInitialRules(); err != nil {
		slog.Error("failed to load DNS rules", "error", err)
		os.Exit(1)
	}

	if records, err := st.ListFakeIPs(); err != nil {
		slog.Warn("load persisted fake IPs", "error", err)
	} else if len(records) > 0 {
		mappings := make([]fakeip.Mapping, 0, len(records))
		for _, r := range records {
			ip, parseErr := netip.ParseAddr(r.IP)
			if parseErr != nil {
				continue
			}
			mappings = append(mappings, fakeip.Mapping{
				IP:        ip,
				Domain:    r.Domain,
				ExpiresAt: r.ExpiresAt,
			})
		}
		n := dnsServer.FakeIPPool().Restore(mappings)
		slog.Info("restored fake IP mappings", "count", n, "persisted", len(records))
	}

	// Initialize relay selector
	selector, err = relay.NewSelector(cfg.Relay, cfg.Check, assetManager, directDNSDialContext, st, bus, dnsServer.ResolveRelayDomain)
	if err != nil {
		slog.Error("failed to create relay selector", "error", err)
		os.Exit(1)
	}
	bus.Subscribe(eventbus.EventRelayChange, func(eventbus.Event) {
		assetManager.RetryFailedAsync()
	})

	// Initialize TUN engine
	tunEngine := tun.NewEngine(cfg.TUN, dnsServer, selector, sessions, assetManager)

	apiServer := api.NewServer(cfg.API, st, dnsServer, selector, sessions)
	apiServer.SetTUNEngine(tunEngine)
	apiServer.SetVersion(version)
	shutdownCh := make(chan struct{})
	apiServer.SetShutdownFunc(func() {
		select {
		case <-shutdownCh:
		default:
			close(shutdownCh)
		}
	})

	// --- Start all components ---

	// 1. Start DNS server
	if err := dnsServer.Start(); err != nil {
		slog.Error("failed to start DNS server", "error", err)
		os.Exit(1)
	}

	// 2. Start relay selector (health checks)
	selector.Start()

	// 3. Start TUN engine
	if err := tunEngine.Start(); err != nil {
		slog.Error("failed to start TUN engine", "error", err)
		selector.Stop()
		if stopErr := dnsServer.Stop(); stopErr != nil {
			slog.Error("DNS shutdown error", "error", stopErr)
		}
		os.Exit(1)
	}

	// 4. Start API
	if err := apiServer.Start(); err != nil {
		slog.Error("failed to start API", "error", err)
		os.Exit(1)
	}

	fakeIPSaverStop := make(chan struct{})
	fakeIPSaverDone := make(chan struct{})
	go func() {
		defer close(fakeIPSaverDone)
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-fakeIPSaverStop:
				return
			case <-t.C:
				if err := saveFakeIPs(st, dnsServer.FakeIPPool()); err != nil {
					slog.Warn("persist fake IPs", "error", err)
				}
			}
		}
	}()

	assetManager.MarkReady()
	slog.Info("Punch is ready",
		"dns", config.DNSListenAddr(cfg.DNS),
		"api", cfg.API.Listen,
	)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Also handle SIGHUP for config reload
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)

	for {
		select {
		case sig := <-sigCh:
			slog.Info("received signal, shutting down", "signal", sig)
			goto shutdown
		case <-shutdownCh:
			slog.Info("received API shutdown request")
			goto shutdown
		case <-reloadCh:
			slog.Info("received SIGHUP, reloading config")
			// TODO: implement hot reload of rule lists
		}
	}

shutdown:
	// Graceful shutdown in reverse order
	slog.Info("shutting down...")

	close(fakeIPSaverStop)
	<-fakeIPSaverDone
	if err := saveFakeIPs(st, dnsServer.FakeIPPool()); err != nil {
		slog.Warn("persist fake IPs on shutdown", "error", err)
	}

	if err := apiServer.Stop(); err != nil {
		slog.Error("API shutdown error", "error", err)
	}
	if err := tunEngine.Stop(); err != nil {
		slog.Error("TUN shutdown error", "error", err)
	}
	selector.Stop()
	unregisterMihomoDNS()
	if err := dnsServer.Stop(); err != nil {
		slog.Error("DNS shutdown error", "error", err)
	}

	slog.Info("Punch stopped")
}

// resolveDataDir picks the directory that will hold punch.db. Precedence:
// explicit flag, $PUNCH_DATA_DIR, platform default.
func resolveDataDir(flagVal string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	if env := os.Getenv("PUNCH_DATA_DIR"); env != "" {
		return filepath.Abs(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home dir: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "punch"), nil
	case "windows":
		if base := os.Getenv("APPDATA"); base != "" {
			return filepath.Join(base, "punch"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "punch"), nil
	default:
		if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
			return filepath.Join(base, "punch"), nil
		}
		return filepath.Join(home, ".config", "punch"), nil
	}
}

// applySetOverrides applies each -set key=value to the persisted config.
func applySetOverrides(items []string) error {
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return fmt.Errorf("invalid -set %q: expected key=value", item)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("invalid -set %q: empty key", item)
		}
		if err := config.Set(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}

func saveFakeIPs(st *config.Store, pool *fakeip.Pool) error {
	if pool == nil {
		return nil
	}
	snap := pool.Snapshot()
	records := make([]config.FakeIPRecord, 0, len(snap))
	for _, m := range snap {
		ip := m.IP.Unmap()
		family := 4
		if ip.Is6() {
			family = 6
		}
		records = append(records, config.FakeIPRecord{
			Domain:    m.Domain,
			Family:    family,
			IP:        ip.String(),
			ExpiresAt: m.ExpiresAt,
		})
	}
	return st.ReplaceFakeIPs(records)
}

func setupLogging(level, file string) {
	logging.Setup(level, file)
}
