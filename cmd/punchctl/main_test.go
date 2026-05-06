package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDNSUpstreamsCommand(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/upstreams" {
			t.Fatalf("path = %q, want /api/dns/upstreams", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]upstreamStats{{
			URL:            "https://dns.example/dns-query",
			Queries:        12,
			AverageLatency: 34,
			LastLatency:    56,
			LastQueriedAt:  "2026-04-28T12:34:56Z",
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "--token", "secret", "dns", "upstreams"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", gotAuth)
	}
	text := out.String()
	for _, want := range []string{"UPSTREAM", "QUERIES", "AVG-LATENCY", "https://dns.example/dns-query", "12", "34ms"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestStatusCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			t.Fatalf("path = %q, want /api/status", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(statusInfo{
			General: statusGeneral{
				Version:       "v1.2.3",
				Architecture:  "darwin/arm64",
				UptimeSeconds: 3723,
				StartedAt:     time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
				MemoryBytes:   12 * 1024 * 1024,
				Goroutines:    17,
			},
			DNS: statusDNS{
				Relay:        statusDecisionStat{Requests: 10, LastDomain: "google.com"},
				Direct:       statusDecisionStat{Requests: 20, LastDomain: "example.com"},
				Reject:       statusDecisionStat{Requests: 3, LastDomain: "ads.example"},
				CacheEntries: 42,
				CacheHits:    7,
			},
			Connectivity: statusConnectivity{
				Domestic: statusConnectivityCheck{
					URL:                 "http://connect.rom.miui.com/generate_204",
					Status:              "healthy",
					LatencyMS:           45,
					TCPConnectLatencyMS: 12,
					LastCheckedAt:       time.Date(2026, 4, 28, 12, 0, 30, 0, time.UTC),
				},
				Outside: statusConnectivityCheck{
					URL:                 "http://www.gstatic.com/generate_204",
					Status:              "healthy",
					LatencyMS:           88,
					TCPConnectLatencyMS: 31,
					LastCheckedAt:       time.Date(2026, 4, 28, 12, 1, 0, 0, time.UTC),
				},
			},
			Relay: statusRelay{
				ActiveRelay:            "auto / hk-1",
				Status:                 "healthy",
				LatencyMS:              88,
				TCPConnectLatencyMS:    31,
				URLTestLatencyMS:       88,
				LastCheckedAt:          time.Date(2026, 4, 28, 12, 1, 0, 0, time.UTC),
				ActiveSessions:         4,
				TotalProcessedSessions: 99,
				DownloadBytes:          3 * 1024 * 1024,
				UploadBytes:            512 * 1024,
				DownloadBPS:            2048,
				UploadBPS:              1024,
				UDP: statusUDP{
					PacketsEnqueued: 300,
					PacketsDropped:  7,
					QueueFullDrops:  5,
					ClosedDrops:     1,
					PendingDrops:    1,
				},
			},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"General:", "Client Version: dev", "Server Version: v1.2.3", "Relay:          10 requests, last google.com", "Cache:          42 entries, 7 hits", "Connectivity:", "Domestic URL:  http://connect.rom.miui.com/generate_204", "Domestic:      healthy (tc latency 12ms, latency 45ms, last check", "Outside URL:   http://www.gstatic.com/generate_204", "Outside:       healthy (tc latency 31ms, latency 88ms, last check", "Active:         auto / hk-1", "Latency:        88ms (tcp 31ms, url 88ms)", "Sessions:       4 active, 99 total processed", "Download:       3.0 MB total, 2.0 KB/s", "UDP Packets:    300 enqueued, 7 dropped (5 queue full, 1 closed, 1 pending)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: http.DefaultClient,
	})
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), "punchctl dev"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestDNSUpstreamsCommandJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]upstreamStats{{
			URL:            "https://dns.example/dns-query",
			Queries:        12,
			AverageLatency: 34,
			LastLatency:    56,
			LastQueriedAt:  "2026-04-28T12:34:56Z",
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "upstreams", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{`"url"`, `"queries"`, `"average_latency_ms"`, `34`, `56`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json output missing %q:\n%s", want, text)
		}
	}
}

func TestDNSUpstreamsFieldSelectorAndSort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]upstreamStats{
			{URL: "https://b.example/dns-query", Queries: 2, AverageLatency: 20},
			{URL: "https://a.example/dns-query", Queries: 12, AverageLatency: 10},
			{URL: "https://c.example/dns-query", Queries: 0, AverageLatency: 30},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "upstreams", "--field-selector", "queries!=0", "--sort-by", "-.queries"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	if strings.Contains(text, "https://c.example/dns-query") {
		t.Fatalf("field selector did not filter zero-query upstream:\n%s", text)
	}
	first := strings.Index(text, "https://a.example/dns-query")
	second := strings.Index(text, "https://b.example/dns-query")
	if first == -1 || second == -1 || first > second {
		t.Fatalf("sort order = %d then %d, want a before b:\n%s", first, second, text)
	}
}

func TestSortByPutsDashValuesLast(t *testing.T) {
	rows := []upstreamRow{
		{URL: "unknown", AverageLatency: 0},
		{URL: "slow", AverageLatency: 120},
		{URL: "fast", AverageLatency: 9},
	}
	sorted, err := prepareRows(rows, "", "average_latency")
	if err != nil {
		t.Fatalf("prepareRows() error = %v", err)
	}
	got := []string{sorted[0].URL, sorted[1].URL, sorted[2].URL}
	want := []string{"fast", "slow", "unknown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ascending sort = %v, want %v", got, want)
	}

	sorted, err = prepareRows(rows, "", "-average_latency")
	if err != nil {
		t.Fatalf("prepareRows() error = %v", err)
	}
	got = []string{sorted[0].URL, sorted[1].URL, sorted[2].URL}
	want = []string{"slow", "fast", "unknown"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("descending sort = %v, want %v", got, want)
	}
}

