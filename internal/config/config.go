// Package config holds Punch's runtime configuration and persistence.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/dnsrule"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// Config is the top-level runtime configuration.
type Config struct {
	LogLevel             string   `json:"log_level"`
	LogFile              string   `json:"log_file,omitempty"`
	AssetRefreshInterval int      `json:"asset_refresh_interval"`
	DNS                  DNS      `json:"dns"`
	TUN                  TUN      `json:"tun"`
	Relay                Relay    `json:"relay"`
	API                  API      `json:"api"`
	Sessions             Sessions `json:"sessions"`
}

type DNS struct {
	Listen        string      `json:"listen"`
	Upstream      []Upstream  `json:"upstream"`
	CacheSize     int         `json:"cache_size"`
	FakeIPRange   string      `json:"fakeip_range"`
	FakeIPv6Range string      `json:"fakeipv6_range,omitempty"`
	FakeIPTTL     string      `json:"fakeip_ttl"`
	Options       *DNSOptions `json:"options,omitempty"`
	Rules         DNSRules    `json:"rules"`
}

type Upstream struct {
	URL       string   `json:"url"`
	Bootstrap string   `json:"bootstrap,omitempty"`
	Domains   []string `json:"domains,omitempty"`
}

type DNSOptions struct {
	CacheSize     int    `json:"cache_size"`
	FakeIPRange   string `json:"fake_ip_range"`
	FakeIPv6Range string `json:"fake_ipv6_range,omitempty"`
	FakeIPTTL     string `json:"fakeip_ttl,omitempty"`
}

type DNSRules struct {
	Domains []DomainRule `json:"domains,omitempty"`
	CIDRs   []CIDRRule   `json:"cidrs,omitempty"`
}

// DomainRule is one ordered entry in the DNS rule list. Decision is one of
// reject, relay, direct. Source can be a URL/file path, an inline domain rule
// like "domain:example.com", "keyword:stun", "full:x.com", "regexp:...", or a
// qtype rule like "qtype:65".
type DomainRule struct {
	Decision string `json:"decision"`
	Source   string `json:"source"`
}

// CIDRRule is one ordered entry in the CIDR rule list. Decision is direct or
// reject; the implicit default for IPs not matched by any rule is relay.
// Source can be a URL/file path or an inline CIDR like "10.0.0.0/8".
type CIDRRule struct {
	Decision string `json:"decision"`
	Source   string `json:"source"`
}

// Decisions for DomainRule and CIDRRule.
const (
	DecisionReject = "reject"
	DecisionRelay  = "relay"
	DecisionDirect = "direct"
)

type TUN struct {
	Device string   `json:"device"`
	Routes []string `json:"routes,omitempty"`
}

type Relay struct {
	Select       string       `json:"select"`
	AutoStrategy AutoStrategy `json:"auto_strategy"`
	Groups       []RelayGroup `json:"groups,omitempty"`
}

type AutoStrategy struct {
	Mode             string `json:"mode,omitempty"`
	URL              string `json:"url"`
	Interval         int    `json:"interval"`
	Tolerance        int    `json:"tolerance"`
	CheckConcurrency int    `json:"check_concurrency,omitempty"`
}

type RelayGroup struct {
	Type                string           `json:"type"`
	Name                string           `json:"name"`
	URL                 string           `json:"url,omitempty"`
	RefreshDuration     int              `json:"refresh_duration,omitempty"`
	Keep                string           `json:"keep,omitempty"`
	Remove              string           `json:"remove,omitempty"`
	Select              string           `json:"select,omitempty"`
	RelayDomainResolver []Upstream       `json:"relay_domain_resolver,omitempty"`
	Proxies             []map[string]any `json:"proxies,omitempty"`
}

type API struct {
	Listen string `json:"listen"`
	Secret string `json:"secret,omitempty"`
}

type Sessions struct {
	HistoryLimit int `json:"history_limit"`
}

// RelaySelections is the persisted manual selection state for relay groups.
type RelaySelections struct {
	ActiveGroup string
	GroupRelay  map[string]string
}

const (
	ruleKindDomain = "domain"
	ruleKindCIDR   = "cidr"
)

// ErrNotInitialized indicates Init has not loaded the singleton config yet.
var ErrNotInitialized = errors.New("config is not initialized")

var singleton struct {
	mu    sync.RWMutex
	store *Store
	cfg   *Config
}

