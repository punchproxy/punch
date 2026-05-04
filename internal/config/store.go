// Package config holds Punch's runtime configuration and SQLite-backed
// persistence. Configuration is stored in typed tables; downloaded assets and
// small runtime settings share the same database handle.
package config

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrNotFound indicates a lookup missed.
var ErrNotFound = errors.New("not found")

// Store is a SQLite-backed persistence handle.
type Store struct {
	db *gorm.DB
}

// Open opens (or creates) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("sqlite handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DB exposes the underlying GORM handle for low-level callers and tests.
func (s *Store) DB() *gorm.DB { return s.db }

// SQLDB exposes the underlying database/sql handle for integration points that
// require it.
func (s *Store) SQLDB() (*sql.DB, error) { return s.db.DB() }

func (s *Store) migrate() error {
	if err := s.db.AutoMigrate(
		&settingModel{},
		&assetModel{},
		&schemaVersionModel{},
		&configBaseModel{},
		&tunRouteModel{},
		&dnsUpstreamModel{},
		&dnsUpstreamDomainModel{},
		&dnsRuleSourceModel{},
		&relayGroupModel{},
		&relayGroupResolverModel{},
		&relayGroupResolverDomainModel{},
		&relayGroupProxyModel{},
		&relaySelectionModel{},
	); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

type settingModel struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value []byte `gorm:"column:value;not null"`
}

func (settingModel) TableName() string { return "settings" }