func TestDNSUpstreamsDeleteCommand(t *testing.T) {
	var gotMethod string
	var gotURL string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Query().Get("url")
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/dns/upstreams" {
			t.Fatalf("path = %q, want /api/dns/upstreams", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "--token", "secret", "dns", "upstreams", "delete", "https://dns.alidns.com/dns-query"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", gotMethod)
	}
	if gotURL != "https://dns.alidns.com/dns-query" {
		t.Fatalf("url query = %q", gotURL)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", gotAuth)
	}
	if !strings.Contains(out.String(), "deleted") {
		t.Fatalf("output = %q, want deletion confirmation", out.String())
	}
}

func TestDNSUpstreamsCreateCommand(t *testing.T) {
	var gotMethod string
	var gotAuth string
	var gotReq createUpstreamRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/dns/upstreams" {
			t.Fatalf("path = %q, want /api/dns/upstreams", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{
		"--addr", server.URL,
		"--token", "secret",
		"dns", "upstreams", "create", "https://dns.example/dns-query",
		"--bootstrap", "1.1.1.1",
		"--domains", "google.com,full:example.com,keyword:needle,regexp:.+\\.test$",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", gotAuth)
	}
	if gotReq.URL != "https://dns.example/dns-query" || gotReq.Bootstrap != "1.1.1.1" {
		t.Fatalf("request = %+v", gotReq)
	}
	wantDomains := []string{"google.com", "full:example.com", "keyword:needle", "regexp:.+\\.test$"}
	if strings.Join(gotReq.Domains, "|") != strings.Join(wantDomains, "|") {
		t.Fatalf("domains = %#v, want %#v", gotReq.Domains, wantDomains)
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("output = %q, want creation confirmation", out.String())
	}
}

func TestDNSUpstreamsSetCommand(t *testing.T) {
	var putBody createUpstreamRequest
	var putCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/upstreams" {
			t.Fatalf("path = %q, want /api/dns/upstreams", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]upstreamStats{{
				URL:       "https://dns.example/dns-query",
				Bootstrap: "1.1.1.1",
				Domains:   []string{"google.com"},
			}})
		case http.MethodPut:
			putCalled = true
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Fatalf("decode put body: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected method %q", r.Method)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{
		"--addr", server.URL,
		"dns", "upstreams", "set", "https://dns.example/dns-query",
		"--bootstrap", "8.8.8.8",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !putCalled {
		t.Fatal("PUT was not called")
	}
	if putBody.URL != "https://dns.example/dns-query" || putBody.Bootstrap != "8.8.8.8" {
		t.Fatalf("put body = %+v", putBody)
	}
	if strings.Join(putBody.Domains, "|") != "google.com" {
		t.Fatalf("domains = %#v, want preserved [google.com]", putBody.Domains)
	}
	if !strings.Contains(out.String(), "updated") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDNSUpstreamsSetCommandRequiresFlag(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &errOut,
		client: http.DefaultClient,
	})
	cmd.SetArgs([]string{"dns", "upstreams", "set", "https://dns.example/dns-query"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("Execute() error = %v, want 'nothing to update'", err)
	}
}

func TestDNSUpstreamsSetCommandMissingUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %q", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]upstreamStats{})
	}))
	defer server.Close()

	cmd := newRootCommand(commandConfig{
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "upstreams", "set", "https://missing.example/dns-query", "--bootstrap", "1.1.1.1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Execute() error = %v, want not found", err)
	}
}