// Kinds is the set of rule kinds.
const (
	KindDomain = "domain"
	KindCIDR   = "cidr"
)

var scalarKeys = []string{
	"system.log_level",
	"system.log_file",
	"system.asset_refresh_interval",
	"dns.listen",
	"dns.cache_size",
	"dns.fakeip_range",
	"dns.fakeipv6_range",
	"dns.fakeip_ttl",
	"tun.device",
	"relay.select",
	"relay.auto_strategy.mode",
	"relay.auto_strategy.url",
	"relay.auto_strategy.interval",
	"relay.auto_strategy.tolerance",
	"relay.auto_strategy.check_concurrency",
	"api.listen",
	"api.secret",
	"sessions.history_limit",
}

// Load returns the configuration stored in s. If no configuration has been
// written yet, it seeds the store with Default() and returns that.
func Load(s *Store) (*Config, error) {
	cfg, err := loadTables(s)
	if errors.Is(err, ErrNotFound) {
		cfg = Default()
		if err := Save(s, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save validates and persists cfg to s.
func Save(s *Store, cfg *Config) error {
	applyDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return err
	}
	return saveTables(s, cfg)
}

// Init loads the process-wide configuration from s, seeding defaults on first
// launch. After Init succeeds, callers should use Get, Set, or Update.
func Init(s *Store) error {
	cfg, err := Load(s)
	if err != nil {
		return err
	}
	singleton.mu.Lock()
	defer singleton.mu.Unlock()
	singleton.store = s
	singleton.cfg = cloneConfig(cfg)
	return nil
}

// Snapshot returns a full copy of the in-memory configuration for components
// that need a structured section during initialization or reconfiguration.
func Snapshot() (*Config, error) {
	singleton.mu.RLock()
	defer singleton.mu.RUnlock()
	if singleton.cfg == nil {
		return nil, ErrNotInitialized
	}
	return cloneConfig(singleton.cfg), nil
}

// Replace validates cfg, persists it to SQLite, then replaces the in-memory
// snapshot.
func Replace(cfg *Config) error {
	singleton.mu.Lock()
	defer singleton.mu.Unlock()
	if singleton.store == nil || singleton.cfg == nil {
		return ErrNotInitialized
	}
	next := cloneConfig(cfg)
	if err := Save(singleton.store, next); err != nil {
		return err
	}
	singleton.cfg = cloneConfig(next)
	return nil
}

// Get returns the string value for a scalar configuration key.
func Get(key string) (string, error) {
	singleton.mu.RLock()
	defer singleton.mu.RUnlock()
	if singleton.cfg == nil {
		return "", ErrNotInitialized
	}
	return getValue(singleton.cfg, key)
}

// Set updates one scalar configuration key, persists the new configuration,
// and publishes it in memory.
func Set(key, value string) error {
	_, err := Update(func(cfg *Config) error {
		return setValue(cfg, key, value)
	})
	return err
}

// Keys returns the scalar keys accepted by Get and Set.
func Keys() []string {
	return append([]string(nil), scalarKeys...)
}

// Update applies fn to a mutable snapshot, persists it, then publishes it in
// memory. The returned config is the committed snapshot.
func Update(fn func(*Config) error) (*Config, error) {
	singleton.mu.Lock()
	defer singleton.mu.Unlock()
	if singleton.store == nil || singleton.cfg == nil {
		return nil, ErrNotInitialized
	}
	next := cloneConfig(singleton.cfg)
	if err := fn(next); err != nil {
		return nil, err
	}
	if err := Save(singleton.store, next); err != nil {
		return nil, err
	}
	singleton.cfg = cloneConfig(next)
	return cloneConfig(singleton.cfg), nil
}

func getValue(cfg *Config, key string) (string, error) {
	switch key {
	case "system.log_level":
		return cfg.LogLevel, nil
	case "system.log_file":
		return cfg.LogFile, nil
	case "system.asset_refresh_interval":
		return strconv.Itoa(cfg.AssetRefreshInterval), nil
	case "dns.listen":
		return cfg.DNS.Listen, nil
	case "dns.cache_size":
		return strconv.Itoa(cfg.DNS.CacheSize), nil
	case "dns.fakeip_range":
		return cfg.DNS.FakeIPRange, nil
	case "dns.fakeipv6_range":
		return cfg.DNS.FakeIPv6Range, nil
	case "dns.fakeip_ttl":
		return cfg.DNS.FakeIPTTL, nil
	case "tun.device":
		return cfg.TUN.Device, nil
	case "relay.select":
		return cfg.Relay.Select, nil
	case "relay.auto_strategy.mode":
		return cfg.Relay.AutoStrategy.Mode, nil
	case "relay.auto_strategy.url":
		return cfg.Relay.AutoStrategy.URL, nil
	case "relay.auto_strategy.interval":
		return strconv.Itoa(cfg.Relay.AutoStrategy.Interval), nil
	case "relay.auto_strategy.tolerance":
		return strconv.Itoa(cfg.Relay.AutoStrategy.Tolerance), nil
	case "relay.auto_strategy.check_concurrency":
		return strconv.Itoa(cfg.Relay.AutoStrategy.CheckConcurrency), nil
	case "api.listen":
		return cfg.API.Listen, nil
	case "api.secret":
		return cfg.API.Secret, nil
	case "sessions.history_limit":
		return strconv.Itoa(cfg.Sessions.HistoryLimit), nil
	default:
		return "", ErrNotFound
	}
}

func setValue(cfg *Config, key, value string) error {
	switch key {
	case "system.log_level":
		cfg.LogLevel = value
	case "system.log_file":
		cfg.LogFile = value
	case "system.asset_refresh_interval":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.AssetRefreshInterval = parsed
	case "dns.listen":
		cfg.DNS.Listen = value
	case "dns.cache_size":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.DNS.CacheSize = parsed
	case "dns.fakeip_range":
		cfg.DNS.FakeIPRange = value
	case "dns.fakeipv6_range":
		cfg.DNS.FakeIPv6Range = value
	case "dns.fakeip_ttl":
		cfg.DNS.FakeIPTTL = value
	case "tun.device":
		cfg.TUN.Device = value
	case "relay.select":
		cfg.Relay.Select = value
	case "relay.auto_strategy.mode":
		cfg.Relay.AutoStrategy.Mode = value
	case "relay.auto_strategy.url":
		cfg.Relay.AutoStrategy.URL = value
	case "relay.auto_strategy.interval":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.Relay.AutoStrategy.Interval = parsed
	case "relay.auto_strategy.tolerance":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.Relay.AutoStrategy.Tolerance = parsed
	case "relay.auto_strategy.check_concurrency":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.Relay.AutoStrategy.CheckConcurrency = parsed
	case "api.listen":
		cfg.API.Listen = value
	case "api.secret":
		cfg.API.Secret = value
	case "sessions.history_limit":
		parsed, err := parsePositiveInt(key, value)
		if err != nil {
			return err
		}
		cfg.Sessions.HistoryLimit = parsed
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func parsePositiveInt(key, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func cloneConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.TUN.Routes = append([]string(nil), cfg.TUN.Routes...)
	out.DNS.Upstream = cloneUpstreams(cfg.DNS.Upstream)
	if cfg.DNS.Options != nil {
		options := *cfg.DNS.Options
		out.DNS.Options = &options
	}
	out.DNS.Rules.Domains = append([]DomainRule(nil), cfg.DNS.Rules.Domains...)
	out.DNS.Rules.CIDRs = append([]CIDRRule(nil), cfg.DNS.Rules.CIDRs...)
	out.Relay.Groups = cloneRelayGroups(cfg.Relay.Groups)
	return &out
}

func cloneUpstreams(upstreams []Upstream) []Upstream {
	out := make([]Upstream, len(upstreams))
	for i, upstream := range upstreams {
		out[i] = upstream
		out[i].Domains = append([]string(nil), upstream.Domains...)
	}
	return out
}

func cloneRelayGroups(groups []RelayGroup) []RelayGroup {
	out := make([]RelayGroup, len(groups))
	for i, group := range groups {
		out[i] = group
		out[i].RelayDomainResolver = cloneUpstreams(group.RelayDomainResolver)
		out[i].Proxies = make([]map[string]any, len(group.Proxies))
		for j, proxy := range group.Proxies {
			out[i].Proxies[j] = cloneStringAnyMap(proxy)
		}
	}
	return out
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return v
	}
}

func loadTables(s *Store) (*Config, error) {
	var base configBaseModel
	err := s.db.First(&base, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load config base: %w", err)
	}
	cfg := &Config{
		LogLevel:             base.LogLevel,
		LogFile:              base.LogFile,
		AssetRefreshInterval: base.AssetRefreshInterval,
		DNS: DNS{
			Listen:        base.DNSListen,
			CacheSize:     base.DNSCacheSize,
			FakeIPRange:   base.DNSFakeIPRange,
			FakeIPv6Range: base.DNSFakeIPv6Range,
			FakeIPTTL:     base.DNSFakeIPTTL,
		},
		TUN: TUN{
			Device: base.TUNDevice,
		},
		Relay: Relay{
			Select: base.RelaySelect,
			AutoStrategy: AutoStrategy{
				Mode:             base.RelayAutoMode,
				URL:              base.RelayAutoURL,
				Interval:         base.RelayAutoInterval,
				Tolerance:        base.RelayAutoTolerance,
				CheckConcurrency: base.RelayCheckConcurrency,
			},
		},
		API: API{
			Listen: base.APIListen,
			Secret: base.APISecret,
		},
		Sessions: Sessions{
			HistoryLimit: base.SessionsHistoryLimit,
		},
	}

	routes, err := loadTUNRoutes(s)
	if err != nil {
		return nil, err
	}
	cfg.TUN.Routes = routes

	upstreams, err := loadDNSUpstreams(s)
	if err != nil {
		return nil, err
	}
	cfg.DNS.Upstream = upstreams

	rules, err := loadDNSRules(s)
	if err != nil {
		return nil, err
	}
	cfg.DNS.Rules = rules

	groups, err := loadRelayGroups(s)
	if err != nil {
		return nil, err
	}
	cfg.Relay.Groups = groups

	return cfg, nil
}

func loadTUNRoutes(s *Store) ([]string, error) {
	var rows []tunRouteModel
	if err := s.db.Order("position").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load tun routes: %w", err)
	}
	routes := make([]string, 0, len(rows))
	for _, row := range rows {
		routes = append(routes, row.Route)
	}
	return routes, nil
}

func loadDNSUpstreams(s *Store) ([]Upstream, error) {
	var rows []dnsUpstreamModel
	if err := s.db.Order("position").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load dns upstreams: %w", err)
	}

	upstreams := make([]Upstream, 0, len(rows))
	for _, row := range rows {
		upstream := Upstream{URL: row.URL, Bootstrap: row.Bootstrap}
		var domainRows []dnsUpstreamDomainModel
		if err := s.db.Where("upstream_position = ?", row.Position).Order("position").Find(&domainRows).Error; err != nil {
			return nil, fmt.Errorf("load dns upstream domains: %w", err)
		}
		for _, domainRow := range domainRows {
			upstream.Domains = append(upstream.Domains, domainRow.Domain)
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, nil
}

func loadDNSRules(s *Store) (DNSRules, error) {
	var rules DNSRules
	var rows []dnsRuleSourceModel
	if err := s.db.Order("kind, position").Find(&rows).Error; err != nil {
		return rules, fmt.Errorf("load dns rules: %w", err)
	}

	for _, row := range rows {
		switch row.Kind {
		case ruleKindDomain:
			rules.Domains = append(rules.Domains, DomainRule{Decision: row.Decision, Source: row.Source})
		case ruleKindCIDR:
			rules.CIDRs = append(rules.CIDRs, CIDRRule{Decision: row.Decision, Source: row.Source})
		default:
			return rules, fmt.Errorf("unknown dns rule kind %q", row.Kind)
		}
	}

	return rules, nil
}

func loadRelayGroups(s *Store) ([]RelayGroup, error) {
	var rows []relayGroupModel
	if err := s.db.Order("position").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load relay groups: %w", err)
	}

	groups := make([]RelayGroup, 0, len(rows))
	for _, row := range rows {
		group := RelayGroup{
			Type:            row.Type,
			Name:            row.Name,
			URL:             row.URL,
			RefreshDuration: row.RefreshDuration,
			Keep:            row.Keep,
			Remove:          row.Remove,
			Select:          row.Select,
		}
		resolvers, err := loadRelayGroupResolvers(s, row.Position)
		if err != nil {
			return nil, err
		}
		group.RelayDomainResolver = resolvers
		proxies, err := loadRelayGroupProxies(s, row.Position)
		if err != nil {
			return nil, err
		}
		group.Proxies = proxies
		groups = append(groups, group)
	}
	return groups, nil
}

func loadRelayGroupResolvers(s *Store, groupPosition int) ([]Upstream, error) {
	var rows []relayGroupResolverModel
	if err := s.db.Where("group_position = ?", groupPosition).Order("position").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load relay group resolvers: %w", err)
	}
	resolvers := make([]Upstream, 0, len(rows))
	for _, row := range rows {
		resolver := Upstream{URL: row.URL, Bootstrap: row.Bootstrap}
		var domainRows []relayGroupResolverDomainModel
		err := s.db.Where("group_position = ? AND resolver_position = ?", row.GroupPosition, row.Position).
			Order("position").Find(&domainRows).Error
		if err != nil {
			return nil, fmt.Errorf("load relay group resolver domains: %w", err)
		}
		for _, domainRow := range domainRows {
			resolver.Domains = append(resolver.Domains, domainRow.Domain)
		}
		resolvers = append(resolvers, resolver)
	}
	return resolvers, nil
}

func loadRelayGroupProxies(s *Store, groupPosition int) ([]map[string]any, error) {
	var rows []relayGroupProxyModel
	if err := s.db.Where("group_position = ?", groupPosition).Order("position").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load relay group proxies: %w", err)
	}
	proxies := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var proxy map[string]any
		if err := yaml.Unmarshal(row.ProxyYAML, &proxy); err != nil {
			return nil, fmt.Errorf("parse relay group proxy: %w", err)
		}
		proxies = append(proxies, proxy)
	}
	return proxies, nil
}

