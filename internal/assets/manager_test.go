package assets

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/config"
)

func newTestManager(t *testing.T, refreshInterval time.Duration) *Manager {
	t.Helper()
	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	m, err := New(st, refreshInterval, dial, dial)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// openAndDrain opens source, ignoring ErrNotCached (the first open of an
// uncached remote asset only kicks off the async fetch).
func openAndDrain(t *testing.T, m *Manager, source string) {
	t.Helper()
	rc, err := m.Open(source)
	if err != nil {
		return
	}
	defer rc.Close()
	_, _ = io.ReadAll(rc)
}

// A remote list opened once at startup must still be re-fetched later: nothing
// re-opens it, so without the refresh loop it goes stale forever while the UI
// advertises a next-update time.
func TestRefreshStaleSourcesRefetchesOpenedAssets(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("body"))
	}))
	defer srv.Close()

	m := newTestManager(t, time.Millisecond)

	// The first open only records the source and kicks off an async fetch;
	// Refresh waits for that fetch (or joins it) so the cache is populated
	// before the handler below is registered.
	openAndDrain(t, m, srv.URL)
	if err := m.Refresh(srv.URL, false); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	before := hits.Load()

	readyHits := make(chan string, 4)
	m.OnReady(func(url string) { readyHits <- url })
	m.MarkReady()

	time.Sleep(5 * time.Millisecond) // let the cached copy age past the interval
	m.refreshStaleSources()

	select {
	case got := <-readyHits:
		if got != srv.URL {
			t.Fatalf("ready handler url = %q, want %q", got, srv.URL)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale asset was never re-fetched")
	}
	if got := hits.Load(); got <= before {
		t.Fatalf("upstream hits = %d, want more than %d", got, before)
	}
}

func TestRefreshStaleSourcesSkipsFreshAndUnopenedAssets(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("body"))
	}))
	defer srv.Close()

	m := newTestManager(t, time.Hour)
	openAndDrain(t, m, srv.URL)
	waitFor(t, func() bool { return hits.Load() >= 1 })
	m.MarkReady()

	before := hits.Load()
	m.refreshStaleSources()
	time.Sleep(50 * time.Millisecond)
	if got := hits.Load(); got != before {
		t.Fatalf("fresh asset was re-fetched: hits %d -> %d", before, got)
	}
}

func TestRefreshCheckIntervalStaysWithinBounds(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
	}{
		{time.Hour, 15 * time.Minute},
		{4 * time.Minute, time.Minute},
		{20 * time.Minute, 5 * time.Minute},
		{time.Second, time.Minute},
	} {
		if got := refreshCheckInterval(tc.in); got != tc.want {
			t.Errorf("refreshCheckInterval(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
