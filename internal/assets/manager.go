// Package assets caches remote rule/relay lists so Punch can keep working
// between refreshes and across restarts. Cached bodies live in the config
// store; the manager just coordinates fetches and
// stale-while-revalidate reads.
package assets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/punchproxy/punch/internal/config"
)

// ErrNotCached indicates a remote asset has no cached copy yet. The manager
// kicks off an async fetch when it returns this; callers should treat the
// asset as unavailable for now and rely on the ReadyHandler mechanism to be
// notified when the fetch completes.
var ErrNotCached = errors.New("assets: not yet cached")

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// ReadyHandler is invoked (outside any internal lock) after a remote asset
// has been successfully downloaded and cached.
type ReadyHandler func(url string)

type Manager struct {
	store           *config.Store
	refreshInterval time.Duration
	client          *http.Client
	directClient    *http.Client

	mu        sync.Mutex
	refreshes map[string]*refreshState
	failed    map[string]bool
	ready     bool // true after MarkReady is called; suppresses background refresh during startup
	handlers  []ReadyHandler
}

type refreshState struct {
	done    chan struct{}
	content []byte
	err     error
}

// AssetStatus is returned by Status().
type AssetStatus struct {
	LastUpdated time.Time
}

// New constructs an asset manager backed by s. dialContext is used for the
// primary (typically proxied) HTTP client; directDialContext is used when
// callers explicitly request a direct fetch or when the proxied fetch has
// no chance of succeeding (e.g. no usable relay group).
func New(s *config.Store, refreshInterval time.Duration, dialContext DialContextFunc, directDialContext DialContextFunc) (*Manager, error) {
	if s == nil {
		return nil, fmt.Errorf("assets: store is required")
	}
	return &Manager{
		store:           s,
		refreshInterval: refreshInterval,
		client:          newHTTPClient(dialContext),
		directClient:    newHTTPClient(directDialContext),
		refreshes:       make(map[string]*refreshState),
		failed:          make(map[string]bool),
	}, nil
}

// MarkReady signals that startup is complete and background refreshes may proceed.
func (m *Manager) MarkReady() {
	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
}

// OnReady registers a handler invoked whenever a remote asset finishes
// downloading successfully. Handlers fire after the cache is populated and
// are called outside the manager's internal lock.
func (m *Manager) OnReady(h ReadyHandler) {
	if h == nil {
		return
	}
	m.mu.Lock()
	m.handlers = append(m.handlers, h)
	m.mu.Unlock()
}

