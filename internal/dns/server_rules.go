package dns

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/dnsrule"
)

type RuleListEntry struct {
	Value       string    `json:"value"`
	Type        string    `json:"type"`
	Count       int       `json:"count"`
	Hits        int64     `json:"hits"`
	LastUpdated time.Time `json:"last_updated,omitempty"`
}

type ruleState struct {
	domainMatcher *dnsrule.Matcher
	directIPs     *IPSet
	rejectIPs     *IPSet
	ruleLists     map[string][]RuleListEntry
}

func newRuleState() *ruleState {
	return &ruleState{
		domainMatcher: dnsrule.NewMatcher(),
		directIPs:     NewIPSet(),
		rejectIPs:     NewIPSet(),
		ruleLists:     make(map[string][]RuleListEntry),
	}
}

func (r *ruleState) ipSetFor(decision string) *IPSet {
	switch decision {
	case config.DecisionDirect:
		return r.directIPs
	case config.DecisionReject:
		return r.rejectIPs
	}
	return nil
}

// LoadInitialRules completes server initialization after the caller has had a
// chance to publish the server pointer to any dial closures used by assets.
func (s *Server) LoadInitialRules() error {
	cfg, err := config.Snapshot()
	if err != nil {
		return err
	}
	if err := s.loadRules(cfg); err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	return nil
}

// onAssetReady is invoked by the asset manager once a remote rule list has
// finished downloading. Startup no longer blocks on rule downloads, so this
// is the path that populates the matchers when the async fetch completes.
func (s *Server) onAssetReady(url string) {
	if !s.isRuleSource(url) {
		return
	}
	if err := s.reloadRuleSource(url); err != nil {
		slog.Warn("update rules after async asset download failed", "url", url, "error", err)
		return
	}
	slog.Info("rules updated after async asset download", "url", url)
}

func (s *Server) isRuleSource(url string) bool {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.isRuleSourceLocked(url)
}

func (s *Server) isRuleSourceLocked(url string) bool {
	for _, rule := range s.rawRules.Domains {
		if rule.Source == url {
			return true
		}
	}
	for _, rule := range s.rawRules.CIDRs {
		if rule.Source == url {
			return true
		}
	}
	return false
}

// ReloadRules rebuilds the rule matchers from the last-loaded raw config,
// without triggering any remote fetches. Safe to call concurrently; callers
// are serialized via reloadMu.
func (s *Server) ReloadRules() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	cfg := &config.Config{}
	cfg.DNS.Rules = s.rawRules
	return s.loadRules(cfg)
}

// UpdateRules replaces the active rule set with the given rules and rebuilds
// the matchers.
func (s *Server) UpdateRules(rules config.DNSRules) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.rawRules = rules
	cfg := &config.Config{}
	cfg.DNS.Rules = rules
	return s.loadRules(cfg)
}

// Rules returns the raw rule configuration currently active on the server.
func (s *Server) Rules() config.DNSRules {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.rawRules
}

// Assets returns the asset manager backing this DNS server.
func (s *Server) Assets() *assets.Manager { return s.assets }

// RefreshSource forces an immediate refresh of source through the asset
// manager and reloads rule matchers so the new content is visible.
func (s *Server) RefreshSource(source string) error {
	if s.assets == nil {
		return fmt.Errorf("asset manager unavailable")
	}
	if err := s.assets.Refresh(source, false); err != nil {
		return err
	}
	if isRemoteSource(source) {
		return nil
	}
	return s.reloadRuleSource(source)
}

func (s *Server) reloadRuleSource(source string) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	domainAffected := false
	cidrDecisions := make(map[string]struct{})
	for _, rule := range s.rawRules.Domains {
		if rule.Source == source {
			domainAffected = true
		}
	}
	for _, rule := range s.rawRules.CIDRs {
		if rule.Source == source {
			cidrDecisions[rule.Decision] = struct{}{}
		}
	}
	if !domainAffected && len(cidrDecisions) == 0 {
		return nil
	}

	var domainMatcher *dnsrule.Matcher
	var domainEntries map[string][]RuleListEntry
	if domainAffected {
		domainMatcher, domainEntries = s.buildDomainMatcher(s.rawRules.Domains)
	}

	ipSets := make(map[string]*IPSet, len(cidrDecisions))
	cidrEntries := make(map[string][]RuleListEntry, len(cidrDecisions))
	for decision := range cidrDecisions {
		set, entries := s.buildIPSet(decision, s.rawRules.CIDRs)
		if set == nil {
			continue
		}
		if decision == config.DecisionDirect {
			lists := map[string][]RuleListEntry{"direct-cidrs": entries}
			addInternalDirectCIDRs(set, lists)
			entries = lists["direct-cidrs"]
		}
		set.Sort()
		ipSets[decision] = set
		cidrEntries[decision+"-cidrs"] = entries
	}

	s.rulesMu.Lock()
	if domainMatcher != nil {
		s.domainMatcher = domainMatcher
		for _, bucket := range []string{"reject-domains", "relay-domains", "direct-domains"} {
			entries := domainEntries[bucket]
			carryRuleHits(entries, s.ruleLists[bucket])
			s.ruleLists[bucket] = entries
		}
	}
	for decision, set := range ipSets {
		switch decision {
		case config.DecisionDirect:
			s.directIPs = set
		case config.DecisionReject:
			s.rejectIPs = set
		}
	}
	for bucket, entries := range cidrEntries {
		s.ruleLists[bucket] = entries
	}
	s.rulesMu.Unlock()
	return nil
}

