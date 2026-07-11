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

func (s *Server) resolveAndClassifyWithResolver(ctx context.Context, r *dns.Msg, domain string, qtype uint16, disableFakeIP bool, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, Decision, string, string, string) {
	result, err := s.resolveCachedWithResolver(ctx, r, domain, qtype, resolverOverride, respectAnswerTTL)
	if err != nil || result.msg == nil && !result.cached {
		return nil, DecisionIgnore, "resolve-failed", "", ""
	}
	if result.cached {
		if !result.stale {
			s.cacheHits.Add(1)
		}
		return s.processCachedResponse(r, domain, qtype, disableFakeIP, result.hit, result.upstream)
	}

	return s.processUpstreamResponse(r, domain, qtype, disableFakeIP, result.msg, result.upstream, result.queryResult)
}

func (s *Server) resolveAndCacheWithResolver(ctx context.Context, r *dns.Msg, domain string, qtype uint16, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, string, string) {
	result, err := s.resolveCachedWithResolver(ctx, r, domain, qtype, resolverOverride, respectAnswerTTL)
	if err != nil {
		return nil, "", ""
	}
	if result.cached {
		if !result.stale {
			s.cacheHits.Add(1)
		}
		return result.hit.message(), result.upstream, result.queryResult
	}
	return result.msg, result.upstream, result.queryResult
}

func (s *Server) resolveCachedWithResolver(ctx context.Context, r *dns.Msg, domain string, qtype uint16, resolverOverride *ResolverGroup, respectAnswerTTL bool) (cachedDNSResult, error) {
	var refreshStale func()
	if !respectAnswerTTL {
		refreshStale = func() {
			s.refreshCacheAsync(domain, qtype, r.Copy(), resolverOverride)
		}
	}
	return resolveCachedDNS(ctx, cachedDNSOptions{
		cache:                         s.cache,
		name:                          domain,
		qtype:                         qtype,
		msg:                           r,
		upstreams:                     s.cacheUpstreamsFor(r, resolverOverride),
		respectAnswerTTL:              respectAnswerTTL,
		refreshStale:                  refreshStale,
		fallbackToStaleOnResolveError: respectAnswerTTL,
		resolve: func(ctx context.Context, msg *dns.Msg) (*dns.Msg, string, error) {
			resp, upstream := s.resolveUpstreamWithResolver(ctx, msg, resolverOverride)
			return resp, upstream, nil
		},
	})
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

func (s *Server) cacheUpstreamsFor(r *dns.Msg, resolverOverride *ResolverGroup) []string {
	resolver := s.currentResolver()
	if resolverOverride != nil {
		resolver = resolverOverride
	}
	if resolver == nil {
		return nil
	}
	return resolver.selectedUpstreamURLs(r)
}

func (s *Server) FakeIPPool() *fakeip.Pool { return s.fakeIPPool }
func (s *Server) Cache() *Cache            { return s.cache }

func (s *Server) UpstreamStats() []UpstreamStats {
	return s.currentResolver().UpstreamStats()
}

func (s *Server) UpdateUpstreams(upstreams []config.Upstream) {
	next := make([]*UpstreamResolver, 0, len(upstreams))
	for _, upstream := range upstreams {
		next = append(next, NewUpstreamResolverWithCache(upstream.URL, upstream.Bootstrap, s.cache, upstream.Domains...))
	}
	if len(next) == 0 {
		next = append(next, NewUpstreamResolverWithCache("8.8.8.8:53", "", s.cache))
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

func (s *Server) ResolveRelayDomain(ctx context.Context, groupName, host string) ([]netip.Addr, time.Time, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, time.Time{}, nil
	}

	source := "relay"
	if groupName != "" {
		source = "relay:" + groupName
	}

	qtypes := []uint16{dns.TypeA, dns.TypeAAAA}
	results := make(chan relayDomainResolveResult, len(qtypes))
	for _, qtype := range qtypes {
		go func(qtype uint16) {
			results <- s.resolveRelayDomainFamily(ctx, source, host, qtype)
		}(qtype)
	}

	byQType := make(map[uint16]relayDomainResolveResult, len(qtypes))
	for range qtypes {
		result := <-results
		byQType[result.qtype] = result
	}

	var ips []netip.Addr
	var relayExpiry time.Time
	for _, qtype := range qtypes {
		result := byQType[qtype]
		if result.err != nil {
			continue
		}
		ips = append(ips, result.ips...)
		if !result.expiresAt.IsZero() && (relayExpiry.IsZero() || result.expiresAt.Before(relayExpiry)) {
			relayExpiry = result.expiresAt
		}
	}
	if len(ips) == 0 {
		return nil, time.Time{}, fmt.Errorf("no addresses for %s", host)
	}
	return ips, relayExpiry, nil
}

type relayDomainResolveResult struct {
	qtype     uint16
	ips       []netip.Addr
	expiresAt time.Time
	err       error
}

func (s *Server) resolveRelayDomainFamily(ctx context.Context, source, host string, qtype uint16) relayDomainResolveResult {
	result := relayDomainResolveResult{qtype: qtype}
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(host), qtype)

	reply, _, _, _, _, err := s.serveMsgWithOptions(ctx, msg, source, true, nil, true)
	if err != nil {
		result.err = err
		return result
	}
	if reply == nil {
		result.err = fmt.Errorf("no DNS response")
		return result
	}
	if ttl := answerMinTTL(reply); ttl > 0 {
		result.expiresAt = time.Now().Add(time.Duration(ttl) * time.Second)
	}
	for _, rr := range reply.Answer {
		switch ans := rr.(type) {
		case *dns.A:
			if qtype != dns.TypeA {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ans.A); ok {
				result.ips = append(result.ips, ip.Unmap())
			}
		case *dns.AAAA:
			if qtype != dns.TypeAAAA {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ans.AAAA); ok {
				result.ips = append(result.ips, ip.Unmap())
			}
		}
	}
	return result
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
