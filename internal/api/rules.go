package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	pdns "github.com/punchproxy/punch/internal/dns"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/dnsrule"
)

type ruleEntry struct {
	Index       int       `json:"index"`
	Decision    string    `json:"decision"`
	Source      string    `json:"source"`
	Type        string    `json:"type,omitempty"`
	Count       int       `json:"count"`
	Hits        int64     `json:"hits"`
	LastUpdated time.Time `json:"last_updated,omitempty"`
	NextUpdate  time.Time `json:"next_update,omitempty"`
	Default     bool      `json:"default,omitempty"`
}

type ruleRequest struct {
	Decision  string `json:"decision"`
	Source    string `json:"source"`
	NewSource string `json:"new_source,omitempty"`
	Index     *int   `json:"index,omitempty"`
}

func (s *Server) handleDNSRules(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.listDomainRules(cfg.DNS.Rules.Domains))
}

func (s *Server) handleDNSRoutes(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.listCIDRRules(cfg.DNS.Rules.CIDRs))
}

func (s *Server) handleCreateDNSRule(w http.ResponseWriter, r *http.Request) {
	s.handleCreateRule(w, r, config.KindDomain)
}

func (s *Server) handleCreateDNSRoute(w http.ResponseWriter, r *http.Request) {
	s.handleCreateRule(w, r, config.KindCIDR)
}

func (s *Server) handleUpdateDNSRule(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateRule(w, r, config.KindDomain)
}

func (s *Server) handleUpdateDNSRoute(w http.ResponseWriter, r *http.Request) {
	s.handleUpdateRule(w, r, config.KindCIDR)
}

func (s *Server) handleDeleteDNSRule(w http.ResponseWriter, r *http.Request) {
	s.handleDeleteRule(w, r, config.KindDomain)
}

func (s *Server) handleDeleteDNSRoute(w http.ResponseWriter, r *http.Request) {
	s.handleDeleteRule(w, r, config.KindCIDR)
}

func (s *Server) handleMoveDNSRule(w http.ResponseWriter, r *http.Request) {
	s.handleMoveRule(w, r, config.KindDomain)
}

func (s *Server) handleMoveDNSRoute(w http.ResponseWriter, r *http.Request) {
	s.handleMoveRule(w, r, config.KindCIDR)
}

func (s *Server) handleRefreshDNSRule(w http.ResponseWriter, r *http.Request) {
	s.handleRefreshRule(w, r, config.KindDomain)
}

func (s *Server) handleRefreshDNSRoute(w http.ResponseWriter, r *http.Request) {
	s.handleRefreshRule(w, r, config.KindCIDR)
}

type ruleAccessor struct {
	kind     string
	getAll   func(*config.DNSRules) []ruleSource
	setAll   func(*config.DNSRules, []ruleSource)
	validate func(decision, source string) (string, error)
}

type ruleSource struct {
	Decision string
	Source   string
}