func (s *Server) buildDomainMatcher(rules []config.DomainRule) (*dnsrule.Matcher, map[string][]RuleListEntry) {
	matcher := dnsrule.NewMatcher()
	ruleLists := make(map[string][]RuleListEntry)
	for i, rule := range rules {
		if !validDomainDecision(rule.Decision) {
			slog.Warn("invalid domain rule decision", "decision", rule.Decision, "source", rule.Source)
			continue
		}
		bucket := rule.Decision + "-domains"
		s.loadDomainRule(matcher, ruleLists, bucket, rule.Decision, rule.Source, i)
	}
	return matcher, ruleLists
}

func (s *Server) buildIPSet(decision string, rules []config.CIDRRule) (*IPSet, []RuleListEntry) {
	state := newRuleState()
	set := state.ipSetFor(decision)
	if set == nil {
		slog.Warn("invalid cidr rule decision", "decision", decision)
		return nil, nil
	}
	bucket := decision + "-cidrs"
	for _, rule := range rules {
		if rule.Decision != decision {
			continue
		}
		s.loadCIDRRule(set, state.ruleLists, bucket, decision, rule.Source)
	}
	set.Sort()
	return set, state.ruleLists[bucket]
}

func (s *Server) loadDomainRule(matcher *dnsrule.Matcher, ruleLists map[string][]RuleListEntry, bucket, decision, source string, order int) {
	if isSource(source) {
		n, err := dnsrule.Load(source, matcher, s.assets, decision, order)
		if err != nil {
			if errors.Is(err, assets.ErrNotCached) {
				slog.Info("domain list pending async download", "decision", decision, "source", source)
				ruleLists[bucket] = append(ruleLists[bucket], RuleListEntry{
					Value: source,
					Type:  "asset",
					Count: 0,
				})
			} else {
				slog.Warn("failed to load domain list", "decision", decision, "source", source, "error", err)
			}
			return
		}
		ruleEntry := RuleListEntry{Value: source, Type: "asset", Count: n}
		if s.assets != nil {
			if status, ok := s.assets.Status(source); ok {
				ruleEntry.LastUpdated = status.LastUpdated
			}
		}
		ruleLists[bucket] = append(ruleLists[bucket], ruleEntry)
		slog.Info("loaded domain list", "decision", decision, "source", source, "count", n)
		return
	}
	if err := matcher.AddRuleWithSource(source, source, decision, order); err != nil {
		slog.Warn("invalid inline domain rule", "decision", decision, "rule", source, "error", err)
		return
	}
	ruleType := "inline"
	if kind, _ := dnsrule.Split(source); kind == dnsrule.KindQType {
		ruleType = "qtype"
	}
	ruleLists[bucket] = append(ruleLists[bucket], RuleListEntry{
		Value: source,
		Type:  ruleType,
		Count: 1,
	})
}

func (s *Server) loadCIDRRule(set *IPSet, ruleLists map[string][]RuleListEntry, bucket, decision, source string) {
	if isSource(source) {
		n, err := LoadIPSet(source, set, s.assets)
		if err != nil {
			if errors.Is(err, assets.ErrNotCached) {
				slog.Info("CIDR list pending async download", "decision", decision, "source", source)
				ruleLists[bucket] = append(ruleLists[bucket], RuleListEntry{
					Value: source,
					Type:  "asset",
					Count: 0,
				})
			} else {
				slog.Warn("failed to load CIDR list", "decision", decision, "source", source, "error", err)
			}
			return
		}
		ruleEntry := RuleListEntry{Value: source, Type: "asset", Count: n}
		if s.assets != nil {
			if status, ok := s.assets.Status(source); ok {
				ruleEntry.LastUpdated = status.LastUpdated
			}
		}
		ruleLists[bucket] = append(ruleLists[bucket], ruleEntry)
		slog.Info("loaded CIDR list", "decision", decision, "source", source, "count", n)
		return
	}
	prefix, err := netip.ParsePrefix(source)
	if err != nil {
		slog.Warn("invalid inline CIDR", "decision", decision, "cidr", source, "error", err)
		return
	}
	set.AddWithSource(prefix, prefix.Masked().String())
	ruleLists[bucket] = append(ruleLists[bucket], RuleListEntry{
		Value: prefix.Masked().String(),
		Type:  "inline",
		Count: 1,
	})
}