func TestDNSUpstreamsDeleteMissingReturnsClearError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream not found", http.StatusNotFound)
	}))
	defer server.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &errOut,
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "upstreams", "delete", "https://missing.example/dns-query"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want not found error")
	}
	if !strings.Contains(err.Error(), "upstream") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Execute() error = %v, want clear not found message", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestDNSCacheCommand(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/cache" {
			t.Fatalf("path = %q, want /api/dns/cache", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]cacheEntry{{
			Name:          "example.com.",
			QType:         "A",
			Result:        "93.184.216.34",
			Upstream:      "https://dns.example/dns-query",
			State:         "live",
			StoredAt:      now.Add(-30 * time.Second),
			ExpiresAt:     now.Add(60 * time.Second),
			LazyExpiresAt: now.Add(3600 * time.Second),
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "cache"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "QTYPE", "STATE", "TTL", "UPSTREAM", "example.com.", "live", "https://dns.example/dns-query"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "RESULT") {
		t.Fatalf("default output should not include RESULT column:\n%s", text)
	}
}

func TestDNSCacheCommandWideShowsResult(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]cacheEntry{{
			Name:      "wide.example.",
			QType:     "AAAA",
			Result:    "2606:2800::1",
			Upstream:  "https://dns.example/dns-query",
			State:     "stale",
			StoredAt:  now.Add(-time.Hour),
			ExpiresAt: now.Add(-time.Minute),
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "cache", "-o", "wide"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"RESULT", "UPSTREAM", "EXPIRES", "2606:2800::1", "https://dns.example/dns-query", "stale", "expired"} {
		if !strings.Contains(text, want) {
			t.Fatalf("wide output missing %q:\n%s", want, text)
		}
	}
}

func TestDNSCacheFlushCommand(t *testing.T) {
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.URL.Path != "/api/dns/cache" {
			t.Fatalf("path = %q, want /api/dns/cache", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "cache", "flush"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", gotMethod)
	}
	if !strings.Contains(out.String(), "flushed") {
		t.Fatalf("output = %q, want flushed confirmation", out.String())
	}
}

func TestDNSTraceStreamsLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/queries/stream" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter is not a Flusher")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		enc := json.NewEncoder(w)
		entries := []queryLog{
			{Time: time.Date(2026, 4, 28, 15, 35, 12, 0, time.UTC), Domain: "google.com", QType: "A", Decision: "relay", Result: "198.18.0.5", Latency: 12, Rule: "gfw-list"},
			{Time: time.Date(2026, 4, 28, 15, 35, 13, 0, time.UTC), Domain: "example.com", QType: "AAAA", Decision: "direct", Result: "2606:2800::1", Latency: 8, Cached: true},
		}
		for _, e := range entries {
			if err := enc.Encode(e); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out bytes.Buffer
	cfg := commandConfig{
		out:     &out,
		errOut:  &bytes.Buffer{},
		client:  server.Client(),
		addr:    server.URL,
		timeout: 2 * time.Second,
	}
	if err := traceQueries(ctx, cfg, &out, ""); err != nil {
		t.Fatalf("traceQueries() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"TIME", "DECISION", "google.com", "RELAY", "198.18.0.5", "12ms", "gfw-list", "DIRECT*"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestDNSTraceJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		_, _ = w.Write([]byte(`{"domain":"a.test","qtype":"A","decision":"reject"}` + "\n"))
		flusher.Flush()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out bytes.Buffer
	cfg := commandConfig{
		out:     &out,
		errOut:  &bytes.Buffer{},
		client:  server.Client(),
		addr:    server.URL,
		timeout: 2 * time.Second,
	}
	if err := traceQueries(ctx, cfg, &out, "json"); err != nil {
		t.Fatalf("traceQueries() error = %v", err)
	}
	if strings.Contains(out.String(), "TIME") {
		t.Fatalf("json mode should not print header, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"domain":"a.test"`) {
		t.Fatalf("json passthrough missing domain field:\n%s", out.String())
	}
}

func TestDNSTraceCancelsOnContextDone(t *testing.T) {
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		<-r.Context().Done()
		close(released)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	cfg := commandConfig{
		out:     &bytes.Buffer{},
		errOut:  &bytes.Buffer{},
		client:  server.Client(),
		addr:    server.URL,
		timeout: time.Second,
	}
	if err := traceQueries(ctx, cfg, &bytes.Buffer{}, ""); err != nil {
		t.Fatalf("traceQueries() error = %v, want nil on context cancel", err)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("server context never released after client cancel")
	}
}

func TestAPIURLDefaultsHTTP(t *testing.T) {
	got, err := apiURL("127.0.0.1:28854", "/api/dns/upstreams")
	if err != nil {
		t.Fatalf("apiURL() error = %v", err)
	}
	if got != "http://127.0.0.1:28854/api/dns/upstreams" {
		t.Fatalf("apiURL() = %q", got)
	}
}

func TestAPIURLPreservesEscapedPathSegments(t *testing.T) {
	got, err := apiURL("127.0.0.1:28854", "/api/relaygroups/Gugu%20Airport")
	if err != nil {
		t.Fatalf("apiURL() error = %v", err)
	}
	if got != "http://127.0.0.1:28854/api/relaygroups/Gugu%20Airport" {
		t.Fatalf("apiURL() = %q", got)
	}
}

func TestAPIURLPreservesQuery(t *testing.T) {
	got, err := apiURL("127.0.0.1:28854", "/api/relays/hk-1/check?group=main")
	if err != nil {
		t.Fatalf("apiURL() error = %v", err)
	}
	if got != "http://127.0.0.1:28854/api/relays/hk-1/check?group=main" {
		t.Fatalf("apiURL() = %q", got)
	}
}

func TestWriteUpstreamTableFormatsEmptyValues(t *testing.T) {
	var out bytes.Buffer
	err := writeUpstreams(&out, []upstreamStats{{
		URL:            "8.8.8.8:53",
		Queries:        0,
		AverageLatency: 0,
		LastLatency:    0,
	}}, listFlags{})
	if err != nil {
		t.Fatalf("writeUpstreamTable() error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "8.8.8.8:53") || !strings.Contains(text, "-") {
		t.Fatalf("unexpected table output:\n%s", text)
	}
}

func TestDNSFakeIPsCommand(t *testing.T) {
	expiresAt := time.Now().Add(5 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/fakeips" {
			t.Fatalf("path = %q, want /api/dns/fakeips", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]fakeIPEntry{{
			FakeIP:    "198.18.0.4",
			Domain:    "example.com",
			State:     "active",
			ExpiresAt: expiresAt,
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "fakeips"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"FAKE-IP", "DOMAIN", "STATE", "TIME-LEFT", "198.18.0.4", "example.com", "active"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("fakeips output missing %q:\n%s", want, out.String())
		}
	}
}

func TestFormatTimestamp(t *testing.T) {
	got := formatTimestamp(time.Date(2026, 4, 28, 12, 34, 56, 0, time.UTC).Format(time.RFC3339Nano))
	if !strings.Contains(got, "2026-04-28") {
		t.Fatalf("formatTimestamp() = %q", got)
	}
}

func TestRelayGroupsCommand(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relaygroups" {
			t.Fatalf("path = %q, want /api/relaygroups", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]relayGroupStatus{{
			Name:                     "main",
			Type:                     "remote",
			RelayCount:               3,
			Selected:                 true,
			Select:                   "auto",
			CurrentRelay:             "hk-1",
			CurrentStatus:            "healthy",
			CurrentLatency:           42,
			CurrentTCPConnectLatency: 11,
			RemoteAddress:            "https://provider.example/sub.yaml",
			LastRefreshedAt:          now,
			NextRefreshAt:            now.Add(time.Hour),
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "RELAYS", "SELECTED", "TC_LATENCY", "TTL", "main", "remote", "hk-1", "42ms", "11ms"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, " -\n") {
		t.Fatalf("remote relay group TTL was not rendered:\n%s", text)
	}
}

func TestRelaysCommandShowsTCPLatencyAndNoModeColumn(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relays" {
			t.Fatalf("path = %q, want /api/relays", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]relayHealth{{
			Name:              "main / hk-1",
			Group:             "main",
			Type:              "ss",
			Addr:              "relay.example:443",
			Status:            "healthy",
			Latency:           42,
			TCPConnectLatency: 7,
			LastCheckedAt:     now,
			LastRefreshedAt:   now,
			Selected:          true,
			GroupMode:         "manual",
			GroupSourceURL:    "https://provider.example/sub.yaml",
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "-o", "wide"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"GROUP", "RELAY", "LATENCY", "TC_LATENCY", "main", "hk-1", "42ms", "7ms"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "MODE") || strings.Contains(text, "manual") {
		t.Fatalf("relay table should not show relay group mode:\n%s", text)
	}
}

func TestRelayGroupCreateProviderFileIncludesResolvers(t *testing.T) {
	dir := t.TempDir()
	providerPath := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(providerPath, []byte(`
proxies:
  - name: hk-1
    type: ss
    server: relay.example
    port: 443
    cipher: aes-128-gcm
    password: secret
resolvers:
  - url: https://dns.example/dns-query
    bootstrap: 1.1.1.1
`), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}

	var gotMethod string
	var got relayGroupConfig
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.URL.Path != "/api/relaygroups" {
			t.Fatalf("path = %q, want /api/relaygroups", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups", "create", "main", "--provider-file", providerPath, "--select", "manual"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if got.Name != "main" || got.Type != "inline" || got.Select != "manual" {
		t.Fatalf("group = %+v", got)
	}
	if len(got.Proxies) != 1 || got.Proxies[0]["name"] != "hk-1" {
		t.Fatalf("proxies = %#v", got.Proxies)
	}
	if len(got.RelayDomainResolver) != 1 || got.RelayDomainResolver[0].URL != "https://dns.example/dns-query" {
		t.Fatalf("resolvers = %#v", got.RelayDomainResolver)
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRelayProviderFileRenamesDuplicateProxyNames(t *testing.T) {
	dir := t.TempDir()
	providerPath := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(providerPath, []byte(`
proxies:
  - name: hk-1
    type: ss
  - name: hk-1
    type: vmess
  - name: hk-1-1
    type: direct
  - name: hk-1
    type: trojan
`), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}

	provider, err := readRelayProvider(providerPath)
	if err != nil {
		t.Fatalf("readRelayProvider() error = %v", err)
	}
	var names []string
	for _, proxy := range provider.Proxies {
		name, _ := proxy["name"].(string)
		names = append(names, name)
	}
	want := []string{"hk-1", "hk-1-1", "hk-1-1-1", "hk-1-2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestRelayGroupGetDescribe(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relaygroups/main" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(relayGroupStatus{
			Name:            "main",
			Type:            "inline",
			RelayCount:      1,
			Select:          "auto",
			CurrentRelay:    "hk-1",
			CurrentStatus:   "untested",
			CurrentLatency:  0,
			LastRefreshedAt: now,
			NextRefreshAt:   now.Add(time.Hour),
			Config: relayGroupConfig{
				Type: "inline",
				Name: "main",
				Keep: "HK",
				RelayDomainResolver: []relayUpstream{{
					URL:       "https://dns.example/dns-query",
					Bootstrap: "1.1.1.1",
				}},
				Proxies: []map[string]any{{"name": "hk-1", "type": "ss", "server": "relay.example"}},
			},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups", "get", "main"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Name:", "main", "Relays:            1 (keep HK)", "Selected:          no (auto)", "Current Relay:", "hk-1 (untested, latency -, tc latency -)", "Last Refreshed:", "Resolvers:", "https://dns.example/dns-query"} {
		if !strings.Contains(text, want) {
			t.Fatalf("describe output missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"TTL:", "Next Check:", "Next Refresh:", "Spec:"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("describe output should not show %q:\n%s", unwanted, text)
		}
	}
}

func TestRelayGroupRefreshAllCommand(t *testing.T) {
	var gotPath, gotMethod, gotAll string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAll = r.URL.Query().Get("all")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups", "refresh", "--all"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/relaygroups/refresh" || gotAll != "true" {
		t.Fatalf("method/path/all = %s %s %s", gotMethod, gotPath, gotAll)
	}
}

func TestRelayGroupCheckCommand(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups", "check", "main"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/relaygroups/main/check" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
}

func TestRelaysCreateAcceptsProviderFile(t *testing.T) {
	dir := t.TempDir()
	providerPath := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(providerPath, []byte(`
proxies:
  - name: hk-1
    type: ss
  - name: jp-1
    type: vmess
`), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}

	var gotPath, gotMethod string
	var got relaysRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "create", "main", "--file", providerPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/relaygroups/main/relays" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if len(got.Relays) != 2 || got.Relays[1]["name"] != "jp-1" {
		t.Fatalf("relays = %#v", got.Relays)
	}
	if !strings.Contains(out.String(), "2 relay(s) created") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRelayGetDescribeShowsSpec(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/relaygroups/main/relays/hk-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(relayHealth{
			Name:      "main / hk-1",
			Group:     "main",
			Type:      "ss",
			Addr:      "relay.example:443",
			Status:    "healthy",
			Latency:   31,
			Selected:  false,
			GroupMode: "auto",
			History: []relayHistory{{
				Time:              time.Date(2026, 4, 30, 1, 2, 3, 0, time.UTC),
				Status:            "healthy",
				Latency:           31,
				TCPConnectLatency: 12,
			}},
			Spec: map[string]any{"name": "hk-1", "type": "ss", "server": "relay.example"},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "get", "main", "hk-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"Name:", "main / hk-1", "healthy (latency 31ms, tc latency -)", "Selected:        no (auto)", "History:", "healthy  latency 31ms  tc latency 12ms", "Spec:", "server"} {
		if !strings.Contains(text, want) {
			t.Fatalf("relay describe output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "TTL:") {
		t.Fatalf("relay describe output should not show TTL:\n%s", text)
	}
}

func TestRelaySelectCommandUsesRelayOnly(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/relays" {
			_ = json.NewEncoder(w).Encode([]relayHealth{{
				Name:  "main / hk-1",
				Group: "main",
			}})
			return
		}
		gotPath = r.URL.Path
		gotMethod = r.Method
		if r.URL.Path != "/api/relays/hk-1/select" {
			t.Fatalf("path = %q, want /api/relays/hk-1/select", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"relay":  "main / hk-1",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "select", "hk-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/relays/hk-1/select" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(out.String(), `relay "main / hk-1" selected`) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRelayCheckCommandRequiresGroupForAmbiguousName(t *testing.T) {
	var posted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/relays" {
			_ = json.NewEncoder(w).Encode([]relayHealth{
				{Name: "a / hk-1", Group: "a"},
				{Name: "b / hk-1", Group: "b"},
			})
			return
		}
		posted = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cmd := newRootCommand(commandConfig{
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relays", "check", "hk-1"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "supply --group") {
		t.Fatalf("Execute() error = %v, want ambiguity guidance", err)
	}
	if posted {
		t.Fatal("ambiguous relay check posted despite missing --group")
	}
}

func TestRelayGroupSelectCommand(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if r.URL.Path != "/api/relaygroups/main/select" {
			t.Fatalf("path = %q, want /api/relaygroups/main/select", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"group":  "main",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{
		out:    &out,
		errOut: &bytes.Buffer{},
		client: server.Client(),
	})
	cmd.SetArgs([]string{"--addr", server.URL, "relaygroups", "select", "main"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/relaygroups/main/select" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(out.String(), `relay group "main" selected`) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDNSRulesListCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/rules" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]ruleStats{
			{Decision: "reject", Index: 0, Source: "domain:ads.example", Type: "inline", Count: 1, Hits: 2},
			{Decision: "relay", Index: 1, Source: "https://example.com/list.txt", Type: "asset", Count: 120, Hits: 3},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "rules"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"DECISION", "INDEX", "COUNT", "HITS", "SOURCE", "domain:ads.example", "https://example.com/list.txt"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "10.0.0.0/8") {
		t.Fatalf("dns rules should only show domain rules:\n%s", out.String())
	}
}

func TestDNSRulesCreateCommand(t *testing.T) {
	var gotMethod string
	var gotBody rulePayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.URL.Path != "/api/dns/rules" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "rules", "create", "reject", "domain:ads.example", "--index", "2"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotBody.Decision != "reject" || gotBody.Source != "domain:ads.example" {
		t.Fatalf("body = %+v", gotBody)
	}
	if gotBody.Index == nil || *gotBody.Index != 2 {
		t.Fatalf("index = %+v", gotBody.Index)
	}
	if !strings.Contains(out.String(), "created") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDNSRulesSetCommand(t *testing.T) {
	var gotMethod string
	var gotQuery url.Values
	var gotBody rulePayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "rules", "set", "1", "relay", "new.example"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotQuery.Get("index") != "1" {
		t.Fatalf("query = %+v", gotQuery)
	}
	if gotBody.Decision != "relay" || gotBody.Source != "new.example" {
		t.Fatalf("body = %+v", gotBody)
	}
}

func TestDNSRulesDeleteCommand(t *testing.T) {
	var gotQuery url.Values
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "routes", "delete", "0"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotQuery.Get("index") != "0" {
		t.Fatalf("query = %+v", gotQuery)
	}
}

func TestDNSRulesMoveCommand(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotBody rulePayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "rules", "move", "2", "--index", "0"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/api/dns/rules/move" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery.Get("index") != "2" {
		t.Fatalf("query = %+v", gotQuery)
	}
	if gotBody.Index == nil || *gotBody.Index != 0 {
		t.Fatalf("index = %+v", gotBody.Index)
	}
}

func TestDNSRulesMoveRequiresIndex(t *testing.T) {
	cmd := newRootCommand(commandConfig{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, client: http.DefaultClient})
	cmd.SetArgs([]string{"dns", "rules", "move", "1"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--index is required") {
		t.Fatalf("err = %v, want --index required", err)
	}
}

func TestDNSRulesRefreshCommand(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "rules", "refresh", "1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/api/dns/rules/refresh" || gotQuery.Get("index") != "1" {
		t.Fatalf("path/query = %s %+v", gotPath, gotQuery)
	}
}

func TestDNSRoutesListShowsDefaultRelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns/routes" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]ruleStats{
			{Decision: "direct", Index: 0, Source: "10.0.0.0/8", Type: "inline", Count: 1},
			{Decision: "relay", Index: 1, Source: "DEFAULT", Type: "default", Default: true},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "dns", "routes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"10.0.0.0/8", "DEFAULT", "relay"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSessionsListCommand(t *testing.T) {
	established := time.Date(2026, 4, 28, 20, 21, 46, 748000000, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]sessionInfo{{
			ID:          "42",
			Status:      "CLOSED",
			Domain:      "chatgpt.com",
			Destination: "chatgpt.com:443",
			Source:      "198.18.0.1:61583",
			Protocol:    "TCP:443",
			Relay:       "Gugu Airport / MKCLOUD-SH-JP-IX-XTLS",
			Established: established,
			DurationMS:  3000,
			Download:    5017,
			Upload:      2969,
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "sessions"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"ID", "DESTINATION", "chatgpt.com", "198.18.0.1:61583", "TCP:443", "4.9 KB", "2.9 KB", "CLOSED"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("sessions output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSessionsCommandHasNoLimitFlag(t *testing.T) {
	cmd := newRootCommand(commandConfig{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, client: http.DefaultClient})
	cmd.SetArgs([]string{"sessions", "--limit", "25"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --limit") {
		t.Fatalf("err = %v, want unknown limit flag", err)
	}
}

func TestConfigCommandListGetSet(t *testing.T) {
	var setPath, setMethod string
	var setBody configValueRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/config" && r.URL.Query().Get("key") == "":
			_ = json.NewEncoder(w).Encode([]configEntry{{Key: "sessions.history_limit", Value: "1000"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/config":
			_ = json.NewEncoder(w).Encode(configEntry{Key: r.URL.Query().Get("key"), Value: "1000"})
		case r.Method == http.MethodPut && r.URL.Path == "/api/config/sessions.history_limit":
			setPath = r.URL.Path
			setMethod = r.Method
			if err := json.NewDecoder(r.Body).Decode(&setBody); err != nil {
				t.Fatalf("decode set body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(configEntry{Key: "sessions.history_limit", Value: setBody.Value})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "config"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config list error = %v", err)
	}
	if !strings.Contains(out.String(), "sessions.history_limit") {
		t.Fatalf("config list output = %q", out.String())
	}

	out.Reset()
	cmd = newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "config", "get", "sessions.history_limit"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config get error = %v", err)
	}
	if strings.TrimSpace(out.String()) != "1000" {
		t.Fatalf("config get output = %q", out.String())
	}

	out.Reset()
	cmd = newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "config", "set", "sessions.history_limit", "2000"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set error = %v", err)
	}
	if setMethod != http.MethodPut || setPath != "/api/config/sessions.history_limit" || setBody.Value != "2000" {
		t.Fatalf("set request = %s %s %#v", setMethod, setPath, setBody)
	}
	if !strings.Contains(out.String(), "sessions.history_limit=2000") {
		t.Fatalf("config set output = %q", out.String())
	}
}

func TestSessionGetCommand(t *testing.T) {
	established := time.Date(2026, 4, 28, 20, 20, 55, 355000000, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/54" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(sessionInfo{
			ID:          "54",
			Status:      "ACTIVE",
			Domain:      "my.1password.com",
			Destination: "my.1password.com:443",
			Source:      "198.18.0.1:61354",
			Protocol:    "TCP:443",
			Relay:       "Gugu Airport / MKCLOUD-SH-JP-IX-XTLS",
			FakeIP:      "198.18.1.4",
			Established: established,
			DurationMS:  int64((4*time.Hour + 24*time.Minute).Milliseconds()),
			Download:    1740,
			Upload:      2560,
			Trace: []sessionTrace{{
				OffsetMS: 0,
				Message:  "DNS resolved A → 198.18.1.4",
			}, {
				OffsetMS: 115,
				Message:  "Relay connected",
			}},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "sessions", "get", "54"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"Destination:", "my.1password.com:443", "Fake IP:", "198.18.1.4", "Duration:", "4h 24m", "[00:00.115] Relay connected"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("session detail missing %q:\n%s", want, out.String())
		}
	}
}

func TestSessionsTerminateCommands(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Query().Get("all") == "true" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "terminated": 3})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "sessions", "terminate", "54"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("terminate ID error = %v", err)
	}

	out.Reset()
	cmd = newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "sessions", "terminate", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("terminate all error = %v", err)
	}
	if strings.Join(requests, ",") != "DELETE /api/sessions/54,DELETE /api/sessions?all=true" {
		t.Fatalf("requests = %#v", requests)
	}
	if !strings.Contains(out.String(), "3 session(s) terminated") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSystemCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system" {
			t.Fatalf("path = %q, want /api/system", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(systemInfo{
			TUNInterfaceName:    "utun9",
			TUNAddress:          "198.18.0.1/30",
			TUNIPv6Address:      "fdfe:dcba:9876::1/126",
			ExtraTUNRoutesCount: 2,
			SystemDNS: []systemDNSInfo{{
				Name:           "Wi-Fi",
				Current:        []string{"198.18.0.1"},
				OverriddenFrom: []string{"223.5.5.5", "119.29.29.29"},
			}},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "system"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"Interface:", "utun9", "IPv6 Address:", "fdfe:dcba:9876::1/126", "Extra Routes: 2", "Wi-Fi: 198.18.0.1 [overriden from 223.5.5.5, 119.29.29.29]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("system output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSystemRoutesCreateDeleteCommands(t *testing.T) {
	var createBody systemRoutePayload
	var deleteQuery url.Values
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/api/system/routes" {
			t.Fatalf("path = %q, want /api/system/routes", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatalf("decode create: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"ok","route":"1.1.1.0/24"}`))
		case http.MethodDelete:
			deleteQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "system", "routes", "create", "1.1.1.1/24", "--index", "0"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create Execute() error = %v", err)
	}

	out.Reset()
	cmd = newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "system", "routes", "delete", "1.1.1.0/24"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("delete Execute() error = %v", err)
	}

	if strings.Join(requests, ",") != "POST /api/system/routes,DELETE /api/system/routes" {
		t.Fatalf("requests = %#v", requests)
	}
	if createBody.Route != "1.1.1.1/24" || createBody.Index == nil || *createBody.Index != 0 {
		t.Fatalf("create body = %+v", createBody)
	}
	if deleteQuery.Get("route") != "1.1.1.0/24" {
		t.Fatalf("delete query = %+v", deleteQuery)
	}
}

func TestSystemRoutesListCommand(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.Local)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/routes" {
			t.Fatalf("path = %q, want /api/system/routes", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]systemRoute{{
			Index:       0,
			Route:       "https://core.telegram.org/resources/cidr.txt",
			LastUpdated: now,
			NextUpdate:  now.Add(time.Hour),
		}})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "system", "routes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.HasPrefix(out.String(), "ROUTE") {
		t.Fatalf("system routes output should start with ROUTE:\n%s", out.String())
	}
	for _, want := range []string{"LAST-UPDATED", "NEXT-UPDATE", "ROUTE", formatTime(now), formatTime(now.Add(time.Hour)), "https://core.telegram.org/resources/cidr.txt"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("system routes output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "INDEX") {
		t.Fatalf("system routes output should not include index:\n%s", out.String())
	}
}

func TestSystemRoutesRefreshCommand(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody systemRoutePayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newRootCommand(commandConfig{out: &out, errOut: &bytes.Buffer{}, client: server.Client()})
	cmd.SetArgs([]string{"--addr", server.URL, "system", "routes", "refresh", "https://core.telegram.org/resources/cidr.txt"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/system/routes/refresh" {
		t.Fatalf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotBody.Route != "https://core.telegram.org/resources/cidr.txt" {
		t.Fatalf("body = %+v", gotBody)
	}
	if !strings.Contains(out.String(), `route "https://core.telegram.org/resources/cidr.txt" refreshed`) {
		t.Fatalf("output = %q", out.String())
	}
}
