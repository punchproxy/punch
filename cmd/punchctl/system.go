package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

type systemInfo struct {
	TUNInterfaceName string          `json:"tun_interface_name"`
	TUNAddress       string          `json:"tun_address"`
	ExtraTUNRoutes   []string        `json:"extra_tun_routes"`
	SystemDNS        []systemDNSInfo `json:"system_dns"`
}

type systemDNSInfo struct {
	Name           string   `json:"name"`
	Current        []string `json:"current"`
	OverriddenFrom []string `json:"overridden_from,omitempty"`
}

func newSystemCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
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
	fmt.Fprintf(w, "  Extra Routes: %s\n", formatStringList(info.ExtraTUNRoutes))
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