func saveTables(s *Store, cfg *Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		base := configBaseModel{
			ID:                    1,
			LogLevel:              cfg.LogLevel,
			LogFile:               cfg.LogFile,
			AssetRefreshInterval:  cfg.AssetRefreshInterval,
			DNSListen:             cfg.DNS.Listen,
			DNSCacheSize:          cfg.DNS.CacheSize,
			DNSFakeIPRange:        cfg.DNS.FakeIPRange,
			DNSFakeIPv6Range:      cfg.DNS.FakeIPv6Range,
			DNSFakeIPTTL:          cfg.DNS.FakeIPTTL,
			TUNDevice:             cfg.TUN.Device,
			RelaySelect:           cfg.Relay.Select,
			RelayAutoMode:         cfg.Relay.AutoStrategy.Mode,
			RelayAutoURL:          cfg.Relay.AutoStrategy.URL,
			RelayAutoInterval:     cfg.Relay.AutoStrategy.Interval,
			RelayAutoTolerance:    cfg.Relay.AutoStrategy.Tolerance,
			RelayCheckConcurrency: cfg.Relay.AutoStrategy.CheckConcurrency,
			APIListen:             cfg.API.Listen,
			APISecret:             cfg.API.Secret,
			SessionsHistoryLimit:  cfg.Sessions.HistoryLimit,
		}
		if err := tx.Save(&base).Error; err != nil {
			return fmt.Errorf("save config base: %w", err)
		}
		if err := replaceTUNRoutes(tx, cfg.TUN.Routes); err != nil {
			return err
		}
		if err := replaceDNSUpstreams(tx, cfg.DNS.Upstream); err != nil {
			return err
		}
		if err := replaceDNSRules(tx, cfg.DNS.Rules); err != nil {
			return err
		}
		if err := replaceRelayGroups(tx, cfg.Relay.Groups); err != nil {
			return err
		}
		return nil
	})
}

