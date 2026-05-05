package dns

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/fakeip"
)

func (s *Server) resolveAndClassifyWithResolver(ctx context.Context, r *dns.Msg, domain string, qtype uint16, disableFakeIP bool, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, Decision, string, string) {
	if cached, ok := s.cache.lookup(domain, qtype); ok {
		if !cached.stale && !(respectAnswerTTL && cached.answerMinTTL() == 0) {
			s.cacheHits.Add(1)
			return s.processCachedResponse(r, domain, qtype, disableFakeIP, cached, "Cache")
		}
		if !respectAnswerTTL {
			s.refreshCacheAsync(domain, qtype, r.Copy(), resolverOverride)
		}
		return s.processCachedResponse(r, domain, qtype, disableFakeIP, cached, "Cache (stale)")
	}

	resp, upstream := s.resolveUpstreamWithResolver(ctx, r, resolverOverride)
	if resp == nil {
		return nil, DecisionIgnore, "resolve-failed", ""
	}

	s.cache.Put(domain, qtype, resp, upstream)
	return s.processUpstreamResponse(r, domain, qtype, disableFakeIP, resp, upstream)
}

func (s *Server) resolveAndCacheWithResolver(ctx context.Context, r *dns.Msg, domain string, qtype uint16, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, string) {
	if cached, ok := s.cache.lookup(domain, qtype); ok {
		if !cached.stale && !(respectAnswerTTL && cached.answerMinTTL() == 0) {
			s.cacheHits.Add(1)
			return cached.message(), "Cache"
		}
		if !respectAnswerTTL {
			s.refreshCacheAsync(domain, qtype, r.Copy(), resolverOverride)
			return cached.message(), "Cache (stale)"
		}
	}

	resp, upstream := s.resolveUpstreamWithResolver(ctx, r, resolverOverride)
	if resp != nil {
		s.cache.Put(domain, qtype, resp, upstream)
	}
	return resp, upstream
}

func (s *Server) resolveUpstreamWithResolver(ctx context.Context, r *dns.Msg, resolverOverride *ResolverGroup) (*dns.Msg, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resolver := s.currentResolver()
	if resolverOverride != nil {
		resolver = resolverOverride
	}
	s.upstreamRequests.Add(1)
	result, err := resolver.Resolve(ctx, r)
	if err != nil {
		slog.Debug("upstream resolve failed", "error", err)
		return nil, ""
	}
	return result.Msg, result.Upstream
}

func (s *Server) refreshCacheAsync(domain string, qtype uint16, r *dns.Msg, resolverOverride *ResolverGroup) {
	key := s.refreshCacheKey(domain, qtype, resolverOverride)

	s.refreshMu.Lock()
	if s.refreshing == nil {
		s.refreshing = make(map[string]struct{})
	}
	if _, ok := s.refreshing[key]; ok {
		s.refreshMu.Unlock()
		return
	}
	s.refreshing[key] = struct{}{}
	s.refreshMu.Unlock()

	go func() {
		defer func() {
			s.refreshMu.Lock()
			delete(s.refreshing, key)
			s.refreshMu.Unlock()
		}()
		_, _, _ = s.refreshGroup.Do(key, func() (any, error) {
			s.refreshCacheWithResolver(domain, qtype, r, resolverOverride)
			return nil, nil
		})
	}()
}

func (s *Server) refreshCacheKey(domain string, qtype uint16, resolverOverride *ResolverGroup) string {
	resolver := s.currentResolver()
	if resolverOverride != nil {
		resolver = resolverOverride
	}
	return fmt.Sprintf("%s:%p", cacheKey(domain, qtype), resolver)
}

func (s *Server) refreshCacheWithResolver(domain string, qtype uint16, r *dns.Msg, resolverOverride *ResolverGroup) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resolver := s.currentResolver()
	if resolverOverride != nil {
		resolver = resolverOverride
	}
	s.upstreamRequests.Add(1)
	result, err := resolver.Resolve(ctx, r)
	if err != nil {
		return
	}
	s.cache.Put(domain, qtype, result.Msg, result.Upstream)
}

func (s *Server) FakeIPPool() *fakeip.Pool { return s.fakeIPPool }
func (s *Server) Cache() *Cache            { return s.cache }

func (s *Server) UpstreamStats() []UpstreamStats {
	return s.currentResolver().UpstreamStats()
}

func (s *Server) UpdateUpstreams(upstreams []config.Upstream) {
	next := make([]*UpstreamResolver, 0, len(upstreams))
	for _, upstream := range upstreams {
		next = append(next, NewUpstreamResolver(upstream.URL, upstream.Bootstrap, upstream.Domains...))
	}
	if len(next) == 0 {
		next = append(next, NewUpstreamResolver("8.8.8.8:53", ""))
	}

	s.resolverMu.Lock()
	s.resolver = NewResolverGroup(next)
	s.resolverMu.Unlock()
}

func (s *Server) currentResolver() *ResolverGroup {
	s.resolverMu.RLock()
	defer s.resolverMu.RUnlock()
	return s.resolver
}

func (s *Server) FlushCache() {
	s.cache.Flush()
}

func (s *Server) ResolveRelayDomain(ctx context.Context, groupName, host string, upstreams []config.Upstream) ([]netip.Addr, time.Time, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, time.Time{}, nil
	}

	var resolverOverride *ResolverGroup
	if len(upstreams) > 0 {
		overrideUpstreams := make([]*UpstreamResolver, 0, len(upstreams))
		for _, upstream := range upstreams {
			overrideUpstreams = append(overrideUpstreams, NewUpstreamResolver(upstream.URL, upstream.Bootstrap, upstream.Domains...))
		}
		resolverOverride = NewResolverGroup(overrideUpstreams)
	}

	source := "relay"
	if groupName != "" {
		source = "relay:" + groupName
	}

	var ips []netip.Addr
	var relayExpiry time.Time
	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(host), qtype)
		reply, _, _, _, err := s.serveMsgWithOptions(ctx, msg, source, true, resolverOverride, true)
		if err != nil || reply == nil {
			continue
		}
		if ttl := answerMinTTL(reply); ttl > 0 {
			expiresAt := time.Now().Add(time.Duration(ttl) * time.Second)
			if relayExpiry.IsZero() || expiresAt.Before(relayExpiry) {
				relayExpiry = expiresAt
			}
		}
		for _, rr := range reply.Answer {
			switch ans := rr.(type) {
			case *dns.A:
				if ip, ok := netip.AddrFromSlice(ans.A); ok {
					ips = append(ips, ip.Unmap())
				}
			case *dns.AAAA:
				if ip, ok := netip.AddrFromSlice(ans.AAAA); ok {
					ips = append(ips, ip.Unmap())
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, time.Time{}, fmt.Errorf("no addresses for %s", host)
	}
	return ips, relayExpiry, nil
}

func (s *Server) lookupFakeIP(domain string, qtype uint16) (netip.Addr, bool) {
	family := fakeip.FamilyIPv4
	if qtype == dns.TypeAAAA {
		family = fakeip.FamilyIPv6
	}
	if !s.fakeIPPool.HasFamily(family) {
		return netip.Addr{}, false
	}
	result := s.fakeIPPool.LookupResultForFamily(domain, family)
	if !result.Mapping.IP.IsValid() {
		return netip.Addr{}, false
	}
	return result.Mapping.IP, true
}
