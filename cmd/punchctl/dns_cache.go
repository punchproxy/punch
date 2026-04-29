package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

type cacheEntry struct {
	Name          string    `json:"name"`
	QType         string    `json:"qtype"`
	Result        string    `json:"result"`
	StoredAt      time.Time `json:"stored_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	LazyExpiresAt time.Time `json:"lazy_expires_at"`
	State         string    `json:"state"`
}

type cacheRow struct {
	Name          string `json:"name" yaml:"name"`
	QType         string `json:"qtype" yaml:"qtype"`
	State         string `json:"state" yaml:"state"`
	TTL           string `json:"ttl" yaml:"ttl"`
	Result        string `json:"result,omitempty" yaml:"result,omitempty"`
	StoredAt      string `json:"stored_at,omitempty" yaml:"stored_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	LazyExpiresAt string `json:"lazy_expires_at,omitempty" yaml:"lazy_expires_at,omitempty"`
}

func newCacheCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect the DNS cache",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			entries, err := fetchCache(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeCache(c.OutOrStdout(), entries, flags)
		},
	}
	addListFlags(cmd, &flags)

	cmd.AddCommand(&cobra.Command{
		Use:   "flush",
		Short: "Flush the DNS cache",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := flushCache(c.Context(), *cfg); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "cache flushed")
			return nil
		},
	})
	return cmd
}

func fetchCache(ctx context.Context, cfg commandConfig) ([]cacheEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/cache")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var entries []cacheEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode cache: %w", err)
	}
	return entries, nil
}

func flushCache(ctx context.Context, cfg commandConfig) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/cache")
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodDelete, endpoint, nil, http.StatusOK)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func writeCache(w io.Writer, entries []cacheEntry, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "NAME:{.Name},QTYPE:{.QType},STATE:{.State},TTL:{.TTL}",
		WideColumnSpec:    "NAME:{.Name},QTYPE:{.QType},STATE:{.State},TTL:{.TTL},RESULT:{.Result},STORED:{.StoredAt},EXPIRES:{.ExpiresAt},LAZY-EXPIRES:{.LazyExpiresAt}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(cacheRows(entries), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func cacheRows(entries []cacheEntry) []cacheRow {
	now := time.Now()
	rows := make([]cacheRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, cacheRow{
			Name:          entry.Name,
			QType:         entry.QType,
			State:         entry.State,
			TTL:           formatRemaining(now, entry.ExpiresAt),
			Result:        formatOptional(entry.Result),
			StoredAt:      formatTime(entry.StoredAt),
			ExpiresAt:     formatTime(entry.ExpiresAt),
			LazyExpiresAt: formatTime(entry.LazyExpiresAt),
		})
	}
	return rows
}