func replaceTUNRoutes(tx *gorm.DB, routes []string) error {
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tunRouteModel{}).Error; err != nil {
		return fmt.Errorf("clear tun routes: %w", err)
	}
	rows := make([]tunRouteModel, 0, len(routes))
	for i, route := range routes {
		rows = append(rows, tunRouteModel{Position: i, Route: route})
	}
	if len(rows) == 0 {
		return nil
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("save tun routes: %w", err)
	}
	return nil
}

func replaceDNSUpstreams(tx *gorm.DB, upstreams []Upstream) error {
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&dnsUpstreamDomainModel{}).Error; err != nil {
		return fmt.Errorf("clear dns upstream domains: %w", err)
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&dnsUpstreamModel{}).Error; err != nil {
		return fmt.Errorf("clear dns upstreams: %w", err)
	}
	rows := make([]dnsUpstreamModel, 0, len(upstreams))
	var domainRows []dnsUpstreamDomainModel
	for i, upstream := range upstreams {
		rows = append(rows, dnsUpstreamModel{
			Position:  i,
			URL:       upstream.URL,
			Bootstrap: upstream.Bootstrap,
		})
		for j, domain := range upstream.Domains {
			domainRows = append(domainRows, dnsUpstreamDomainModel{
				UpstreamPosition: i,
				Position:         j,
				Domain:           domain,
			})
		}
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("save dns upstreams: %w", err)
		}
	}
	if len(domainRows) > 0 {
		if err := tx.Create(&domainRows).Error; err != nil {
			return fmt.Errorf("save dns upstream domains: %w", err)
		}
	}
	return nil
}