type assetModel struct {
	URL       string `gorm:"column:url;primaryKey"`
	Content   []byte `gorm:"column:content;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (assetModel) TableName() string { return "assets" }

type schemaVersionModel struct {
	Version int `gorm:"column:version;primaryKey"`
}

func (schemaVersionModel) TableName() string { return "schema_version" }

type configBaseModel struct {
	ID                    int    `gorm:"column:id;primaryKey;autoIncrement:false"`
	LogLevel              string `gorm:"column:log_level;not null"`
	LogFile               string `gorm:"column:log_file;not null"`
	AssetRefreshInterval  int    `gorm:"column:asset_refresh_interval;not null"`
	DNSListen             string `gorm:"column:dns_listen;not null"`
	DNSCacheSize          int    `gorm:"column:dns_cache_size;not null"`
	DNSFakeIPRange        string `gorm:"column:dns_fake_ip_range;not null"`
	DNSFakeIPv6Range      string `gorm:"column:dns_fake_ipv6_range"`
	DNSFakeIPTTL          string `gorm:"column:dns_fakeip_ttl;not null;default:1h"`
	DNSDisableIPv6FakeIP  *bool  `gorm:"column:dns_disable_ipv6_fakeip;default:1"`
	TUNDevice             string `gorm:"column:tun_device;not null"`
	RelaySelect           string `gorm:"column:relay_select;not null"`
	RelayAutoMode         string `gorm:"column:relay_auto_mode;not null"`
	RelayAutoURL          string `gorm:"column:relay_auto_url;not null"`
	RelayAutoInterval     int    `gorm:"column:relay_auto_interval;not null"`
	RelayAutoTolerance    int    `gorm:"column:relay_auto_tolerance;not null"`
	RelayCheckConcurrency int    `gorm:"column:relay_check_concurrency;not null;default:10"`
	APIListen             string `gorm:"column:api_listen;not null"`
	APISecret             string `gorm:"column:api_secret;not null"`
	SessionsHistoryLimit  int    `gorm:"column:sessions_history_limit;not null;default:1000"`
}

func (configBaseModel) TableName() string { return "config_base" }

type tunRouteModel struct {
	Position int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	Route    string `gorm:"column:route;not null"`
}

func (tunRouteModel) TableName() string { return "tun_routes" }

type dnsUpstreamModel struct {
	Position  int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	URL       string `gorm:"column:url;not null"`
	Bootstrap string `gorm:"column:bootstrap;not null"`
}

func (dnsUpstreamModel) TableName() string { return "dns_upstreams" }

type dnsUpstreamDomainModel struct {
	UpstreamPosition int    `gorm:"column:upstream_position;primaryKey;autoIncrement:false"`
	Position         int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	Domain           string `gorm:"column:domain;not null"`
}

func (dnsUpstreamDomainModel) TableName() string { return "dns_upstream_domains" }

type dnsRuleSourceModel struct {
	Kind     string `gorm:"column:kind;primaryKey"`
	Decision string `gorm:"column:decision;primaryKey"`
	Position int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	Source   string `gorm:"column:source;not null"`
}

func (dnsRuleSourceModel) TableName() string { return "dns_rule_sources" }

type relayGroupModel struct {
	Position        int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	Type            string `gorm:"column:type;not null"`
	Name            string `gorm:"column:name;not null"`
	URL             string `gorm:"column:url;not null"`
	RefreshDuration int    `gorm:"column:refresh_duration;not null"`
	Keep            string `gorm:"column:keep;not null"`
	Remove          string `gorm:"column:remove;not null"`
	Select          string `gorm:"column:select;not null"`
}

func (relayGroupModel) TableName() string { return "relay_groups" }

type relayGroupResolverModel struct {
	GroupPosition int    `gorm:"column:group_position;primaryKey;autoIncrement:false"`
	Position      int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	URL           string `gorm:"column:url;not null"`
	Bootstrap     string `gorm:"column:bootstrap;not null"`
}

func (relayGroupResolverModel) TableName() string { return "relay_group_resolvers" }

type relayGroupResolverDomainModel struct {
	GroupPosition    int    `gorm:"column:group_position;primaryKey;autoIncrement:false"`
	ResolverPosition int    `gorm:"column:resolver_position;primaryKey;autoIncrement:false"`
	Position         int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	Domain           string `gorm:"column:domain;not null"`
}

func (relayGroupResolverDomainModel) TableName() string { return "relay_group_resolver_domains" }

type relayGroupProxyModel struct {
	GroupPosition int    `gorm:"column:group_position;primaryKey;autoIncrement:false"`
	Position      int    `gorm:"column:position;primaryKey;autoIncrement:false"`
	ProxyYAML     []byte `gorm:"column:proxy_yaml;not null"`
}

func (relayGroupProxyModel) TableName() string { return "relay_group_proxies" }

type relaySelectionModel struct {
	GroupName string `gorm:"column:group_name;primaryKey"`
	RelayName string `gorm:"column:relay_name;not null"`
	Active    bool   `gorm:"column:active;not null"`
	Position  int    `gorm:"column:position;not null"`
}

func (relaySelectionModel) TableName() string { return "relay_selections" }

// GetSetting returns the raw bytes stored under key, or ErrNotFound.
func (s *Store) GetSetting(key string) ([]byte, error) {
	var row settingModel
	err := s.db.First(&row, "key = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get setting %s: %w", key, err)
	}
	return row.Value, nil
}

// SetSetting upserts a value under key.
func (s *Store) SetSetting(key string, value []byte) error {
	err := s.db.Save(&settingModel{Key: key, Value: value}).Error
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// Asset is a cached remote asset.
type Asset struct {
	URL       string
	Content   []byte
	UpdatedAt time.Time
}

// GetAsset returns the cached asset for url or ErrNotFound.
func (s *Store) GetAsset(url string) (*Asset, error) {
	var row assetModel
	err := s.db.First(&row, "url = ?", url).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get asset %s: %w", url, err)
	}
	return &Asset{
		URL:       row.URL,
		Content:   row.Content,
		UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC(),
	}, nil
}

// PutAsset upserts an asset blob.
func (s *Store) PutAsset(url string, content []byte, updatedAt time.Time) error {
	err := s.db.Save(&assetModel{
		URL:       url,
		Content:   content,
		UpdatedAt: updatedAt.Unix(),
	}).Error
	if err != nil {
		return fmt.Errorf("put asset %s: %w", url, err)
	}
	return nil
}

// AssetMeta describes a cached asset without loading its body.
type AssetMeta struct {
	URL       string
	UpdatedAt time.Time
	Size      int64
}

// ListAssetMeta returns metadata for every cached asset.
func (s *Store) ListAssetMeta() ([]AssetMeta, error) {
	var rows []assetModel
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	out := make([]AssetMeta, 0, len(rows))
	for _, row := range rows {
		out = append(out, AssetMeta{
			URL:       row.URL,
			UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC(),
			Size:      int64(len(row.Content)),
		})
	}
	return out, nil
}
