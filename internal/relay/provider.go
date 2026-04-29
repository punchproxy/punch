package relay

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"gopkg.in/yaml.v3"
)

type relayPayload struct {
	Proxies   []map[string]any  `yaml:"proxies"`
	Resolvers []config.Upstream `yaml:"resolvers"`
}

// onAssetReady is invoked by the asset manager once a remote relay payload
// has finished downloading. Startup no longer blocks on these downloads, so
// this is the path that materializes relay dialers when the async fetch
// completes.
func (s *Selector) onAssetReady(url string) {
	s.mu.RLock()
	var matches []string
	for name, cfg := range s.groupCfgs {
		if cfg.Type == "remote" && cfg.URL == url {
			matches = append(matches, name)
		}
	}
	s.mu.RUnlock()
	for _, name := range matches {
		if err := s.ReloadGroup(name); err != nil {
			slog.Warn("reload relay group after async asset download failed", "group", name, "error", err)
			continue
		}
		slog.Info("relay group reloaded after async asset download", "group", name)
	}
}

func (s *Selector) buildGroup(cfg config.RelayGroup, assetManager *assets.Manager, urlCache map[string]relayPayload) (*group, error) {
	name := cfg.Name
	if name == "" {
		return nil, fmt.Errorf("relay group missing name")
	}
	g := &group{name: name, mode: normalizeSelectMode(cfg.Select), sourceURL: cfg.URL}
	if cfg.RefreshDuration > 0 {
		g.refreshEvery = time.Duration(cfg.RefreshDuration) * time.Second
	}

	var mappings []map[string]any
	upstreams := cfg.RelayDomainResolver
	switch cfg.Type {
	case "remote":
		slog.Debug("load remote relay group", "group", name, "url", cfg.URL)
		payload, ok := urlCache[cfg.URL]
		if !ok {
			loaded, err := loadRemotePayload(cfg.URL, assetManager)
			if err != nil {
				g.loadError = err.Error()
				if errors.Is(err, assets.ErrNotCached) {
					slog.Info("relay group pending async download", "group", name, "url", cfg.URL)
				} else {
					slog.Warn("relay group unavailable", "group", name, "error", err)
				}
				return g, nil
			}
			urlCache[cfg.URL] = loaded
			slog.Debug("cached remote relay payload", "group", name, "url", cfg.URL, "proxies", len(loaded.Proxies), "resolvers", len(loaded.Resolvers))
			payload = loaded
		} else {
			slog.Debug("reuse remote relay payload", "group", name, "url", cfg.URL, "proxies", len(payload.Proxies), "resolvers", len(payload.Resolvers))
		}
		mappings = payload.Proxies
		if len(payload.Resolvers) > 0 {
			upstreams = payload.Resolvers
		}
		g.resolvers = cloneUpstreams(upstreams)
		if status, ok := assetManager.Status(cfg.URL); ok {
			g.lastRefreshedAt = status.LastUpdated
			if g.refreshEvery > 0 {
				g.nextRefreshAt = status.LastUpdated.Add(g.refreshEvery)
			}
		}
	case "inline":
		slog.Debug("load inline relay group", "group", name, "proxies", len(cfg.Proxies))
		mappings = cfg.Proxies
		g.resolvers = cloneUpstreams(upstreams)
	default:
		return nil, fmt.Errorf("unsupported relay group type %q", cfg.Type)
	}

	filtered, err := filterRelayMappings(mappings, cfg.Keep, cfg.Remove)
	if err != nil {
		return nil, fmt.Errorf("filter relay group %q: %w", name, err)
	}
	slog.Debug("filtered relay group", "group", name, "input", len(mappings), "filtered", len(filtered), "keep", cfg.Keep, "remove", cfg.Remove)
	if len(filtered) == 0 {
		slog.Warn("relay group has no usable proxies", "group", name)
		g.loadError = "no usable proxies"
		return g, nil
	}

	dialers := make([]Dialer, 0, len(filtered))
	for _, mapping := range filtered {
		dialer, err := s.buildDialer(name, mapping, upstreams)
		if err != nil {
			slog.Warn("skip invalid relay in relay group", "group", name, "error", err)
			continue
		}
		dialers = append(dialers, dialer)
	}
	if len(dialers) == 0 {
		g.loadError = "no valid relays"
		return g, nil
	}

	g.dialers = dialers
	g.specs = make(map[string]map[string]any, len(filtered))
	for _, mapping := range filtered {
		name, _ := mapping["name"].(string)
		if name != "" {
			g.specs[name] = cloneRelayMapping(mapping)
		}
	}
	g.active.Store(0)
	slog.Debug("relay group ready", "group", name, "dialers", len(dialers), "mode", g.mode)
	return g, nil
}