func (s *Server) accessor(kind string) ruleAccessor {
	switch kind {
	case config.KindDomain:
		return ruleAccessor{
			kind: kind,
			getAll: func(r *config.DNSRules) []ruleSource {
				out := make([]ruleSource, len(r.Domains))
				for i, rule := range r.Domains {
					out[i] = ruleSource{Decision: rule.Decision, Source: rule.Source}
				}
				return out
			},
			setAll: func(r *config.DNSRules, rules []ruleSource) {
				r.Domains = make([]config.DomainRule, len(rules))
				for i, rule := range rules {
					r.Domains[i] = config.DomainRule{Decision: rule.Decision, Source: rule.Source}
				}
			},
			validate: validateDomainRule,
		}
	case config.KindCIDR:
		return ruleAccessor{
			kind: kind,
			getAll: func(r *config.DNSRules) []ruleSource {
				out := make([]ruleSource, len(r.CIDRs))
				for i, rule := range r.CIDRs {
					out[i] = ruleSource{Decision: rule.Decision, Source: rule.Source}
				}
				return out
			},
			setAll: func(r *config.DNSRules, rules []ruleSource) {
				r.CIDRs = make([]config.CIDRRule, len(rules))
				for i, rule := range rules {
					r.CIDRs[i] = config.CIDRRule{Decision: rule.Decision, Source: rule.Source}
				}
			},
			validate: validateCIDRRule,
		}
	}
	return ruleAccessor{}
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request, kind string) {
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rule: " + err.Error()})
		return
	}
	acc := s.accessor(kind)
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	source, err := acc.validate(decision, req.Source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rules := acc.getAll(&cfg.DNS.Rules)
	for _, existing := range rules {
		if existing.Decision == decision && existing.Source == source {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "rule already exists"})
			return
		}
	}
	idx := len(rules)
	if req.Index != nil {
		idx = *req.Index
		if idx < 0 || idx > len(rules) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("index %d out of range", idx)})
			return
		}
	}
	next := make([]ruleSource, 0, len(rules)+1)
	next = append(next, rules[:idx]...)
	next = append(next, ruleSource{Decision: decision, Source: source})
	next = append(next, rules[idx:]...)
	acc.setAll(&cfg.DNS.Rules, next)

	if !s.saveAndApplyRules(w, cfg) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":   "ok",
		"decision": decision,
		"source":   source,
		"index":    idx,
	})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request, kind string) {
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rule: " + err.Error()})
		return
	}
	if req.Index == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing index"})
		return
	}
	acc := s.accessor(kind)

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rules := acc.getAll(&cfg.DNS.Rules)
	if *req.Index < 0 || *req.Index >= len(rules) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	}
	current := rules[*req.Index]
	decision := current.Decision
	if strings.TrimSpace(req.Decision) != "" {
		decision = strings.ToLower(strings.TrimSpace(req.Decision))
	}
	source := current.Source
	if strings.TrimSpace(req.NewSource) != "" {
		source = req.NewSource
	} else if strings.TrimSpace(req.Source) != "" {
		source = req.Source
	}
	source, err = acc.validate(decision, source)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for i, existing := range rules {
		if i == *req.Index {
			continue
		}
		if existing.Decision == decision && existing.Source == source {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "rule already exists"})
			return
		}
	}
	rules[*req.Index] = ruleSource{Decision: decision, Source: source}
	acc.setAll(&cfg.DNS.Rules, rules)

	if !s.saveAndApplyRules(w, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"decision": decision,
		"source":   source,
		"index":    *req.Index,
	})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request, kind string) {
	idx, errMsg := parseIndexQuery(r)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	acc := s.accessor(kind)

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rules := acc.getAll(&cfg.DNS.Rules)
	if idx < 0 || idx >= len(rules) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	}
	removed := rules[idx]
	next := append(rules[:idx:idx], rules[idx+1:]...)
	acc.setAll(&cfg.DNS.Rules, next)

	if !s.saveAndApplyRules(w, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"decision": removed.Decision,
		"source":   removed.Source,
		"index":    idx,
	})
}

func (s *Server) handleMoveRule(w http.ResponseWriter, r *http.Request, kind string) {
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rule: " + err.Error()})
		return
	}
	from, errMsg := parseIndexQuery(r)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	if req.Index == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing target index"})
		return
	}
	target := *req.Index
	acc := s.accessor(kind)

	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rules := acc.getAll(&cfg.DNS.Rules)
	if from < 0 || from >= len(rules) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	}
	if target < 0 || target >= len(rules) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("target index %d out of range", target)})
		return
	}
	moved := rules[from]
	rest := append(rules[:from:from], rules[from+1:]...)
	insertAt := target
	if insertAt > len(rest) {
		insertAt = len(rest)
	}
	next := append([]ruleSource{}, rest[:insertAt]...)
	next = append(next, moved)
	next = append(next, rest[insertAt:]...)
	acc.setAll(&cfg.DNS.Rules, next)

	if !s.saveAndApplyRules(w, cfg) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"decision": moved.Decision,
		"source":   moved.Source,
		"index":    target,
	})
}

func (s *Server) handleRefreshRule(w http.ResponseWriter, r *http.Request, kind string) {
	all := r.URL.Query().Get("all") == "true"
	cfg, err := config.Snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	acc := s.accessor(kind)
	rules := acc.getAll(&cfg.DNS.Rules)

	var targets []ruleSource
	if all {
		for _, rule := range rules {
			if isRemoteSource(rule.Source) {
				targets = append(targets, rule)
			}
		}
	} else {
		idx, errMsg := parseIndexQuery(r)
		if errMsg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
			return
		}
		if idx < 0 || idx >= len(rules) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
			return
		}
		if !isRemoteSource(rules[idx].Source) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rule has no remote source to refresh"})
			return
		}
		targets = []ruleSource{rules[idx]}
	}

	if s.dns == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dns server unavailable"})
		return
	}
	refreshed := 0
	var refreshErr error
	for _, rule := range targets {
		if err := s.dns.RefreshSource(rule.Source); err != nil {
			refreshErr = err
			break
		}
		refreshed++
	}
	if refreshErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": refreshErr.Error(), "refreshed": fmt.Sprintf("%d", refreshed)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"refreshed": refreshed,
	})
}

