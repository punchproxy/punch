package dns

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/dnsrule"
	"github.com/punchproxy/punch/internal/fakeip"
	"golang.org/x/sync/singleflight"
)

type Server struct {
	listenAddr        string
	fakeIPPool        *fakeip.Pool
	disableIPv6FakeIP bool
	cache             *Cache
	resolver          *ResolverGroup
	resolverMu        sync.RWMutex

	domainMatcher *dnsrule.Matcher
	directIPs     *IPSet
	rejectIPs     *IPSet
	assets        *assets.Manager
	rulesMu       sync.RWMutex

	udpServer *dns.Server
	tcpServer *dns.Server

	// Stats
	totalQueries     atomic.Int64
	cacheHits        atomic.Int64
	upstreamRequests atomic.Int64
	ignoreDecisions  atomic.Int64
	relayDecisions   atomic.Int64
	directDecisions  atomic.Int64
	rejectDecisions  atomic.Int64
	defaultRuleHits  atomic.Int64

	// Last queried domains by DNS decision, guarded by mu.
	mu               sync.Mutex
	lastRelayDomain  string
	lastDirectDomain string
	lastRejectDomain string

	queryStreamMu      sync.Mutex
	queryStreamClients map[chan<- QueryLog]struct{}

	ruleLists     map[string][]*ruleListEntry
	ruleListIndex map[string]map[string]*ruleListEntry
	rawRules      config.DNSRules

	refreshMu    sync.Mutex
	refreshing   map[string]struct{}
	refreshGroup singleflight.Group

	reloadMu sync.Mutex // serializes ReloadRules calls post-startup
}

func NewServer(assetManager *assets.Manager) (*Server, error) {
	cfg, err := config.Snapshot()
	if err != nil {
		return nil, err
	}
	fakeIPTTLValue := cfg.DNS.FakeIPTTL
	if fakeIPTTLValue == "" {
		fakeIPTTLValue = "1h"
	}
	fakeIPTTL, err := time.ParseDuration(fakeIPTTLValue)
	if err != nil {
		return nil, fmt.Errorf("parse fake ip ttl: %w", err)
	}
	disableIPv6FakeIP := config.DisableIPv6FakeIPEnabled(cfg)
	fakeIPv6Range := cfg.DNS.FakeIPv6Range
	if disableIPv6FakeIP {
		fakeIPv6Range = ""
	}
	fakePool, err := fakeip.NewDualStack(cfg.DNS.FakeIPRange, fakeIPv6Range, fakeIPTTL)
	if err != nil {
		return nil, fmt.Errorf("create fake ip pool: %w", err)
	}

	s := &Server{
		listenAddr:         cfg.DNS.Listen,
		fakeIPPool:         fakePool,
		disableIPv6FakeIP:  disableIPv6FakeIP,
		cache:              NewCache(cfg.DNS.CacheSize, 60, 86400),
		resolver:           NewResolverGroup(buildUpstreams()),
		domainMatcher:      dnsrule.NewMatcher(),
		directIPs:          NewIPSet(),
		rejectIPs:          NewIPSet(),
		assets:             assetManager,
		queryStreamClients: make(map[chan<- QueryLog]struct{}),
		ruleLists:          make(map[string][]*ruleListEntry),
		ruleListIndex:      make(map[string]map[string]*ruleListEntry),
		rawRules:           cfg.DNS.Rules,
		refreshing:         make(map[string]struct{}),
	}

	if assetManager != nil {
		assetManager.OnReady(s.onAssetReady)
	}

	return s, nil
}

func buildUpstreams() []*UpstreamResolver {
	cfg, err := config.Snapshot()
	if err != nil {
		cfg = &config.Config{}
	}
	var upstreams []*UpstreamResolver
	for _, u := range cfg.DNS.Upstream {
		upstreams = append(upstreams, NewUpstreamResolver(u.URL, u.Bootstrap, u.Domains...))
	}
	if len(upstreams) == 0 {
		upstreams = append(upstreams, NewUpstreamResolver("8.8.8.8:53", ""))
	}
	return upstreams
}

func (s *Server) Start() error {
	handler := dns.HandlerFunc(s.handleDNS)
	s.udpServer = &dns.Server{
		Addr:    s.listenAddr,
		Net:     "udp",
		Handler: handler,
	}
	s.tcpServer = &dns.Server{
		Addr:    s.listenAddr,
		Net:     "tcp",
		Handler: handler,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- s.udpServer.ListenAndServe() }()
	go func() { errCh <- s.tcpServer.ListenAndServe() }()

	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-errCh:
		return fmt.Errorf("dns server failed to start: %w", err)
	default:
	}

	slog.Info("DNS server started", "listen", s.listenAddr)
	return nil
}

func (s *Server) Stop() error {
	var errs []error
	if s.udpServer != nil {
		if err := s.udpServer.Shutdown(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.tcpServer != nil {
		if err := s.tcpServer.Shutdown(); err != nil {
			errs = append(errs, err)
		}
	}
	s.udpServer = nil
	s.tcpServer = nil
	if len(errs) > 0 {
		return fmt.Errorf("dns server shutdown errors: %v", errs)
	}
	return nil
}

// DisableIPv6FakeIP reports whether IPv6 fake-IP allocation is disabled.
func (s *Server) DisableIPv6FakeIP() bool { return s.disableIPv6FakeIP }

func (s *Server) handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	resp, _, _, _, _, err := s.serveMsg(context.Background(), r, w.RemoteAddr().String())
	if err != nil {
		dns.HandleFailed(w, r)
		return
	}
	if err := w.WriteMsg(resp); err != nil {
		slog.Debug("dns write failed", "error", err)
	}
}

func (s *Server) ServeMsg(ctx context.Context, r *dns.Msg) (*dns.Msg, error) {
	resp, _, _, _, _, err := s.serveMsg(ctx, r, "tun")
	return resp, err
}
