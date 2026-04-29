package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// queryLog mirrors internal/dns.QueryLog as emitted by the streaming
// endpoint. Duplicated rather than imported to keep punchctl free of the
// server-side packages.
type queryLog struct {
	Time     time.Time `json:"time"`
	Source   string    `json:"source"`
	Domain   string    `json:"domain"`
	QType    string    `json:"qtype"`
	Decision string    `json:"decision"`
	Result   string    `json:"result"`
	Upstream string    `json:"upstream"`
	Latency  int64     `json:"latency_ms"`
	Rule     string    `json:"rule"`
	Cached   bool      `json:"cached"`
}

func newTraceCommand(cfg *commandConfig) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Stream DNS queries as they are resolved (Ctrl+C to stop)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return traceQueries(ctx, *cfg, c.OutOrStdout(), output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: table (default), json")
	return cmd
}

// traceQueries opens the server's NDJSON stream and prints one line per
// query until the context is cancelled (Ctrl+C) or the connection ends.
func traceQueries(ctx context.Context, cfg commandConfig, w io.Writer, output string) error {
	endpoint, err := apiURL(cfg.addr, "/api/dns/queries/stream")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}

	resp, err := cfg.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("API returned %s: %s", resp.Status, msg)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	if output == "json" {
		for scanner.Scan() {
			if _, err := fmt.Fprintln(w, scanner.Text()); err != nil {
				return err
			}
		}
		return finalScanErr(scanner.Err(), ctx)
	}

	fmt.Fprintln(w, traceHeader())
	for scanner.Scan() {
		var ql queryLog
		if err := json.Unmarshal(scanner.Bytes(), &ql); err != nil {
			continue
		}
		if _, err := fmt.Fprintln(w, formatTraceRow(ql)); err != nil {
			return err
		}
	}
	return finalScanErr(scanner.Err(), ctx)
}

// finalScanErr translates the scanner's terminating error. A cancelled
// context (Ctrl+C) is the expected exit and reports no error; everything
// else propagates so users see real network or decode failures.
func finalScanErr(err error, ctx context.Context) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// traceFormat keeps header and row widths in lockstep — change once, both
// stay aligned. The trailing %s has no width so RULE is free to be long.
const traceFormat = "%-23s %-9s %-6s %-40s %-31s %-7s %s"

func traceHeader() string {
	return fmt.Sprintf(traceFormat, "TIME", "DECISION", "QTYPE", "DOMAIN", "RESULT", "LATENCY", "RULE")
}

func formatTraceRow(ql queryLog) string {
	decision := strings.ToUpper(ql.Decision)
	if decision == "" {
		decision = "-"
	}
	if ql.Cached {
		decision += "*"
	}
	result := ql.Result
	if result == "" {
		result = "-"
	}
	rule := ql.Rule
	if rule == "" {
		rule = "-"
	}
	return fmt.Sprintf(traceFormat,
		ql.Time.Local().Format("2006-01-02 15:04:05.000"),
		decision,
		ql.QType,
		truncate(ql.Domain, 40),
		truncate(result, 31),
		fmt.Sprintf("%dms", ql.Latency),
		rule,
	)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