func loadRemotePayload(url string, assetManager *assets.Manager) (relayPayload, error) {
	reader, err := assetManager.OpenDirect(url)
	if err != nil {
		return relayPayload{}, err
	}
	defer reader.Close()

	var schema relayPayload
	if err := yaml.NewDecoder(reader).Decode(&schema); err != nil {
		return relayPayload{}, fmt.Errorf("parse relay payload %s: %w", url, err)
	}
	if len(schema.Proxies) == 0 {
		return relayPayload{}, fmt.Errorf("relay payload %s has no relays", url)
	}
	schema.Proxies = dedupeRelayMappings(schema.Proxies)
	slog.Debug("parsed remote relay payload", "url", url, "proxies", len(schema.Proxies), "resolvers", len(schema.Resolvers))
	return schema, nil
}

func cloneUpstreams(upstreams []config.Upstream) []config.Upstream {
	if len(upstreams) == 0 {
		return nil
	}
	out := make([]config.Upstream, len(upstreams))
	for i, upstream := range upstreams {
		out[i] = upstream
		if len(upstream.Domains) > 0 {
			out[i].Domains = append([]string(nil), upstream.Domains...)
		}
	}
	return out
}

func dedupeRelayMappings(mappings []map[string]any) []map[string]any {
	used := make(map[string]struct{}, len(mappings))
	result := make([]map[string]any, 0, len(mappings))
	for _, mapping := range mappings {
		name, _ := mapping["name"].(string)
		if name == "" {
			result = append(result, mapping)
			continue
		}
		next := name
		if _, ok := used[next]; ok {
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("%s-%d", name, i)
				if _, exists := used[candidate]; !exists {
					next = candidate
					break
				}
			}
			clone := make(map[string]any, len(mapping))
			for k, v := range mapping {
				clone[k] = v
			}
			clone["name"] = next
			mapping = clone
			slog.Warn("renamed duplicate relay in provider", "from", name, "to", next)
		}
		used[next] = struct{}{}
		result = append(result, mapping)
	}
	return result
}

func filterRelayMappings(mappings []map[string]any, keepExpr, removeExpr string) ([]map[string]any, error) {
	var keep, remove *regexp.Regexp
	var err error
	if keepExpr != "" {
		keep, err = regexp.Compile(keepExpr)
		if err != nil {
			return nil, err
		}
	}
	if keep == nil && removeExpr != "" {
		remove, err = regexp.Compile(removeExpr)
		if err != nil {
			return nil, err
		}
	}

	filtered := make([]map[string]any, 0, len(mappings))
	for _, mapping := range mappings {
		name, _ := mapping["name"].(string)
		if name == "" {
			continue
		}
		if keep != nil {
			if !keep.MatchString(name) {
				continue
			}
		} else if remove != nil && remove.MatchString(name) {
			continue
		}
		filtered = append(filtered, mapping)
	}
	return filtered, nil
}

func (s *Selector) buildDialer(groupName string, mapping map[string]any, upstreams []config.Upstream) (Dialer, error) {
	if len(upstreams) == 0 {
		return NewDialerFromMapping(mapping)
	}
	if s.resolveRelayDomain == nil {
		return nil, fmt.Errorf("relay resolver unavailable")
	}
	return NewLazyRelayDialer(groupName, mapping, upstreams, s.resolveRelayDomain)
}

func (s *Selector) directGroup() *group {
	return &group{
		name:    directGroupName,
		mode:    "manual",
		dialers: []Dialer{NewDirectDialer(s.directDialContext)},
	}
}

func (s *Selector) buildGroups(cfg config.Relay) ([]*group, map[string]config.RelayGroup, error) {
	urlCache := make(map[string]relayPayload)
	groups := make([]*group, 0, len(cfg.Groups)+1)
	groupCfgs := make(map[string]config.RelayGroup, len(cfg.Groups))
	for _, gcfg := range cfg.Groups {
		slog.Debug("initialize relay group", "group", gcfg.Name, "type", gcfg.Type, "url", gcfg.URL, "select", gcfg.Select)
		g, err := s.buildGroup(gcfg, s.assets, urlCache)
		if err != nil {
			return nil, nil, err
		}
		if g == nil {
			continue
		}
		groupCfgs[gcfg.Name] = gcfg
		groups = append(groups, g)
	}
	groups = append(groups, s.directGroup())
	return groups, groupCfgs, nil
}
