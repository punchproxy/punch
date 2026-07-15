package dns

import (
	"context"
	"log/slog"

	"github.com/miekg/dns"
)

type cachedDNSResolveFunc func(context.Context, *dns.Msg) (*dns.Msg, string, error)

type cachedDNSOptions struct {
	cache                         *Cache
	name                          string
	qtype                         uint16
	msg                           *dns.Msg
	upstreams                     []string
	respectAnswerTTL              bool
	refreshStale                  func()
	fallbackToStaleOnResolveError bool
	resolve                       cachedDNSResolveFunc
}

type cachedDNSResult struct {
	msg         *dns.Msg
	hit         cacheHit
	upstream    string
	queryResult string
	cached      bool
	stale       bool
}

func resolveCachedDNS(ctx context.Context, opts cachedDNSOptions) (cachedDNSResult, error) {
	var stale cachedDNSResult
	var hasStale bool

	if opts.cache != nil {
		if hit, ok := opts.cache.lookupForUpstreams(opts.name, opts.qtype, opts.upstreams); ok {
			result := cachedDNSResult{
				hit:         hit,
				upstream:    "Cache",
				queryResult: hit.queryResult,
				cached:      true,
				stale:       hit.stale,
			}
			if hit.stale || opts.respectAnswerTTL && hit.answerMinTTL() == 0 {
				result.stale = true
				result.upstream = "Cache (stale)"
				if opts.refreshStale != nil {
					opts.refreshStale()
					return result, nil
				}
				stale = result
				hasStale = true
			} else if !(opts.respectAnswerTTL && hit.answerMinTTL() == 0) {
				return result, nil
			}
		}
	}

	if opts.resolve == nil {
		if hasStale {
			return stale, nil
		}
		return cachedDNSResult{}, nil
	}

	resp, upstream, err := opts.resolve(ctx, opts.msg)
	if err != nil {
		if opts.fallbackToStaleOnResolveError && hasStale {
			slog.Warn("dns re-query failed, serving stale cached answer", "name", opts.name, "qtype", cacheQType(opts.qtype), "error", err)
			return stale, nil
		}
		return cachedDNSResult{}, err
	}
	if resp == nil && opts.fallbackToStaleOnResolveError && hasStale {
		slog.Warn("dns re-query failed, serving stale cached answer", "name", opts.name, "qtype", cacheQType(opts.qtype))
		return stale, nil
	}

	result := cachedDNSResult{
		msg:      resp,
		upstream: upstream,
	}
	if resp != nil && opts.cache != nil {
		result.queryResult = opts.cache.PutForUpstream(opts.name, opts.qtype, resp, upstream)
	}
	return result, nil
}