func replaceDNSRules(tx *gorm.DB, rules DNSRules) error {
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&dnsRuleSourceModel{}).Error; err != nil {
		return fmt.Errorf("clear dns rules: %w", err)
	}
	var rows []dnsRuleSourceModel
	for i, rule := range rules.Domains {
		rows = append(rows, dnsRuleSourceModel{Kind: ruleKindDomain, Decision: rule.Decision, Position: i, Source: rule.Source})
	}
	for i, rule := range rules.CIDRs {
		rows = append(rows, dnsRuleSourceModel{Kind: ruleKindCIDR, Decision: rule.Decision, Position: i, Source: rule.Source})
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("save dns rules: %w", err)
		}
	}

	return nil
}

func replaceRelayGroups(tx *gorm.DB, groups []RelayGroup) error {
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&relayGroupResolverDomainModel{}).Error; err != nil {
		return fmt.Errorf("clear relay group resolver domains: %w", err)
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&relayGroupResolverModel{}).Error; err != nil {
		return fmt.Errorf("clear relay group resolvers: %w", err)
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&relayGroupProxyModel{}).Error; err != nil {
		return fmt.Errorf("clear relay group proxies: %w", err)
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&relayGroupModel{}).Error; err != nil {
		return fmt.Errorf("clear relay groups: %w", err)
	}
	rows := make([]relayGroupModel, 0, len(groups))
	var resolverRows []relayGroupResolverModel
	var resolverDomainRows []relayGroupResolverDomainModel
	var proxyRows []relayGroupProxyModel
	for i, group := range groups {
		rows = append(rows, relayGroupModel{
			Position:        i,
			Type:            group.Type,
			Name:            group.Name,
			URL:             group.URL,
			RefreshDuration: group.RefreshDuration,
			Keep:            group.Keep,
			Remove:          group.Remove,
			Select:          group.Select,
		})
		for j, resolver := range group.RelayDomainResolver {
			resolverRows = append(resolverRows, relayGroupResolverModel{
				GroupPosition: i,
				Position:      j,
				URL:           resolver.URL,
				Bootstrap:     resolver.Bootstrap,
			})
			for k, domain := range resolver.Domains {
				resolverDomainRows = append(resolverDomainRows, relayGroupResolverDomainModel{
					GroupPosition:    i,
					ResolverPosition: j,
					Position:         k,
					Domain:           domain,
				})
			}
		}
		for j, proxy := range group.Proxies {
			raw, err := yaml.Marshal(proxy)
			if err != nil {
				return fmt.Errorf("marshal relay group %d proxy %d: %w", i, j, err)
			}
			proxyRows = append(proxyRows, relayGroupProxyModel{
				GroupPosition: i,
				Position:      j,
				ProxyYAML:     raw,
			})
		}
	}
	if len(rows) > 0 {
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("save relay groups: %w", err)
		}
	}
	if len(resolverRows) > 0 {
		if err := tx.Create(&resolverRows).Error; err != nil {
			return fmt.Errorf("save relay group resolvers: %w", err)
		}
	}
	if len(resolverDomainRows) > 0 {
		if err := tx.Create(&resolverDomainRows).Error; err != nil {
			return fmt.Errorf("save relay group resolver domains: %w", err)
		}
	}
	if len(proxyRows) > 0 {
		if err := tx.Create(&proxyRows).Error; err != nil {
			return fmt.Errorf("save relay group proxies: %w", err)
		}
	}
	return nil
}

