package relay

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/assets"
	"github.com/punchproxy/punch/internal/config"
)

func TestScheduleRefreshRetryBackoffDoublesAndCaps(t *testing.T) {
	g := &group{refreshEvery: 5 * time.Minute}
	now := time.Now()

	g.scheduleRefreshRetryLocked(now)
	if g.refreshBackoff != time.Minute {
		t.Fatalf("first backoff = %v, want 1m", g.refreshBackoff)
	}
	if !g.nextRefreshAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("nextRefreshAt = %v, want now+1m", g.nextRefreshAt)
	}

	g.scheduleRefreshRetryLocked(now)
	if g.refreshBackoff != 2*time.Minute {
		t.Fatalf("second backoff = %v, want 2m", g.refreshBackoff)
	}
	g.scheduleRefreshRetryLocked(now)
	g.scheduleRefreshRetryLocked(now)
	if g.refreshBackoff != 5*time.Minute {
		t.Fatalf("capped backoff = %v, want refreshEvery 5m", g.refreshBackoff)
	}

	// A refresh interval shorter than the base delay caps immediately.
	short := &group{refreshEvery: 30 * time.Second}
	short.scheduleRefreshRetryLocked(now)
	if short.refreshBackoff != 30*time.Second {
		t.Fatalf("short-interval backoff = %v, want 30s", short.refreshBackoff)
	}

	// Groups without auto refresh are left alone.
	manual := &group{}
	manual.scheduleRefreshRetryLocked(now)
	if manual.refreshBackoff != 0 || !manual.nextRefreshAt.IsZero() {
		t.Fatalf("manual group rescheduled: backoff=%v next=%v", manual.refreshBackoff, manual.nextRefreshAt)
	}
}

func TestFailedRefreshBacksOffInsteadOfRetryingEveryTick(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	st, err := config.Open(filepath.Join(t.TempDir(), "punch.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	assetManager, err := assets.New(st, 0, nil, nil)
	if err != nil {
		t.Fatalf("new asset manager: %v", err)
	}

	g := &group{
		name:          "main",
		sourceURL:     server.URL,
		refreshEvery:  10 * time.Minute,
		nextRefreshAt: time.Now().Add(-time.Hour),
	}
	s := &Selector{
		assets: assetManager,
		groups: []*group{g},
		groupCfgs: map[string]config.RelayGroup{
			"main": {Type: "remote", Name: "main", URL: server.URL, RefreshDuration: 600},
		},
	}

	if due := s.dueRefreshGroups(); len(due) != 1 || due[0] != "main" {
		t.Fatalf("dueRefreshGroups() = %v, want [main]", due)
	}
	// dueRefreshGroups marked the group refreshing, as the refresh loop does.
	before := time.Now()
	if err := s.RefreshGroup("main"); err == nil {
		t.Fatal("RefreshGroup() succeeded, want fetch error")
	}

	if g.refreshing {
		t.Fatal("group still marked refreshing after failed fetch")
	}
	if g.refreshBackoff != time.Minute {
		t.Fatalf("backoff after first failure = %v, want 1m", g.refreshBackoff)
	}
	if g.nextRefreshAt.Before(before.Add(time.Minute)) {
		t.Fatalf("nextRefreshAt = %v, want at least now+1m", g.nextRefreshAt)
	}
	if delay := s.refreshRetryDelay("main"); delay < 55*time.Second || delay > time.Minute {
		t.Fatalf("refreshRetryDelay() = %v, want ~1m", delay)
	}
	if due := s.dueRefreshGroups(); len(due) != 0 {
		t.Fatalf("group due again immediately after failure: %v", due)
	}

	// A second failure doubles the delay.
	g.nextRefreshAt = time.Now().Add(-time.Second)
	if due := s.dueRefreshGroups(); len(due) != 1 {
		t.Fatalf("dueRefreshGroups() after backoff elapsed = %v, want [main]", due)
	}
	if err := s.RefreshGroup("main"); err == nil {
		t.Fatal("RefreshGroup() succeeded, want fetch error")
	}
	if g.refreshBackoff != 2*time.Minute {
		t.Fatalf("backoff after second failure = %v, want 2m", g.refreshBackoff)
	}
	if g.refreshing {
		t.Fatal("group still marked refreshing after second failed fetch")
	}
}