func parseIndexQuery(r *http.Request) (int, string) {
	value := strings.TrimSpace(r.URL.Query().Get("index"))
	if value == "" {
		return 0, "missing index"
	}
	var idx int
	if _, err := fmt.Sscanf(value, "%d", &idx); err != nil {
		return 0, "invalid index: " + err.Error()
	}
	return idx, ""
}

func (s *Server) saveAndApplyRules(w http.ResponseWriter, cfg *config.Config) bool {
	if err := config.Replace(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	if s.dns != nil {
		if err := s.dns.UpdateRules(cfg.DNS.Rules); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return false
		}
	}
	return true
}

func (s *Server) listDomainRules(rules []config.DomainRule) []ruleEntry {
	stats := s.ruleStats()
	out := make([]ruleEntry, 0, len(rules))
	for i, rule := range rules {
		out = append(out, s.buildRuleEntry(i, rule.Decision, rule.Source, stats, "domains"))
	}
	return out
}

func (s *Server) listCIDRRules(rules []config.CIDRRule) []ruleEntry {
	stats := s.ruleStats()
	out := make([]ruleEntry, 0, len(rules)+1)
	for i, rule := range rules {
		out = append(out, s.buildRuleEntry(i, rule.Decision, rule.Source, stats, "cidrs"))
	}
	out = append(out, ruleEntry{
		Index:    len(rules),
		Decision: config.DecisionRelay,
		Source:   "DEFAULT",
		Type:     "default",
		Default:  true,
	})
	return out
}

func (s *Server) ruleStats() map[string][]pdns.RuleListEntry {
	if s.dns == nil {
		return nil
	}
	return s.dns.RuleListSnapshot()
}

func (s *Server) buildRuleEntry(index int, decision, source string, stats map[string][]pdns.RuleListEntry, suffix string) ruleEntry {
	entry := ruleEntry{
		Index:    index,
		Decision: decision,
		Source:   source,
	}
	bucket := decision + "-" + suffix
	if stats != nil {
		for _, statEntry := range stats[bucket] {
			if statEntry.Value == source {
				entry.Type = statEntry.Type
				entry.Count = statEntry.Count
				entry.Hits = statEntry.Hits
				entry.LastUpdated = statEntry.LastUpdated
				break
			}
		}
	}
	if isRemoteSource(source) && s.dns != nil && s.dns.Assets() != nil {
		interval := s.dns.Assets().RefreshInterval()
		if interval > 0 && !entry.LastUpdated.IsZero() {
			entry.NextUpdate = entry.LastUpdated.Add(interval)
		}
	}
	if entry.Type == "" {
		if isRemoteSource(source) {
			entry.Type = "asset"
		} else if isLocalPath(source) {
			entry.Type = "file"
		} else {
			entry.Type = "inline"
		}
	}
	return entry
}

func isRemoteSource(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}

func isLocalPath(source string) bool {
	return strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

func validateDomainRule(decision, source string) (string, error) {
	if !validDomainDecision(decision) {
		return "", fmt.Errorf("unsupported decision %q", decision)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("missing source")
	}
	if !isRemoteSource(source) && !isLocalPath(source) {
		source = dnsrule.Normalize(source)
		kind, value := dnsrule.Split(source)
		if kind == dnsrule.KindQType {
			if _, err := dnsrule.ParseQType(value); err != nil {
				return "", err
			}
		}
	}
	return source, nil
}

func validateCIDRRule(decision, source string) (string, error) {
	if !validCIDRDecision(decision) {
		return "", fmt.Errorf("unsupported decision %q", decision)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("missing source")
	}
	if !isRemoteSource(source) && !isLocalPath(source) {
		prefix, err := netip.ParsePrefix(source)
		if err != nil {
			return "", fmt.Errorf("invalid cidr %q: %w", source, err)
		}
		source = prefix.Masked().String()
	}
	return source, nil
}

func validDomainDecision(decision string) bool {
	return decision == config.DecisionReject || decision == config.DecisionRelay || decision == config.DecisionDirect
}

func validCIDRDecision(decision string) bool {
	return decision == config.DecisionDirect || decision == config.DecisionReject
}
