package dns

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/dnsrule"
)

type UpstreamStats struct {
	URL               string   `json:"url"`
	Bootstrap         string   `json:"bootstrap,omitempty"`
	Queries           int64    `json:"queries"`
	AverageLatency    int64    `json:"average_latency_ms"`
	LastLatency       int64    `json:"last_latency_ms"`
	LastQueriedAt     string   `json:"last_queried_at,omitempty"`
	LastQueriedDomain string   `json:"last_queried_domain,omitempty"`
	Domains           []string `json:"domains,omitempty"`
}

// Upstream represents a DNS upstream server.
type UpstreamResolver struct {
	url       string
	bootstrap string
	client    *http.Client
	isDoH     bool
	domains   []string
	matcher   *dnsrule.Matcher

	queries           atomic.Int64
	totalLatency      atomic.Int64
	lastLatency       atomic.Int64
	lastQueriedAt     atomic.Int64
	lastQueriedDomain atomic.Pointer[string]
}

func NewUpstreamResolver(url, bootstrap string, domains ...string) *UpstreamResolver {
	u := &UpstreamResolver{
		url:       url,
		bootstrap: bootstrap,
		isDoH:     strings.HasPrefix(url, "https://"),
	}
	for _, domain := range domains {
		rule := dnsrule.Normalize(domain)
		if rule != "" {
			u.domains = append(u.domains, rule)
		}
	}
	if len(u.domains) > 0 {
		u.matcher = dnsrule.NewMatcher()
		for i, domain := range u.domains {
			_ = u.matcher.AddRule(domain, "match", i)
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()

	if bootstrap != "" && u.isDoH {
		// Use bootstrap DNS to resolve the DoH server hostname
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)
			// Resolve the DoH server using bootstrap
			ips, err := resolveWithBootstrap(host, bootstrap)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("bootstrap: no IPs for %s", host)
			}
			// Pick a random resolved IP
			ip := ips[rand.Intn(len(ips))]
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
		}
	}

	u.client = &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
	return u
}

func (u *UpstreamResolver) Resolve(ctx context.Context, msg *dns.Msg) (*dns.Msg, time.Duration, error) {
	start := time.Now()
	var (
		resp *dns.Msg
		err  error
	)
	if u.isDoH {
		resp, err = u.resolveDoH(ctx, msg)
	} else {
		resp, err = u.resolveUDP(ctx, msg)
	}
	return resp, time.Since(start), err
}

// recordAccepted updates this upstream's stats. It must be called only when
// the upstream's response was the one accepted by the ResolverGroup, so that
// counters reflect winning answers, not every concurrent attempt.
func (u *UpstreamResolver) recordAccepted(latency time.Duration, domain string) {
	ms := latency.Milliseconds()
	u.queries.Add(1)
	u.totalLatency.Add(ms)
	u.lastLatency.Store(ms)
	u.lastQueriedAt.Store(time.Now().UnixNano())
	if domain != "" {
		d := domain
		u.lastQueriedDomain.Store(&d)
	}
}

func (u *UpstreamResolver) Stats() UpstreamStats {
	queries := u.queries.Load()
	var average int64
	if queries > 0 {
		average = u.totalLatency.Load() / queries
	}
	var lastQueriedAt string
	if ts := u.lastQueriedAt.Load(); ts > 0 {
		lastQueriedAt = time.Unix(0, ts).Format(time.RFC3339Nano)
	}
	var lastDomain string
	if d := u.lastQueriedDomain.Load(); d != nil {
		lastDomain = *d
	}
	return UpstreamStats{
		URL:               u.url,
		Bootstrap:         u.bootstrap,
		Queries:           queries,
		AverageLatency:    average,
		LastLatency:       u.lastLatency.Load(),
		LastQueriedAt:     lastQueriedAt,
		LastQueriedDomain: lastDomain,
		Domains:           append([]string(nil), u.domains...),
	}
}

