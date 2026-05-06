package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type statusInfo struct {
	General      statusGeneral      `json:"general"`
	DNS          statusDNS          `json:"dns"`
	Connectivity statusConnectivity `json:"connectivity"`
	Relay        statusRelay        `json:"relay"`
}

type statusGeneral struct {
	Version       string    `json:"version"`
	Architecture  string    `json:"architecture"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	StartedAt     time.Time `json:"started_at"`
	MemoryBytes   uint64    `json:"memory_bytes"`
	Goroutines    int       `json:"goroutines"`
}

type statusDNS struct {
	TotalQueries int64              `json:"total_queries"`
	Relay        statusDecisionStat `json:"relay"`
	Direct       statusDecisionStat `json:"direct"`
	Reject       statusDecisionStat `json:"reject"`
	CacheEntries int                `json:"cache_entries"`
	CacheHits    int64              `json:"cache_hits"`
}

type statusDecisionStat struct {
	Requests   int64  `json:"requests"`
	LastDomain string `json:"last_domain"`
}

type statusConnectivity struct {
	CheckIntervalMS int64                   `json:"check_interval_ms"`
	Domestic        statusConnectivityCheck `json:"domestic"`
	Outside         statusConnectivityCheck `json:"outside"`
}

type statusConnectivityCheck struct {
	URL                 string               `json:"url"`
	Status              string               `json:"status"`
	LatencyMS           int64                `json:"latency_ms"`
	TCPConnectLatencyMS int64                `json:"tcp_connect_latency_ms"`
	LastCheckedAt       time.Time            `json:"last_checked_at"`
	History             []statusHealthRecord `json:"history"`
	Error               string               `json:"error"`
}

type statusHealthRecord struct {
	Time                time.Time `json:"time"`
	Status              string    `json:"status"`
	LatencyMS           int64     `json:"latency_ms"`
	TCPConnectLatencyMS int64     `json:"tcp_connect_latency_ms"`
}

type statusRelay struct {
	ActiveRelay            string    `json:"active_relay"`
	Status                 string    `json:"status"`
	LatencyMS              int64     `json:"latency_ms"`
	TCPConnectLatencyMS    int64     `json:"tcp_connect_latency_ms"`
	URLTestLatencyMS       int64     `json:"url_test_latency_ms"`
	LastCheckedAt          time.Time `json:"last_checked_at"`
	ActiveSessions         int       `json:"active_sessions"`
	TotalProcessedSessions int64     `json:"total_processed_sessions"`
	UploadBytes            int64     `json:"upload_bytes"`
	DownloadBytes          int64     `json:"download_bytes"`
	UploadBPS              int64     `json:"upload_bps"`
	DownloadBPS            int64     `json:"download_bps"`
	UDP                    statusUDP `json:"udp"`
}

type statusUDP struct {
	PacketsEnqueued int64 `json:"packets_enqueued"`
	PacketsDropped  int64 `json:"packets_dropped"`
	QueueFullDrops  int64 `json:"queue_full_drops"`
	ClosedDrops     int64 `json:"closed_drops"`
	PendingDrops    int64 `json:"pending_drops"`
}

func newStatusCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show runtime status",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			status, err := fetchStatus(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeStatus(c.OutOrStdout(), status)
		},
	}
}

func fetchStatus(ctx context.Context, cfg commandConfig) (statusInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/status")
	if err != nil {
		return statusInfo{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return statusInfo{}, err
	}
	defer resp.Body.Close()
	var status statusInfo
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return statusInfo{}, fmt.Errorf("decode status: %w", err)
	}
	return status, nil
}

func writeStatus(w io.Writer, status statusInfo) error {
	fmt.Fprintln(w, "General:")
	fmt.Fprintf(w, "  Client Version: %s\n", formatOptional(version))
	fmt.Fprintf(w, "  Server Version: %s\n", formatOptional(status.General.Version))
	fmt.Fprintf(w, "  Architecture:   %s\n", formatOptional(status.General.Architecture))
	fmt.Fprintf(w, "  Uptime:         %s\n", formatDurationSeconds(status.General.UptimeSeconds))
	fmt.Fprintf(w, "  Started:        %s\n", formatTime(status.General.StartedAt))
	fmt.Fprintf(w, "  Memory:         %s\n", formatBytes(int64(status.General.MemoryBytes)))
	fmt.Fprintf(w, "  Goroutines:     %d\n", status.General.Goroutines)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "DNS:")
	fmt.Fprintf(w, "  Relay:          %d requests, last %s\n", status.DNS.Relay.Requests, formatOptional(status.DNS.Relay.LastDomain))
	fmt.Fprintf(w, "  Direct:         %d requests, last %s\n", status.DNS.Direct.Requests, formatOptional(status.DNS.Direct.LastDomain))
	fmt.Fprintf(w, "  Reject:         %d requests, last %s\n", status.DNS.Reject.Requests, formatOptional(status.DNS.Reject.LastDomain))
	fmt.Fprintf(w, "  Cache:          %d entries, %d hits\n", status.DNS.CacheEntries, status.DNS.CacheHits)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Health (every %s):\n", formatHealthInterval(status.Connectivity.CheckIntervalMS))
	fmt.Fprintln(w, "  Internet:")
	fmt.Fprintf(w, "    %-12s %s\n", "TCP Connect:", formatHealthMetric(status.Connectivity.Domestic, healthMetricTCPConnect))
	fmt.Fprintf(w, "    %-12s %s\n", "Round Trip:", formatHealthMetric(status.Connectivity.Domestic, healthMetricRoundTrip))
	fmt.Fprintln(w, "  Relayed:")
	fmt.Fprintf(w, "    %-12s %s\n", "TCP Connect:", formatHealthMetric(status.Connectivity.Outside, healthMetricTCPConnect))
	fmt.Fprintf(w, "    %-12s %s\n", "Round Trip:", formatHealthMetric(status.Connectivity.Outside, healthMetricRoundTrip))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Relay:")
	fmt.Fprintf(w, "  Active:         %s\n", formatOptional(status.Relay.ActiveRelay))
	fmt.Fprintf(w, "  Status:         %s\n", formatOptional(status.Relay.Status))
	fmt.Fprintf(w, "  Latency:        %s\n", formatRelayLatencies(status.Relay))
	fmt.Fprintf(w, "  Last Check:     %s\n", formatTime(status.Relay.LastCheckedAt))
	fmt.Fprintf(w, "  Sessions:       %d active, %d total processed\n", status.Relay.ActiveSessions, status.Relay.TotalProcessedSessions)
	fmt.Fprintf(w, "  Download:       %s total, %s/s\n", formatBytes(status.Relay.DownloadBytes), formatBytes(status.Relay.DownloadBPS))
	fmt.Fprintf(w, "  Upload:         %s total, %s/s\n", formatBytes(status.Relay.UploadBytes), formatBytes(status.Relay.UploadBPS))
	fmt.Fprintf(w, "  UDP Packets:    %d enqueued, %d dropped (%d queue full, %d closed, %d pending)\n",
		status.Relay.UDP.PacketsEnqueued,
		status.Relay.UDP.PacketsDropped,
		status.Relay.UDP.QueueFullDrops,
		status.Relay.UDP.ClosedDrops,
		status.Relay.UDP.PendingDrops,
	)
	return nil
}

func formatRelayLatencies(status statusRelay) string {
	latency := formatLatency(status.LatencyMS)
	tcp := formatLatency(status.TCPConnectLatencyMS)
	url := formatLatency(status.URLTestLatencyMS)
	if tcp == "-" && url == "-" {
		return latency
	}
	return fmt.Sprintf("%s (tcp %s, url %s)", latency, tcp, url)
}

type healthMetric int

const (
	healthMetricTCPConnect healthMetric = iota
	healthMetricRoundTrip
	healthChartBars = 20
)

const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiGray   = "\x1b[90m"
)

func formatHealthInterval(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

func formatHealthMetric(check statusConnectivityCheck, metric healthMetric) string {
	records := healthRecords(check)
	latency := check.LatencyMS
	if metric == healthMetricTCPConnect {
		latency = check.TCPConnectLatencyMS
	}
	return fmt.Sprintf("%s (last %s @ %s)", formatHealthChart(records, metric), formatHealthLatency(latency), formatClockTime(check.LastCheckedAt))
}

func healthRecords(check statusConnectivityCheck) []statusHealthRecord {
	records := append([]statusHealthRecord(nil), check.History...)
	if !check.LastCheckedAt.IsZero() && shouldAppendCurrentHealthRecord(records, check) {
		records = append(records, statusHealthRecord{
			Time:                check.LastCheckedAt,
			Status:              check.Status,
			LatencyMS:           check.LatencyMS,
			TCPConnectLatencyMS: check.TCPConnectLatencyMS,
		})
	}
	if len(records) > healthChartBars {
		records = records[len(records)-healthChartBars:]
	}
	return records
}

func shouldAppendCurrentHealthRecord(records []statusHealthRecord, check statusConnectivityCheck) bool {
	if len(records) == 0 {
		return true
	}
	last := records[len(records)-1]
	return !last.Time.Equal(check.LastCheckedAt) ||
		last.Status != check.Status ||
		last.LatencyMS != check.LatencyMS ||
		last.TCPConnectLatencyMS != check.TCPConnectLatencyMS
}

func formatHealthChart(records []statusHealthRecord, metric healthMetric) string {
	var b strings.Builder
	if len(records) > healthChartBars {
		records = records[len(records)-healthChartBars:]
	}
	for i := len(records); i < healthChartBars; i++ {
		b.WriteString(colorHealthBlock("", 0, metric))
	}
	for _, record := range records {
		b.WriteString(colorHealthBlock(record.Status, healthRecordLatency(record, metric), metric))
	}
	return b.String()
}

func healthRecordLatency(record statusHealthRecord, metric healthMetric) int64 {
	if metric == healthMetricTCPConnect {
		return record.TCPConnectLatencyMS
	}
	return record.LatencyMS
}

func colorHealthBlock(status string, latency int64, metric healthMetric) string {
	if status == "down" && (metric == healthMetricRoundTrip || latency <= 0) {
		return ansiRed + healthLatencyBlock(latency) + ansiReset
	}
	if status == "degraded" && metric == healthMetricRoundTrip {
		return ansiYellow + healthLatencyBlock(latency) + ansiReset
	}
	color := colorHealthLatency(latency)
	return color + healthLatencyBlock(latency) + ansiReset
}

func healthLatencyBlock(latency int64) string {
	switch {
	case latency <= 0:
		return "█"
	case latency <= 25:
		return "▁"
	case latency <= 50:
		return "▂"
	case latency <= 100:
		return "▃"
	case latency <= 150:
		return "▄"
	case latency <= 250:
		return "▅"
	case latency <= 350:
		return "▆"
	case latency <= 500:
		return "▇"
	default:
		return "█"
	}
}

func colorHealthLatency(latency int64) string {
	if latency <= 0 {
		return ansiGray
	}
	if latency > 500 {
		return ansiYellow
	}
	return ansiGreen
}

func formatHealthLatency(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d ms", ms)
}

func formatClockTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("15:04:05")
}

func formatDurationSeconds(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}
