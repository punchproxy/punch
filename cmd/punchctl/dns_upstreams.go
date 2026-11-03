package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

// errUpstreamNotFound surfaces a missing upstream so command handlers can
// produce a friendly "upstream %q not found" error message.
var errUpstreamNotFound = errors.New("upstream not found")

type upstreamStats struct {
	URL               string   `json:"url"`
	Bootstrap         string   `json:"bootstrap,omitempty"`
	Queries           int64    `json:"queries"`
	Errors            int64    `json:"errors"`
	AverageLatency    int64    `json:"average_latency_ms"`
	LastLatency       int64    `json:"last_latency_ms"`
	LastQueriedAt     string   `json:"last_queried_at,omitempty"`
	LastQueriedDomain string   `json:"last_queried_domain,omitempty"`
	Domains           []string `json:"domains,omitempty"`
	Disabled          bool     `json:"disabled"`
}

type upstreamRow struct {
	URL               string    `json:"url" yaml:"url"`
	Queries           int64     `json:"queries" yaml:"queries"`
	Errors            int64     `json:"errors" yaml:"errors"`
	AverageLatency    latencyMS `json:"average_latency_ms" yaml:"average_latency_ms"`
	LastLatency       latencyMS `json:"last_latency_ms" yaml:"last_latency_ms"`
	Bootstrap         string    `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	LastQueriedAt     string    `json:"last_queried_at,omitempty" yaml:"last_queried_at,omitempty"`
	LastQueriedDomain string    `json:"last_queried_domain,omitempty" yaml:"last_queried_domain,omitempty"`
	Domains           string    `json:"domains,omitempty" yaml:"domains,omitempty"`
	Disabled          bool      `json:"disabled" yaml:"disabled"`
}

type createUpstreamRequest struct {
	URL       string   `json:"url"`
	Bootstrap string   `json:"bootstrap"`
	Domains   []string `json:"domains"`
	Disabled  bool     `json:"disabled"`
}

// updateUpstreamRequest carries flag-level intent: nil pointers mean
// "leave the existing value alone." Resolved against the server's current
// state inside updateUpstream.
type updateUpstreamRequest struct {
	URL       string
	Bootstrap *string
	Domains   *[]string
	Disabled  *bool
}

func newUpstreamsCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "upstreams",
		Short: "Manage DNS upstreams",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			upstreams, err := fetchUpstreams(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeUpstreams(c.OutOrStdout(), upstreams, flags)
		},
	}
	addListFlags(cmd, &flags)

	cmd.AddCommand(newUpstreamCreateCommand(cfg))
	cmd.AddCommand(newUpstreamSetCommand(cfg))
	cmd.AddCommand(newUpstreamDeleteCommand(cfg))
	return cmd
}

func newUpstreamCreateCommand(cfg *commandConfig) *cobra.Command {
	var bootstrap, domains string
	var disabled bool
	cmd := &cobra.Command{
		Use:   "create URL",
		Short: "Create a DNS upstream",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := createUpstream(c.Context(), *cfg, createUpstreamRequest{
				URL:       args[0],
				Bootstrap: bootstrap,
				Domains:   splitDomainMatchers(domains),
				Disabled:  disabled,
			}); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "upstream %q created\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&bootstrap, "bootstrap", "", "bootstrap DNS server for DoH upstream hostnames")
	cmd.Flags().StringVar(&domains, "domains", "", "comma-separated domain matchers routed to this upstream")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "save the upstream without using it for DNS queries")
	return cmd
}

func newUpstreamSetCommand(cfg *commandConfig) *cobra.Command {
	var bootstrap, domains string
	var disabled bool
	cmd := &cobra.Command{
		Use:   "set URL",
		Short: "Update an existing DNS upstream",
		Long:  "Update bootstrap, domain matchers, or disabled state on an existing DNS upstream. Flags that are not provided keep their current values.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			update := updateUpstreamRequest{URL: args[0]}
			if c.Flags().Changed("bootstrap") {
				b := bootstrap
				update.Bootstrap = &b
			}
			if c.Flags().Changed("domains") {
				d := splitDomainMatchers(domains)
				update.Domains = &d
			}
			if c.Flags().Changed("disabled") {
				update.Disabled = &disabled
			}
			if update.Bootstrap == nil && update.Domains == nil && update.Disabled == nil {
				return fmt.Errorf("nothing to update: provide --bootstrap, --domains, or --disabled")
			}
			if err := updateUpstream(c.Context(), *cfg, update); err != nil {
				if errors.Is(err, errUpstreamNotFound) {
					return fmt.Errorf("upstream %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "upstream %q updated\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&bootstrap, "bootstrap", "", "bootstrap DNS server for DoH upstream hostnames (pass empty string to clear)")
	cmd.Flags().StringVar(&domains, "domains", "", "comma-separated domain matchers routed to this upstream (pass empty string to clear)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "disable the upstream (use --disabled=false to enable it)")
	return cmd
}

func newUpstreamDeleteCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "delete URL",
		Short: "Delete a DNS upstream",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := deleteUpstream(c.Context(), *cfg, args[0]); err != nil {
				if errors.Is(err, errUpstreamNotFound) {
					return fmt.Errorf("upstream %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "upstream %q deleted\n", args[0])
			return nil
		},
	}
}

func fetchUpstreams(ctx context.Context, cfg commandConfig) ([]upstreamStats, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/upstreams")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var upstreams []upstreamStats
	if err := json.NewDecoder(resp.Body).Decode(&upstreams); err != nil {
		return nil, fmt.Errorf("decode upstreams: %w", err)
	}
	return upstreams, nil
}

func createUpstream(ctx context.Context, cfg commandConfig, req createUpstreamRequest) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/upstreams")
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, strings.NewReader(string(body)), http.StatusCreated, http.StatusOK)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// updateUpstream merges flag changes with the upstream's current settings.
func updateUpstream(ctx context.Context, cfg commandConfig, update updateUpstreamRequest) error {
	existing, err := fetchUpstreams(ctx, cfg)
	if err != nil {
		return err
	}
	var current *upstreamStats
	for i := range existing {
		if existing[i].URL == update.URL {
			current = &existing[i]
			break
		}
	}
	if current == nil {
		return errUpstreamNotFound
	}

	body := createUpstreamRequest{URL: update.URL, Bootstrap: current.Bootstrap, Domains: current.Domains, Disabled: current.Disabled}
	if update.Bootstrap != nil {
		body.Bootstrap = strings.TrimSpace(*update.Bootstrap)
	}
	if update.Domains != nil {
		body.Domains = *update.Domains
	}
	if body.Domains == nil {
		body.Domains = []string{}
	}
	if update.Disabled != nil {
		body.Disabled = *update.Disabled
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/upstreams")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPut, endpoint, strings.NewReader(string(payload)), http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errUpstreamNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func deleteUpstream(ctx context.Context, cfg commandConfig, upstreamURL string) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/upstreams")
	if err != nil {
		return err
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse upstream delete url: %w", err)
	}
	query := reqURL.Query()
	query.Set("url", upstreamURL)
	reqURL.RawQuery = query.Encode()

	resp, err := doRequest(ctx, cfg, http.MethodDelete, reqURL.String(), nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errUpstreamNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func writeUpstreams(w io.Writer, upstreams []upstreamStats, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "UPSTREAM:{.URL},DISABLED:{.Disabled},QUERIES:{.Queries},ERRORS:{.Errors},AVG-LATENCY:{.AverageLatency}",
		WideColumnSpec:    "UPSTREAM:{.URL},DISABLED:{.Disabled},QUERIES:{.Queries},ERRORS:{.Errors},AVG-LATENCY:{.AverageLatency},LAST-LATENCY:{.LastLatency},BOOTSTRAP:{.Bootstrap},LAST-QUERIED:{.LastQueriedAt},LAST-DOMAIN:{.LastQueriedDomain},DOMAINS:{.Domains}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(upstreamRows(upstreams), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func upstreamRows(upstreams []upstreamStats) []upstreamRow {
	rows := make([]upstreamRow, 0, len(upstreams))
	for _, upstream := range upstreams {
		rows = append(rows, upstreamRow{
			URL:               upstream.URL,
			Disabled:          upstream.Disabled,
			Queries:           upstream.Queries,
			Errors:            upstream.Errors,
			AverageLatency:    latencyMS(upstream.AverageLatency),
			LastLatency:       latencyMS(upstream.LastLatency),
			Bootstrap:         formatOptional(upstream.Bootstrap),
			LastQueriedAt:     formatTimestamp(upstream.LastQueriedAt),
			LastQueriedDomain: formatOptional(upstream.LastQueriedDomain),
			Domains:           formatOptional(strings.Join(upstream.Domains, ",")),
		})
	}
	return rows
}
