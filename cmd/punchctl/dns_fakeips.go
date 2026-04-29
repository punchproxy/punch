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
	"github.com/thediveo/klo"
)

type fakeIPEntry struct {
	FakeIP     string    `json:"fake_ip" yaml:"fake_ip"`
	Domain     string    `json:"domain" yaml:"domain"`
	State      string    `json:"state" yaml:"state"`
	ExpiresAt  time.Time `json:"expires_at" yaml:"expires_at"`
	SessionIDs []string  `json:"session_ids,omitempty" yaml:"session_ids,omitempty"`
}

type fakeIPRow struct {
	FakeIP   string `json:"fake_ip" yaml:"fake_ip"`
	Domain   string `json:"domain" yaml:"domain"`
	State    string `json:"state" yaml:"state"`
	TimeLeft string `json:"time_left" yaml:"time_left"`
	Sessions string `json:"sessions" yaml:"sessions"`
}

func newFakeIPsCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "fakeips",
		Short: "List DNS fake IP entries",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			entries, err := fetchFakeIPs(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeFakeIPs(c.OutOrStdout(), entries, flags)
		},
	}
	addListFlags(cmd, &flags)
	return cmd
}

func fetchFakeIPs(ctx context.Context, cfg commandConfig) ([]fakeIPEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/dns/fakeips")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []fakeIPEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode fake IPs: %w", err)
	}
	return entries, nil
}

func writeFakeIPs(w io.Writer, entries []fakeIPEntry, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "FAKE-IP:{.FakeIP},DOMAIN:{.Domain},STATE:{.State},TIME-LEFT:{.TimeLeft},SESSIONS:{.Sessions}",
		WideColumnSpec:    "FAKE-IP:{.FakeIP},DOMAIN:{.Domain},STATE:{.State},TIME-LEFT:{.TimeLeft},SESSIONS:{.Sessions}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(fakeIPRows(entries), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func fakeIPRows(entries []fakeIPEntry) []fakeIPRow {
	now := time.Now()
	rows := make([]fakeIPRow, 0, len(entries))
	for _, entry := range entries {
		sessions := "-"
		if len(entry.SessionIDs) > 0 {
			sessions = strings.Join(entry.SessionIDs, ",")
		}
		timeLeft := "-"
		if entry.State != "active" {
			timeLeft = formatRemaining(now, entry.ExpiresAt)
		}
		rows = append(rows, fakeIPRow{
			FakeIP:   entry.FakeIP,
			Domain:   entry.Domain,
			State:    formatOptional(entry.State),
			TimeLeft: timeLeft,
			Sessions: sessions,
		})
	}
	return rows
}