// LoadRelaySelections returns persisted relay selection state.
func LoadRelaySelections(s *Store) (RelaySelections, error) {
	var rows []relaySelectionModel
	if err := s.db.Order("position").Find(&rows).Error; err != nil {
		return RelaySelections{}, fmt.Errorf("load relay selections: %w", err)
	}
	if len(rows) == 0 {
		return RelaySelections{}, ErrNotFound
	}
	state := RelaySelections{GroupRelay: make(map[string]string, len(rows))}
	for _, row := range rows {
		state.GroupRelay[row.GroupName] = row.RelayName
		if row.Active {
			state.ActiveGroup = row.GroupName
		}
	}
	return state, nil
}

// SaveRelaySelections persists relay selection state.
func SaveRelaySelections(s *Store, state RelaySelections) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&relaySelectionModel{}).Error; err != nil {
			return fmt.Errorf("clear relay selections: %w", err)
		}
		rows := make([]relaySelectionModel, 0, len(state.GroupRelay))
		position := 0
		for groupName, relayName := range state.GroupRelay {
			rows = append(rows, relaySelectionModel{
				GroupName: groupName,
				RelayName: relayName,
				Active:    groupName == state.ActiveGroup,
				Position:  position,
			})
			position++
		}
		if state.ActiveGroup != "" {
			if _, ok := state.GroupRelay[state.ActiveGroup]; !ok {
				rows = append(rows, relaySelectionModel{
					GroupName: state.ActiveGroup,
					Active:    true,
					Position:  position,
				})
			}
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("save relay selections: %w", err)
		}
		return nil
	})
}

