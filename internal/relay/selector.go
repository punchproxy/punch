package relay

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

const directGroupName = "DIRECT"

const defaultCheckConcurrency = 10
const defaultFullCheckInterval = 24 * time.Hour
const defaultSelectedCheckInterval = 10 * time.Second
const defaultFullTriggerFailures = 5
const maxHealthRecords = 30

var (
	ErrRelaySelectionAutoGroup = errors.New("relay belongs to an auto relay group")
	ErrRelaySelectionAmbiguous = errors.New("relay name is ambiguous")
	ErrGroupSelectionAutoMode  = errors.New("relay group selection is auto")
)

type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthDegraded HealthStatus = "degraded"
	HealthDown     HealthStatus = "down"
	HealthPending  HealthStatus = "pending"
	HealthChecking HealthStatus = "checking"
	HealthUntested HealthStatus = "untested"
)

type RelayHealth struct {
	Name              string         `json:"name"`
	Group             string         `json:"group"`
	Type              string         `json:"type"`
	Addr              string         `json:"addr"`
	Status            HealthStatus   `json:"status"`
	Latency           int64          `json:"latency_ms"`
	TCPConnectLatency int64          `json:"tcp_connect_latency_ms,omitempty"`
	URLTestLatency    int64          `json:"url_test_latency_ms,omitempty"`
	CheckInterval     int64          `json:"check_interval,omitempty"`
	LastCheckedAt     time.Time      `json:"last_checked_at,omitempty"`
	LastRefreshedAt   time.Time      `json:"last_refreshed_at,omitempty"`
	NextRefreshAt     time.Time      `json:"next_refresh_at,omitempty"`
	RefreshInterval   int64          `json:"refresh_interval,omitempty"`
	Selected          bool           `json:"selected"`
	GroupMode         string         `json:"group_mode,omitempty"`
	GroupSourceURL    string         `json:"group_source_url,omitempty"`
	Spec              map[string]any `json:"spec,omitempty"`
	History           []HealthRecord `json:"history,omitempty"`
	Error             string         `json:"error,omitempty"`
	Upload            atomic.Int64   `json:"-"`
	Download          atomic.Int64   `json:"-"`
}

type HealthRecord struct {
	Time              time.Time    `json:"time"`
	Status            HealthStatus `json:"status"`
	Latency           int64        `json:"latency_ms,omitempty"`
	TCPConnectLatency int64        `json:"tcp_connect_latency_ms,omitempty"`
}

type GroupStatus struct {
	Name                     string       `json:"name"`
	Type                     string       `json:"type"`
	RelayCount               int          `json:"relay_count"`
	Selected                 bool         `json:"selected"`
	Select                   string       `json:"select"`
	CurrentRelay             string       `json:"current_relay,omitempty"`
	CurrentStatus            HealthStatus `json:"current_status,omitempty"`
	CurrentLatency           int64        `json:"current_latency_ms,omitempty"`
	CurrentTCPConnectLatency int64        `json:"current_tcp_connect_latency_ms,omitempty"`
	RemoteAddress            string       `json:"remote_address,omitempty"`
	CheckInterval            int64        `json:"check_interval,omitempty"`
	LastCheckedAt            time.Time    `json:"last_checked_at,omitempty"`
	NextCheckAt              time.Time    `json:"next_check_at,omitempty"`
	LastRefreshedAt          time.Time    `json:"last_refreshed_at,omitempty"`
	NextRefreshAt            time.Time    `json:"next_refresh_at,omitempty"`
	RefreshInterval          int64        `json:"refresh_interval,omitempty"`
	Error                    string       `json:"error,omitempty"`
}

type group struct {
	name            string
	mode            string
	sourceURL       string
	loadError       string
	dialers         []Dialer
	specs           map[string]map[string]any
	refreshEvery    time.Duration
	lastRefreshedAt time.Time
	nextRefreshAt   time.Time
	refreshing      bool
	active          atomic.Int32
}

type Selector struct {
	mu                      sync.RWMutex
	groups                  []*group
	health                  map[string]*RelayHealth
	active                  atomic.Int32
	mode                    string
	outsideURL              string
	domesticURL             string
	fullCheckInterval       time.Duration
	selectedCheckInterval   time.Duration
	fullTriggerFailures     int
	selectedCheckFailures   int
	selectedCheckFailureKey string
	tolerance               time.Duration
	domesticHealth          ConnectivityCheck
	outsideHealth           ConnectivityCheck
	outsideHealthKey        string
	checkSem                chan struct{}
	bus                     *eventbus.Bus
	stopCh                  chan struct{}
	benchmarkConfigCh       chan struct{}
	selectedConfigCh        chan struct{}
	store                   *config.Store
	assets                  *assets.Manager
	groupCfgs               map[string]config.RelayGroup
	directDialContext       DialContextFunc
	resolveRelayDomain      RelayResolveFunc
}

func NewSelector(
	relayCfg config.Relay,
	checkCfg config.Check,
	assetManager *assets.Manager,
	directDialContext DialContextFunc,
	stateStore *config.Store,
	bus *eventbus.Bus,
	resolveRelayDomain RelayResolveFunc,
) (*Selector, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("relay: state store is required")
	}
	adapter.UnifiedDelay.Store(true)

	s := &Selector{
		health:                make(map[string]*RelayHealth),
		mode:                  normalizeSelectMode(relayCfg.Select),
		outsideURL:            checkCfg.OutsideURL,
		domesticURL:           checkCfg.DomesticURL,
		fullCheckInterval:     normalizeFullCheckInterval(checkCfg.FullInterval),
		selectedCheckInterval: normalizeSelectedCheckInterval(checkCfg.Interval),
		fullTriggerFailures:   normalizeFullTriggerFailures(checkCfg.FullTriggerFailures),
		tolerance:             time.Duration(checkCfg.Tolerance) * time.Millisecond,
		domesticHealth:        ConnectivityCheck{URL: checkCfg.DomesticURL},
		outsideHealth:         ConnectivityCheck{URL: checkCfg.OutsideURL},
		checkSem:              make(chan struct{}, normalizeCheckConcurrency(checkCfg.Concurrency)),
		bus:                   bus,
		stopCh:                make(chan struct{}),
		benchmarkConfigCh:     make(chan struct{}, 1),
		selectedConfigCh:      make(chan struct{}, 1),
		store:                 stateStore,
		assets:                assetManager,
		groupCfgs:             make(map[string]config.RelayGroup),
		directDialContext:     directDialContext,
		resolveRelayDomain:    resolveRelayDomain,
	}

	if err := s.ApplyConfig(relayCfg, checkCfg); err != nil {
		return nil, err
	}
	if assetManager != nil {
		assetManager.OnReady(s.onAssetReady)
	}
	slog.Debug("relay selector initialized", "groups", len(s.groups), "mode", s.mode)
	return s, nil
}

func (s *Selector) Mode() string { return s.mode }