func (s *Server) loadRules(cfg *config.Config) error {
	rules := cfg.DNS.Rules
	state := newRuleState()

	for i, rule := range rules.Domains {
		if !validDomainDecision(rule.Decision) {
			slog.Warn("invalid domain rule decision", "decision", rule.Decision, "source", rule.Source)
			continue
		}
		bucket := rule.Decision + "-domains"
		s.loadDomainRule(state.domainMatcher, state.ruleLists, bucket, rule.Decision, rule.Source, i)
	}

	for _, rule := range rules.CIDRs {
		set := state.ipSetFor(rule.Decision)
		if set == nil {
			slog.Warn("invalid cidr rule decision", "decision", rule.Decision, "source", rule.Source)
			continue
		}
		bucket := rule.Decision + "-cidrs"
		s.loadCIDRRule(set, state.ruleLists, bucket, rule.Decision, rule.Source)
	}

	state.directIPs.Sort()
	state.rejectIPs.Sort()
	addInternalDirectCIDRs(state.directIPs, state.ruleLists)
	state.directIPs.Sort()

	s.rulesMu.Lock()
	s.domainMatcher = state.domainMatcher
	s.directIPs = state.directIPs
	s.rejectIPs = state.rejectIPs
	s.ruleLists = state.ruleLists
	s.rulesMu.Unlock()

	return nil
}

func (s *Server) domainMatchSource(decision string, domain string) string {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	if s.domainMatcher == nil {
		return ""
	}
	match, ok := s.domainMatcher.Match(domain)
	if !ok || match.Decision != decision {
		return ""
	}
	return match.Source
}

func (s *Server) matchDNSRule(domain string, qtype uint16) (dnsrule.Match, bool) {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	if s.domainMatcher == nil {
		return dnsrule.Match{}, false
	}
	return s.domainMatcher.MatchQuery(domain, qtype)
}

func (s *Server) directIPSource(ip netip.Addr) string {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	return s.directIPs.ContainsSource(ip)
}

// RuleListSnapshot returns a copy of the per-source rule statistics keyed by
// bucket (e.g. "direct-domains", "reject-cidrs"). Each entry includes the
// loaded count, hit counter, and last-updated time.
func (s *Server) RuleListSnapshot() map[string][]RuleListEntry {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	out := make(map[string][]RuleListEntry, len(s.ruleLists))
	for bucket, entries := range s.ruleLists {
		copied := make([]RuleListEntry, len(entries))
		copy(copied, entries)
		out[bucket] = copied
	}
	return out
}

func (s *Server) incrementRuleHit(bucket, source string) {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	for i := range s.ruleLists[bucket] {
		if s.ruleLists[bucket][i].Value == source {
			s.ruleLists[bucket][i].Hits++
			return
		}
	}
}

func validDomainDecision(decision string) bool {
	switch decision {
	case config.DecisionReject, config.DecisionRelay, config.DecisionDirect:
		return true
	default:
		return false
	}
}

func carryRuleHits(next []RuleListEntry, previous []RuleListEntry) {
	hits := make(map[string]int64, len(previous))
	for _, entry := range previous {
		hits[entry.Value] += entry.Hits
	}
	for i := range next {
		next[i].Hits = hits[next[i].Value]
	}
}

// isSource returns true if the entry looks like a file path or URL
// rather than an inline rule (domain:, keyword:, full:, regexp:, or bare CIDR).
func isSource(entry string) bool {
	return strings.HasPrefix(entry, "http://") ||
		strings.HasPrefix(entry, "https://") ||
		strings.HasPrefix(entry, "/") ||
		strings.HasPrefix(entry, "./") ||
		strings.HasPrefix(entry, "../") ||
		strings.HasPrefix(entry, "~/")
}

func isRemoteSource(entry string) bool {
	return strings.HasPrefix(entry, "http://") || strings.HasPrefix(entry, "https://")
}

func addInternalDirectCIDRs(set *IPSet, ruleLists map[string][]RuleListEntry) {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	} {
		prefix, _ := netip.ParsePrefix(cidr)
		value := prefix.Masked().String()
		set.AddWithSource(prefix, value)
		ruleLists["direct-cidrs"] = append(ruleLists["direct-cidrs"], RuleListEntry{
			Value: value,
			Type:  "internal",
			Count: 1,
		})
	}
}