func (u *UpstreamResolver) resolveDoH(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	packed, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack dns msg: %w", err)
	}

	// Use GET with base64url encoding (RFC 8484)
	encoded := base64.RawURLEncoding.EncodeToString(packed)
	reqURL := u.url + "?dns=" + encoded

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65535))
	if err != nil {
		return nil, fmt.Errorf("doh read body: %w", err)
	}

	reply := new(dns.Msg)
	if err := reply.Unpack(body); err != nil {
		return nil, fmt.Errorf("doh unpack: %w", err)
	}
	return reply, nil
}

func (u *UpstreamResolver) resolveUDP(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	addr := u.url
	if !strings.Contains(addr, ":") {
		addr += ":53"
	}

	client := &dns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}
	reply, _, err := client.ExchangeContext(ctx, msg, addr)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// ResolverGroup sends queries to multiple upstreams concurrently and returns the first response.
type ResolveResult struct {
	Msg      *dns.Msg
	Upstream string
}

type ResolverGroup struct {
	upstreams []*UpstreamResolver
}

func NewResolverGroup(upstreams []*UpstreamResolver) *ResolverGroup {
	return &ResolverGroup{upstreams: upstreams}
}

func (g *ResolverGroup) UpstreamStats() []UpstreamStats {
	stats := make([]UpstreamStats, 0, len(g.upstreams))
	for _, upstream := range g.upstreams {
		stats = append(stats, upstream.Stats())
	}
	return stats
}

func (g *ResolverGroup) Resolve(ctx context.Context, msg *dns.Msg) (ResolveResult, error) {
	upstreams := g.selectUpstreams(msg)
	if len(upstreams) == 0 {
		return ResolveResult{}, fmt.Errorf("no upstream resolvers configured")
	}
	domain := questionDomain(msg)
	if len(upstreams) == 1 {
		m, latency, err := upstreams[0].Resolve(ctx, msg)
		if err != nil {
			return ResolveResult{}, err
		}
		upstreams[0].recordAccepted(latency, domain)
		return ResolveResult{Msg: m, Upstream: upstreams[0].url}, nil
	}

	// Concurrent query - return first success
	type result struct {
		msg      *dns.Msg
		upstream *UpstreamResolver
		latency  time.Duration
		err      error
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ch := make(chan result, len(upstreams))
	for _, u := range upstreams {
		go func() {
			m, latency, err := u.Resolve(ctx, msg)
			ch <- result{m, u, latency, err}
		}()
	}

	var lastErr error
	for range upstreams {
		r := <-ch
		if r.err == nil && r.msg != nil {
			r.upstream.recordAccepted(r.latency, domain)
			return ResolveResult{Msg: r.msg, Upstream: r.upstream.url}, nil
		}
		lastErr = r.err
	}
	return ResolveResult{}, fmt.Errorf("all upstreams failed, last error: %w", lastErr)
}

func questionDomain(msg *dns.Msg) string {
	if msg == nil || len(msg.Question) == 0 {
		return ""
	}
	return strings.TrimSuffix(msg.Question[0].Name, ".")
}

func (g *ResolverGroup) selectUpstreams(msg *dns.Msg) []*UpstreamResolver {
	if len(g.upstreams) == 0 || msg == nil || len(msg.Question) == 0 {
		return g.upstreams
	}
	domain := strings.TrimSuffix(msg.Question[0].Name, ".")
	var matched []*UpstreamResolver
	var defaults []*UpstreamResolver
	for _, upstream := range g.upstreams {
		if upstream.matcher == nil {
			defaults = append(defaults, upstream)
			continue
		}
		if _, ok := upstream.matcher.Match(domain); ok {
			matched = append(matched, upstream)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	if len(defaults) > 0 {
		return defaults
	}
	return g.upstreams
}

func resolveWithBootstrap(host, bootstrap string) ([]string, error) {
	if net.ParseIP(host) != nil {
		return []string{host}, nil
	}

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(host), dns.TypeA)

	addr := bootstrap
	if !strings.Contains(addr, ":") {
		addr += ":53"
	}

	client := &dns.Client{
		Net:     "udp",
		Timeout: 5 * time.Second,
	}
	reply, _, err := client.Exchange(msg, addr)
	if err != nil {
		return nil, err
	}

	var ips []string
	for _, rr := range reply.Answer {
		if a, ok := rr.(*dns.A); ok {
			ips = append(ips, a.A.String())
		}
	}
	return ips, nil
}
