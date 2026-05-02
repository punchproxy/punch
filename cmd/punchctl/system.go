package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var errCtlSystemRouteNotFound = errors.New("system route not found")

type systemInfo struct {
	TUNInterfaceName    string          `json:"tun_interface_name"`
	TUNAddress          string          `json:"tun_address"`
	TUNIPv6Address      string          `json:"tun_ipv6_address,omitempty"`
	ExtraTUNRoutesCount int             `json:"extra_tun_routes_count"`
	SystemDNS           []systemDNSInfo `json:"system_dns"`
}

type systemRouteDetail struct {
	Index       int       `json:"index"`
	Route       string    `json:"route"`
	Type        string    `json:"type"`
	Prefixes    []string  `json:"prefixes"`
	Applied     bool      `json:"applied"`
	LastUpdated time.Time `json:"last_updated,omitempty"`
	NextUpdate  time.Time `json:"next_update,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type systemDNSInfo struct {
	Name           string   `json:"name"`
	Current        []string `json:"current"`
	OverriddenFrom []string `json:"overridden_from,omitempty"`
}

type systemRoute struct {
	Index       int       `json:"index" yaml:"index"`
	Route       string    `json:"route" yaml:"route"`
	LastUpdated time.Time `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	NextUpdate  time.Time `json:"next_update,omitempty" yaml:"next_update,omitempty"`
}

type systemRouteRow struct {
	Route       string `json:"route" yaml:"route"`
	LastUpdated string `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	NextUpdate  string `json:"next_update,omitempty" yaml:"next_update,omitempty"`
}

type systemRoutePayload struct {
	Route string `json:"route"`
	Index *int   `json:"index,omitempty"`
}

func newSystemCommand(cfg *commandConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "Show system network configuration",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			info, err := fetchSystem(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeSystem(c.OutOrStdout(), info)
		},
	}
	cmd.AddCommand(newSystemRoutesCommand(cfg))
	return cmd
}

func fetchSystem(ctx context.Context, cfg commandConfig) (systemInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/system")
	if err != nil {
		return systemInfo{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return systemInfo{}, err
	}
	defer resp.Body.Close()
	var info systemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return systemInfo{}, fmt.Errorf("decode system: %w", err)
	}
	return info, nil
}

func writeSystem(w io.Writer, info systemInfo) error {
	fmt.Fprintln(w, "TUN:")
	fmt.Fprintf(w, "  Interface:    %s\n", formatOptional(info.TUNInterfaceName))
	fmt.Fprintf(w, "  Address:      %s\n", formatOptional(info.TUNAddress))
	if strings.TrimSpace(info.TUNIPv6Address) != "" {
		fmt.Fprintf(w, "  IPv6 Address: %s\n", info.TUNIPv6Address)
	}
	fmt.Fprintf(w, "  Extra Routes: %d\n", info.ExtraTUNRoutesCount)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "System DNS:")
	if len(info.SystemDNS) == 0 {
		fmt.Fprintln(w, "  -")
		return nil
	}
	for _, dns := range info.SystemDNS {
		current := formatStringList(dns.Current)
		if len(dns.OverriddenFrom) > 0 {
			current += " [overriden from " + formatStringList(dns.OverriddenFrom) + "]"
		}
		fmt.Fprintf(w, "  %s: %s\n", formatOptional(dns.Name), current)
	}
	return nil
}

func formatStringList(values []string) string {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return "-"
	}
	return strings.Join(clean, ", ")
}

func newSystemRoutesCommand(cfg *commandConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "Manage TUN extra routes",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			routes, err := fetchSystemRoutes(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeSystemRoutes(c.OutOrStdout(), routes)
		},
	}
	cmd.AddCommand(newSystemRouteCreateCommand(cfg))
	cmd.AddCommand(newSystemRouteDeleteCommand(cfg))
	cmd.AddCommand(newSystemRouteRefreshCommand(cfg))
	cmd.AddCommand(newSystemRouteGetCommand(cfg))
	return cmd
}

func newSystemRouteGetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get ROUTE",
		Short: "Show resolved CIDRs and apply status for a TUN extra route",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			detail, err := fetchSystemRoute(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errCtlSystemRouteNotFound) {
					return fmt.Errorf("route %q not found", args[0])
				}
				return err
			}
			return writeSystemRouteDetail(c.OutOrStdout(), detail)
		},
	}
}

func fetchSystemRoute(ctx context.Context, cfg commandConfig, route string) (systemRouteDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	reqURL := &url.URL{Path: "/api/system/routes/get"}
	query := reqURL.Query()
	query.Set("route", route)
	reqURL.RawQuery = query.Encode()
	endpoint, err := apiURL(cfg.addr, reqURL.String())
	if err != nil {
		return systemRouteDetail{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return systemRouteDetail{}, errCtlSystemRouteNotFound
		}
		return systemRouteDetail{}, err
	}
	defer resp.Body.Close()
	var detail systemRouteDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return systemRouteDetail{}, fmt.Errorf("decode system route: %w", err)
	}
	return detail, nil
}

func writeSystemRouteDetail(w io.Writer, detail systemRouteDetail) error {
	fmt.Fprintf(w, "Route:    %s\n", detail.Route)
	fmt.Fprintf(w, "Index:    %d\n", detail.Index)
	fmt.Fprintf(w, "Type:     %s\n", formatOptional(detail.Type))
	applied := "no"
	if detail.Applied {
		applied = "yes"
	}
	fmt.Fprintf(w, "Applied:  %s\n", applied)
	if detail.Type == "source" {
		fmt.Fprintf(w, "Updated:  %s\n", formatTime(detail.LastUpdated))
		fmt.Fprintf(w, "Next:     %s\n", formatTime(detail.NextUpdate))
	}
	if detail.Error != "" {
		fmt.Fprintf(w, "Error:    %s\n", detail.Error)
	}
	fmt.Fprintf(w, "Prefixes (%d):\n", len(detail.Prefixes))
	if len(detail.Prefixes) == 0 {
		fmt.Fprintln(w, "  -")
		return nil
	}
	for _, p := range detail.Prefixes {
		fmt.Fprintf(w, "  %s\n", p)
	}
	return nil
}

func newSystemRouteCreateCommand(cfg *commandConfig) *cobra.Command {
	var index int
	cmd := &cobra.Command{
		Use:   "create ROUTE",
		Short: "Create a TUN extra route",
		Long: `Create a TUN extra route.

ROUTE is a CIDR such as "1.1.1.0/24" or a URL/file source containing CIDRs.
Use --index to insert at a specific position; defaults to appending at the end.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			payload := systemRoutePayload{Route: args[0]}
			if c.Flags().Changed("index") {
				idx := index
				payload.Index = &idx
			}
			route, err := createSystemRoute(c.Context(), *cfg, payload)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "route %q created\n", route)
			return nil
		},
	}
	cmd.Flags().IntVar(&index, "index", 0, "position to insert at (0-based)")
	return cmd
}

func newSystemRouteDeleteCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "delete INDEX|ROUTE",
		Short: "Delete a TUN extra route",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			target := args[0]
			if err := deleteSystemRoute(c.Context(), *cfg, target); err != nil {
				if errors.Is(err, errCtlSystemRouteNotFound) {
					return fmt.Errorf("route %q not found", target)
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "route %q deleted\n", target)
			return nil
		},
	}
}

func newSystemRouteRefreshCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh URL",
		Short: "Refresh a remote TUN extra route source",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := refreshSystemRoute(c.Context(), *cfg, args[0]); err != nil {
				if errors.Is(err, errCtlSystemRouteNotFound) {
					return fmt.Errorf("route %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "route %q refreshed\n", args[0])
			return nil
		},
	}
}

func fetchSystemRoutes(ctx context.Context, cfg commandConfig) ([]systemRoute, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/system/routes")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var routes []systemRoute
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		return nil, fmt.Errorf("decode system routes: %w", err)
	}
	return routes, nil
}

func createSystemRoute(ctx context.Context, cfg commandConfig, payload systemRoutePayload) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/system/routes")
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, strings.NewReader(string(body)), http.StatusCreated, http.StatusOK)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Route string `json:"route"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode created route: %w", err)
	}
	if result.Route == "" {
		result.Route = payload.Route
	}
	return result.Route, nil
}

func deleteSystemRoute(ctx context.Context, cfg commandConfig, target string) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	reqURL := &url.URL{Path: "/api/system/routes"}
	query := reqURL.Query()
	if idx, err := strconv.Atoi(target); err == nil && idx >= 0 {
		query.Set("index", strconv.Itoa(idx))
	} else {
		query.Set("route", target)
	}
	reqURL.RawQuery = query.Encode()
	endpoint, err := apiURL(cfg.addr, reqURL.String())
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodDelete, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errCtlSystemRouteNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func refreshSystemRoute(ctx context.Context, cfg commandConfig, route string) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/system/routes/refresh")
	if err != nil {
		return err
	}
	body, err := json.Marshal(systemRoutePayload{Route: route})
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, strings.NewReader(string(body)), http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errCtlSystemRouteNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func writeSystemRoutes(w io.Writer, routes []systemRoute) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ROUTE\tLAST-UPDATED\tNEXT-UPDATE")
	for _, route := range systemRouteRows(routes) {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", route.Route, route.LastUpdated, route.NextUpdate)
	}
	return tw.Flush()
}

func systemRouteRows(routes []systemRoute) []systemRouteRow {
	rows := make([]systemRouteRow, 0, len(routes))
	for _, route := range routes {
		rows = append(rows, systemRouteRow{
			Route:       route.Route,
			LastUpdated: formatTime(route.LastUpdated),
			NextUpdate:  formatTime(route.NextUpdate),
		})
	}
	return rows
}