// Default returns the built-in first-run configuration. It ships with public
// DNS upstreams and public rule lists so the DNS and TUN layers are useful
// out of the box. The Relay section is empty until configured in the store.
func Default() *Config {
	cfg := &Config{
		LogLevel:             "info",
		AssetRefreshInterval: 3600,
		DNS: DNS{
			Listen: "0.0.0.0:28853",
			Upstream: []Upstream{
				{URL: "https://doh.pub/dns-query", Bootstrap: "119.29.29.29"},
				{URL: "https://dns.alidns.com/dns-query", Bootstrap: "223.5.5.5"},
			},
			CacheSize:     100000,
			FakeIPRange:   "198.18.0.0/15",
			FakeIPv6Range: "fdfe:dcba:9876::/64",
			FakeIPTTL:     "1h",
			Rules: DNSRules{
				Domains: []DomainRule{
					{Decision: DecisionReject, Source: "qtype:ptr"},
					{Decision: DecisionReject, Source: "qtype:svcb"},
					{Decision: DecisionReject, Source: "qtype:https"},
					{Decision: DecisionReject, Source: "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/reject-list.txt"},
					{Decision: DecisionRelay, Source: "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/gfw.txt"},
					{Decision: DecisionDirect, Source: "https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/direct-list.txt"},
					{Decision: DecisionDirect, Source: "https://raw.githubusercontent.com/v2fly/domain-list-community/master/data/private"},
					{Decision: DecisionDirect, Source: "keyword:stun"},
					{Decision: DecisionDirect, Source: "domain:in-addr.arpa"},
				},
				CIDRs: []CIDRRule{
					{Decision: DecisionDirect, Source: "https://ispip.clang.cn/all_cn.txt"},
					{Decision: DecisionDirect, Source: "https://ispip.clang.cn/all_cn_ipv6.txt"},
				},
			},
		},
		TUN: TUN{
			Device: "punch0",
		},
		Relay: Relay{
			Select: "auto",
			AutoStrategy: AutoStrategy{
				URL:              "http://www.gstatic.com/generate_204",
				Interval:         3600,
				Tolerance:        50,
				CheckConcurrency: 10,
			},
		},
		API: API{
			Listen: "127.0.0.1:28854",
		},
		Sessions: Sessions{
			HistoryLimit: 1000,
		},
	}
	return cfg
}

func applyDefaults(cfg *Config) {
	applyLegacyDNSOptions(cfg)
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.DNS.Listen == "" {
		cfg.DNS.Listen = "0.0.0.0:53"
	}
	if cfg.DNS.CacheSize == 0 {
		cfg.DNS.CacheSize = 100000
	}
	if cfg.DNS.FakeIPRange == "" {
		cfg.DNS.FakeIPRange = "198.18.0.0/15"
	}
	if cfg.DNS.FakeIPv6Range == "" {
		cfg.DNS.FakeIPv6Range = "fdfe:dcba:9876::/64"
	}
	if cfg.DNS.FakeIPTTL == "" {
		cfg.DNS.FakeIPTTL = "1h"
	}
	if cfg.AssetRefreshInterval == 0 {
		cfg.AssetRefreshInterval = 3600
	}
	if cfg.TUN.Device == "" {
		cfg.TUN.Device = "punch0"
	}
	if cfg.Relay.Select == "" {
		cfg.Relay.Select = "auto"
	}
	if cfg.Relay.AutoStrategy.URL == "" {
		cfg.Relay.AutoStrategy.URL = "http://www.gstatic.com/generate_204"
	}
	if cfg.Relay.AutoStrategy.Interval == 0 {
		cfg.Relay.AutoStrategy.Interval = 300
	}
	if cfg.Relay.AutoStrategy.Tolerance == 0 {
		cfg.Relay.AutoStrategy.Tolerance = 50
	}
	if cfg.Relay.AutoStrategy.CheckConcurrency == 0 {
		cfg.Relay.AutoStrategy.CheckConcurrency = 10
	}
	if cfg.API.Listen == "" {
		cfg.API.Listen = "127.0.0.1:28854"
	}
	if cfg.Sessions.HistoryLimit == 0 {
		cfg.Sessions.HistoryLimit = 1000
	}
	for i := range cfg.Relay.Groups {
		if cfg.Relay.Groups[i].Type == "remote" && cfg.Relay.Groups[i].RefreshDuration == 0 {
			cfg.Relay.Groups[i].RefreshDuration = cfg.AssetRefreshInterval
		}
	}
}