// Open returns a reader for source. Remote sources are served from the store
// cache (refreshed in the background if stale). Local paths are opened from
// disk unchanged.
func (m *Manager) Open(source string) (io.ReadCloser, error) {
	if !isRemote(source) {
		return openLocal(source)
	}
	content, err := m.ensureCached(source, false)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

// OpenDirect is like Open but always fetches through the direct dial context.
func (m *Manager) OpenDirect(source string) (io.ReadCloser, error) {
	if !isRemote(source) {
		return openLocal(source)
	}
	content, err := m.ensureCached(source, true)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func openLocal(source string) (io.ReadCloser, error) {
	if strings.HasPrefix(source, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			source = filepath.Join(home, strings.TrimPrefix(source, "~/"))
		}
	}
	slog.Debug("open local asset", "source", source)
	return os.Open(source)
}

func (m *Manager) ensureCached(url string, direct bool) ([]byte, error) {
	m.mu.Lock()
	cached, _ := m.store.GetAsset(url)

	if cached != nil && !m.shouldRefresh(cached.UpdatedAt) {
		slog.Debug("use cached asset", "url", url, "last_updated", cached.UpdatedAt)
		m.mu.Unlock()
		return cached.Content, nil
	}

	if cached != nil {
		if m.ready {
			slog.Debug("use stale cached asset and refresh in background", "url", url, "last_updated", cached.UpdatedAt)
			_ = m.startRefreshLocked(url, direct)
		} else {
			slog.Debug("use cached asset (startup, skip refresh)", "url", url, "last_updated", cached.UpdatedAt)
		}
		m.mu.Unlock()
		return cached.Content, nil
	}

	// No cached copy: kick off an async fetch and return ErrNotCached so the
	// caller can proceed without blocking startup. Affected components are
	// notified via OnReady handlers once the fetch completes.
	_ = m.startRefreshLocked(url, direct)
	m.mu.Unlock()
	slog.Debug("asset not yet cached, async download in progress", "url", url)
	return nil, ErrNotCached
}

func (m *Manager) startRefreshLocked(url string, direct bool) *refreshState {
	if state, ok := m.refreshes[url]; ok {
		return state
	}
	state := &refreshState{done: make(chan struct{})}
	m.refreshes[url] = state
	go m.runRefresh(url, direct, state)
	return state
}

func (m *Manager) runRefresh(url string, direct bool, state *refreshState) {
	defer close(state.done)

	body, err := m.fetchWithFallback(url, direct)
	if err != nil {
		state.err = err
		m.mu.Lock()
		m.failed[url] = true
		delete(m.refreshes, url)
		m.mu.Unlock()
		return
	}

	now := time.Now().UTC()
	if err := m.store.PutAsset(url, body, now); err != nil {
		state.err = err
		m.mu.Lock()
		delete(m.refreshes, url)
		m.mu.Unlock()
		return
	}
	slog.Debug("cached remote asset", "url", url, "bytes", len(body))

	state.content = body

	m.mu.Lock()
	delete(m.failed, url)
	delete(m.refreshes, url)
	handlers := append([]ReadyHandler(nil), m.handlers...)
	m.mu.Unlock()

	for _, h := range handlers {
		h(url)
	}
}

// fetchWithFallback tries the primary (proxied) client first, falling back to
// the direct client if the primary fetch fails. This means rule/relay-list
// refreshes succeed on first launch when no relay groups are configured yet.
func (m *Manager) fetchWithFallback(url string, direct bool) ([]byte, error) {
	if direct {
		return fetch(m.directClient, url)
	}
	body, err := fetch(m.client, url)
	if err == nil {
		return body, nil
	}
	slog.Debug("proxied asset fetch failed, retrying directly", "url", url, "error", err)
	body2, err2 := fetch(m.directClient, url)
	if err2 != nil {
		return nil, err
	}
	return body2, nil
}

// RetryFailedAsync kicks off (proxied) refreshes for every URL whose last
// fetch failed. Callers invoke this when a new relay becomes usable.
func (m *Manager) RetryFailedAsync() {
	m.mu.Lock()
	failed := make([]string, 0, len(m.failed))
	for url := range m.failed {
		failed = append(failed, url)
	}
	m.mu.Unlock()

	for _, url := range failed {
		m.mu.Lock()
		_ = m.startRefreshLocked(url, false)
		m.mu.Unlock()
	}
}

// Status returns the last-updated timestamp for source, if cached.
func (m *Manager) Status(source string) (AssetStatus, bool) {
	if !isRemote(source) {
		return AssetStatus{}, false
	}
	a, err := m.store.GetAsset(source)
	if err != nil || a == nil {
		return AssetStatus{}, false
	}
	return AssetStatus{LastUpdated: a.UpdatedAt}, true
}

// RefreshInterval returns the configured refresh interval, or zero when
// background refreshes are disabled.
func (m *Manager) RefreshInterval() time.Duration {
	return m.refreshInterval
}

// Refresh triggers an immediate refresh of source and waits for completion.
func (m *Manager) Refresh(source string, direct bool) error {
	if !isRemote(source) {
		return nil
	}
	m.mu.Lock()
	state := m.startRefreshLocked(source, direct)
	m.mu.Unlock()
	<-state.done
	return state.err
}

func (m *Manager) shouldRefresh(updatedAt time.Time) bool {
	if m.refreshInterval <= 0 {
		return false
	}
	return time.Since(updatedAt) >= m.refreshInterval
}

func fetch(client *http.Client, url string) ([]byte, error) {
	slog.Debug("download remote asset", "url", url)
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return body, nil
}

func newHTTPClient(dialContext DialContextFunc) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if dialContext != nil {
		transport.DialContext = dialContext
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

func isRemote(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}