func applyLegacyDNSOptions(cfg *Config) {
	if cfg.DNS.Options == nil {
		return
	}
	if cfg.DNS.CacheSize == 0 {
		cfg.DNS.CacheSize = cfg.DNS.Options.CacheSize
	}
	if cfg.DNS.FakeIPRange == "" {
		cfg.DNS.FakeIPRange = cfg.DNS.Options.FakeIPRange
	}
	if cfg.DNS.FakeIPv6Range == "" {
		cfg.DNS.FakeIPv6Range = cfg.DNS.Options.FakeIPv6Range
	}
	if cfg.DNS.FakeIPTTL == "" {
		cfg.DNS.FakeIPTTL = cfg.DNS.Options.FakeIPTTL
	}
	cfg.DNS.Options = nil
}

func validateConfig(cfg *Config) error {
	if err := validateFakeIPRange("dns.fakeip_range", cfg.DNS.FakeIPRange, false); err != nil {
		return err
	}
	if err := validateFakeIPRange("dns.fakeipv6_range", cfg.DNS.FakeIPv6Range, true); err != nil {
		return err
	}
	if cfg.DNS.FakeIPTTL != "" {
		ttl, err := time.ParseDuration(cfg.DNS.FakeIPTTL)
		if err != nil {
			return fmt.Errorf("dns.fakeip_ttl: %w", err)
		}
		if ttl <= 0 {
			return fmt.Errorf("dns.fakeip_ttl must be positive")
		}
	}
	for i, upstream := range cfg.DNS.Upstream {
		if err := validateDNSUpstream(upstream); err != nil {
			return fmt.Errorf("dns.upstream[%d]: %w", i, err)
		}
	}
	for i, group := range cfg.Relay.Groups {
		for j, upstream := range group.RelayDomainResolver {
			if err := validateDNSUpstream(upstream); err != nil {
				return fmt.Errorf("relay.groups[%d].relay_domain_resolver[%d]: %w", i, j, err)
			}
		}
	}
	return nil
}

func validateFakeIPRange(key, cidr string, wantIPv6 bool) error {
	if cidr == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if wantIPv6 && !prefix.Addr().Is6() {
		return fmt.Errorf("%s must be an IPv6 CIDR", key)
	}
	if !wantIPv6 && !prefix.Addr().Is4() {
		return fmt.Errorf("%s must be an IPv4 CIDR", key)
	}
	return nil
}

func validateDNSUpstream(upstream Upstream) error {
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		return fmt.Errorf("invalid upstream url %q: %w", upstream.URL, err)
	}
	for _, domain := range upstream.Domains {
		if err := validateUpstreamDomainMatcher(domain); err != nil {
			return fmt.Errorf("upstream %q domain %q: %w", upstream.URL, domain, err)
		}
	}

	switch parsed.Scheme {
	case "https", "tls", "dot":
	default:
		return nil
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("upstream %q is missing host", upstream.URL)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if upstream.Bootstrap == "" {
		return fmt.Errorf("upstream %q uses hostname %q and requires bootstrap", upstream.URL, host)
	}
	return nil
}

func validateUpstreamDomainMatcher(rule string) error {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return fmt.Errorf("empty matcher")
	}
	kind, value := dnsrule.Split(rule)
	if kind == dnsrule.KindRegexp {
		_, err := regexp.Compile(value)
		return err
	}
	return nil
}
