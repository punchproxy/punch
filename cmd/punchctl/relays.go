package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
	"gopkg.in/yaml.v3"
)

var (
	errRelayGroupNotFound = errors.New("relay group not found")
	errRelayNotFound      = errors.New("relay not found")
)

type relayGroupConfig struct {
	Type            string           `json:"type" yaml:"type"`
	Name            string           `json:"name" yaml:"name"`
	URL             string           `json:"url,omitempty" yaml:"url,omitempty"`
	RefreshDuration int              `json:"refresh_duration,omitempty" yaml:"refresh_duration,omitempty"`
	Keep            string           `json:"keep,omitempty" yaml:"keep,omitempty"`
	Remove          string           `json:"remove,omitempty" yaml:"remove,omitempty"`
	Select          string           `json:"select,omitempty" yaml:"select,omitempty"`
	Proxies         []map[string]any `json:"proxies,omitempty" yaml:"proxies,omitempty"`
}

type relayGroupStatus struct {
	Name            string           `json:"name" yaml:"name"`
	Type            string           `json:"type" yaml:"type"`
	RelayCount      int              `json:"relay_count" yaml:"relay_count"`
	Selected        bool             `json:"selected" yaml:"selected"`
	Select          string           `json:"select" yaml:"select"`
	CurrentRelay    string           `json:"current_relay,omitempty" yaml:"current_relay,omitempty"`
	CurrentStatus   string           `json:"current_status,omitempty" yaml:"current_status,omitempty"`
	CurrentLatency  int64            `json:"current_latency_ms,omitempty" yaml:"current_latency_ms,omitempty"`
	RemoteAddress   string           `json:"remote_address,omitempty" yaml:"remote_address,omitempty"`
	CheckInterval   int64            `json:"check_interval,omitempty" yaml:"check_interval,omitempty"`
	LastCheckedAt   time.Time        `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	NextCheckAt     time.Time        `json:"next_check_at,omitempty" yaml:"next_check_at,omitempty"`
	LastRefreshedAt time.Time        `json:"last_refreshed_at,omitempty" yaml:"last_refreshed_at,omitempty"`
	NextRefreshAt   time.Time        `json:"next_refresh_at,omitempty" yaml:"next_refresh_at,omitempty"`
	RefreshInterval int64            `json:"refresh_interval,omitempty" yaml:"refresh_interval,omitempty"`
	Error           string           `json:"error,omitempty" yaml:"error,omitempty"`
	Config          relayGroupConfig `json:"config" yaml:"config"`
}

type relayHealth struct {
	Name            string         `json:"name" yaml:"name"`
	Group           string         `json:"group" yaml:"group"`
	Type            string         `json:"type" yaml:"type"`
	Addr            string         `json:"addr" yaml:"addr"`
	Status          string         `json:"status" yaml:"status"`
	Latency         int64          `json:"latency_ms" yaml:"latency_ms"`
	CheckInterval   int64          `json:"check_interval,omitempty" yaml:"check_interval,omitempty"`
	LastCheckedAt   time.Time      `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	LastRefreshedAt time.Time      `json:"last_refreshed_at,omitempty" yaml:"last_refreshed_at,omitempty"`
	NextRefreshAt   time.Time      `json:"next_refresh_at,omitempty" yaml:"next_refresh_at,omitempty"`
	RefreshInterval int64          `json:"refresh_interval,omitempty" yaml:"refresh_interval,omitempty"`
	Selected        bool           `json:"selected" yaml:"selected"`
	GroupMode       string         `json:"group_mode,omitempty" yaml:"group_mode,omitempty"`
	GroupSourceURL  string         `json:"group_source_url,omitempty" yaml:"group_source_url,omitempty"`
	Error           string         `json:"error,omitempty" yaml:"error,omitempty"`
	Spec            map[string]any `json:"spec,omitempty" yaml:"spec,omitempty"`
	History         []relayHistory `json:"history,omitempty" yaml:"history,omitempty"`
}

type relayHistory struct {
	Time    time.Time `json:"time" yaml:"time"`
	Status  string    `json:"status" yaml:"status"`
	Latency int64     `json:"latency_ms,omitempty" yaml:"latency_ms,omitempty"`
}

type relayGroupRow struct {
	Name            string    `json:"name" yaml:"name"`
	Type            string    `json:"type" yaml:"type"`
	Relays          int       `json:"relays" yaml:"relays"`
	Select          string    `json:"select" yaml:"select"`
	Selected        string    `json:"selected" yaml:"selected"`
	Relay           string    `json:"relay" yaml:"relay"`
	Status          string    `json:"status" yaml:"status"`
	Latency         latencyMS `json:"latency_ms" yaml:"latency_ms"`
	TTL             string    `json:"ttl" yaml:"ttl"`
	Remote          string    `json:"remote,omitempty" yaml:"remote,omitempty"`
	LastCheckedAt   string    `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	NextCheckAt     string    `json:"next_check_at,omitempty" yaml:"next_check_at,omitempty"`
	LastRefreshedAt string    `json:"last_refreshed_at,omitempty" yaml:"last_refreshed_at,omitempty"`
	NextRefreshAt   string    `json:"next_refresh_at,omitempty" yaml:"next_refresh_at,omitempty"`
	Error           string    `json:"error,omitempty" yaml:"error,omitempty"`
}

type relayRow struct {
	Group           string    `json:"group" yaml:"group"`
	Relay           string    `json:"relay" yaml:"relay"`
	Type            string    `json:"type" yaml:"type"`
	Status          string    `json:"status" yaml:"status"`
	Latency         latencyMS `json:"latency_ms" yaml:"latency_ms"`
	Selected        string    `json:"selected" yaml:"selected"`
	Addr            string    `json:"addr,omitempty" yaml:"addr,omitempty"`
	Remote          string    `json:"remote,omitempty" yaml:"remote,omitempty"`
	LastCheckedAt   string    `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	LastRefreshedAt string    `json:"last_refreshed_at,omitempty" yaml:"last_refreshed_at,omitempty"`
	Error           string    `json:"error,omitempty" yaml:"error,omitempty"`
}

type relayProviderFile struct {
	Proxies []map[string]any `yaml:"proxies"`
}

type relaysRequest struct {
	Relays []map[string]any `json:"relays"`
}

func newRelayGroupsCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "relaygroups",
		Short: "Manage relay groups",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			groups, err := fetchRelayGroups(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeRelayGroups(c.OutOrStdout(), groups, flags)
		},
	}
	addListFlags(cmd, &flags)
	cmd.AddCommand(newRelayGroupGetCommand(cfg))
	cmd.AddCommand(newRelayGroupCreateCommand(cfg))
	cmd.AddCommand(newRelayGroupSetCommand(cfg))
	cmd.AddCommand(newRelayGroupSelectCommand(cfg))
	cmd.AddCommand(newRelayGroupRefreshCommand(cfg))
	cmd.AddCommand(newRelayGroupCheckCommand(cfg))
	cmd.AddCommand(newRelayGroupDeleteCommand(cfg))
	return cmd
}

func newRelayGroupGetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get NAME",
		Short: "Show relay group details",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			group, err := fetchRelayGroup(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errRelayGroupNotFound) {
					return fmt.Errorf("relay group %q not found", args[0])
				}
				return err
			}
			return writeRelayGroupDescribe(c.OutOrStdout(), group)
		},
	}
}

func newRelayGroupCreateCommand(cfg *commandConfig) *cobra.Command {
	var providerFile, remoteURL, selectMode, keep, remove string
	var refresh int
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a relay group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			group, err := buildRelayGroupFromFlags(args[0], providerFile, remoteURL, selectMode, keep, remove, refresh)
			if err != nil {
				return err
			}
			if err := createRelayGroup(c.Context(), *cfg, group); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay group %q created\n", args[0])
			return nil
		},
	}
	addRelayGroupFlags(cmd, &providerFile, &remoteURL, &selectMode, &keep, &remove, &refresh)
	return cmd
}

func newRelayGroupSetCommand(cfg *commandConfig) *cobra.Command {
	var providerFile, remoteURL, selectMode, keep, remove string
	var refresh int
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Update a relay group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			current, err := fetchRelayGroup(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errRelayGroupNotFound) {
					return fmt.Errorf("relay group %q not found", args[0])
				}
				return err
			}
			group := current.Config
			changed := false
			if c.Flags().Changed("url") {
				group.Type = "remote"
				group.URL = strings.TrimSpace(remoteURL)
				group.Proxies = nil
				changed = true
			}
			if c.Flags().Changed("provider-file") {
				provider, err := readRelayProvider(providerFile)
				if err != nil {
					return err
				}
				group.Type = "inline"
				group.URL = ""
				group.Proxies = provider.Proxies
				changed = true
			}
			if c.Flags().Changed("select") {
				group.Select = selectMode
				changed = true
			}
			if c.Flags().Changed("keep") {
				group.Keep = keep
				changed = true
			}
			if c.Flags().Changed("remove") {
				group.Remove = remove
				changed = true
			}
			if c.Flags().Changed("refresh-duration") {
				group.RefreshDuration = refresh
				changed = true
			}
			if !changed {
				return fmt.Errorf("nothing to update: provide --url, --provider-file, --select, --keep, --remove, and/or --refresh-duration")
			}
			if err := updateRelayGroup(c.Context(), *cfg, args[0], group); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay group %q updated\n", args[0])
			return nil
		},
	}
	addRelayGroupFlags(cmd, &providerFile, &remoteURL, &selectMode, &keep, &remove, &refresh)
	return cmd
}

func newRelayGroupDeleteCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete a relay group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := deleteRelayGroup(c.Context(), *cfg, args[0]); err != nil {
				if errors.Is(err, errRelayGroupNotFound) {
					return fmt.Errorf("relay group %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay group %q deleted\n", args[0])
			return nil
		},
	}
}

func newRelayGroupSelectCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "select NAME",
		Short: "Select a relay group when group selection is manual",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			selected, err := selectRelayGroupByName(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errRelayGroupNotFound) {
					return fmt.Errorf("relay group %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay group %q selected\n", selected)
			return nil
		},
	}
}

func newRelayGroupRefreshCommand(cfg *commandConfig) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "refresh [NAME]",
		Short: "Refresh remote relay groups",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("NAME cannot be used with --all")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			if err := refreshRelayGroups(c.Context(), *cfg, args, all); err != nil {
				return err
			}
			if all {
				fmt.Fprintln(c.OutOrStdout(), "remote relay groups refreshed")
			} else {
				fmt.Fprintf(c.OutOrStdout(), "relay group %q refreshed\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "refresh all remote relay groups")
	return cmd
}

func newRelayGroupCheckCommand(cfg *commandConfig) *cobra.Command {
	var all bool
	var wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "check [NAME]",
		Short: "Check relay group health",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("NAME cannot be used with --all")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			out := c.OutOrStdout()
			var baseline map[string]time.Time
			var filter func(relayHealth) bool
			if wait {
				snapshot, err := fetchRelays(c.Context(), *cfg)
				if err != nil {
					return err
				}
				baseline = baselineLastChecked(snapshot)
				if all {
					filter = func(relayHealth) bool { return true }
				} else {
					group := args[0]
					filter = func(r relayHealth) bool { return r.Group == group }
				}
			}
			if err := checkRelayGroups(c.Context(), *cfg, args, all); err != nil {
				return err
			}
			if all {
				fmt.Fprintln(out, "relay groups check in progress")
			} else {
				fmt.Fprintf(out, "relay group %q check in progress\n", args[0])
			}
			if !wait {
				return nil
			}
			results, err := waitForRelayChecks(c.Context(), *cfg, filter, baseline, waitTimeout)
			if err != nil {
				return err
			}
			return printCheckResults(out, results)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "check all relay groups")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for checks to complete and print latency results")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 60*time.Second, "maximum time to wait when --wait is set")
	return cmd
}

func newRelaysCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "relays",
		Short: "Manage relays",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			relays, err := fetchRelays(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeRelays(c.OutOrStdout(), relays, flags)
		},
	}
	addListFlags(cmd, &flags)
	cmd.AddCommand(newRelayGetCommand(cfg))
	cmd.AddCommand(newRelayCreateCommand(cfg))
	cmd.AddCommand(newRelaySetCommand(cfg))
	cmd.AddCommand(newRelaySelectCommand(cfg))
	cmd.AddCommand(newRelayCheckCommand(cfg))
	cmd.AddCommand(newRelayDeleteCommand(cfg))
	return cmd
}

func newRelayGetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get GROUP RELAY",
		Short: "Show relay details",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			relay, err := fetchRelay(c.Context(), *cfg, args[0], args[1])
			if err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q in group %q not found", args[1], args[0])
				}
				return err
			}
			return writeRelayDescribe(c.OutOrStdout(), relay)
		},
	}
}

func newRelayCreateCommand(cfg *commandConfig) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "create GROUP --file relay.yaml",
		Short: "Create relays in an inline relay group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("missing --file")
			}
			proxies, err := readRelayFile(file)
			if err != nil {
				return err
			}
			if err := createRelays(c.Context(), *cfg, args[0], proxies); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%d relay(s) created in group %q\n", len(proxies), args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "relay YAML or provider YAML with proxies")
	return cmd
}

func newRelaySetCommand(cfg *commandConfig) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "set GROUP RELAY --file relay.yaml",
		Short: "Update a relay in an inline relay group",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("missing --file")
			}
			proxies, err := readRelayFile(file)
			if err != nil {
				return err
			}
			proxy, err := selectRelaySpec(proxies, args[1])
			if err != nil {
				return err
			}
			if err := updateRelay(c.Context(), *cfg, args[0], args[1], proxy); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay %q in group %q updated\n", args[1], args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "relay YAML or provider YAML with proxies")
	return cmd
}

func newRelaySelectCommand(cfg *commandConfig) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "select RELAY",
		Short: "Select a relay in a manual relay group",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := ensureRelayNameUnambiguous(c.Context(), *cfg, args[0], group); err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q not found", args[0])
				}
				return err
			}
			selected, err := selectRelayByName(c.Context(), *cfg, args[0], group)
			if err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay %q selected\n", selected)
			return nil
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "relay group name when RELAY is ambiguous")
	return cmd
}

func newRelayCheckCommand(cfg *commandConfig) *cobra.Command {
	var group string
	var all bool
	var wait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "check [RELAY]",
		Short: "Check relay health",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("RELAY cannot be used with --all")
				}
				if group != "" {
					return fmt.Errorf("--group cannot be used with --all")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			out := c.OutOrStdout()
			if all {
				var baseline map[string]time.Time
				if wait {
					snapshot, err := fetchRelays(c.Context(), *cfg)
					if err != nil {
						return err
					}
					baseline = baselineLastChecked(snapshot)
				}
				if err := checkRelayGroups(c.Context(), *cfg, nil, true); err != nil {
					return err
				}
				fmt.Fprintln(out, "relays check in progress")
				if !wait {
					return nil
				}
				results, err := waitForRelayChecks(c.Context(), *cfg, func(relayHealth) bool { return true }, baseline, waitTimeout)
				if err != nil {
					return err
				}
				return printCheckResults(out, results)
			}
			if err := ensureRelayNameUnambiguous(c.Context(), *cfg, args[0], group); err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q not found", args[0])
				}
				return err
			}
			var baseline map[string]time.Time
			if wait {
				snapshot, err := fetchRelays(c.Context(), *cfg)
				if err != nil {
					return err
				}
				baseline = baselineLastChecked(snapshot)
			}
			checked, err := checkRelay(c.Context(), *cfg, args[0], group)
			if err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(out, "relay %q check in progress\n", checked)
			if !wait {
				return nil
			}
			short := checked
			if idx := strings.LastIndex(checked, "/"); idx >= 0 {
				short = checked[idx+1:]
			}
			filter := func(r relayHealth) bool {
				if r.Name == checked {
					return true
				}
				if relayShortName(r.Name, r.Group) == short && (group == "" || r.Group == group) {
					return true
				}
				return false
			}
			results, err := waitForRelayChecks(c.Context(), *cfg, filter, baseline, waitTimeout)
			if err != nil {
				return err
			}
			return printCheckResults(out, results)
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "relay group name when RELAY is ambiguous")
	cmd.Flags().BoolVar(&all, "all", false, "check all relays")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the check to complete and print latency results")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 60*time.Second, "maximum time to wait when --wait is set")
	return cmd
}

func newRelayDeleteCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "delete GROUP RELAY",
		Short: "Delete a relay from an inline relay group",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if err := deleteRelay(c.Context(), *cfg, args[0], args[1]); err != nil {
				if errors.Is(err, errRelayNotFound) {
					return fmt.Errorf("relay %q in group %q not found", args[1], args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "relay %q in group %q deleted\n", args[1], args[0])
			return nil
		},
	}
}

func addRelayGroupFlags(cmd *cobra.Command, providerFile, remoteURL, selectMode, keep, remove *string, refresh *int) {
	cmd.Flags().StringVar(providerFile, "provider-file", "", "Mihomo proxy provider YAML with proxies")
	cmd.Flags().StringVar(remoteURL, "url", "", "remote Mihomo proxy provider URL")
	cmd.Flags().StringVar(selectMode, "select", "", "relay selection mode for the group: auto or manual")
	cmd.Flags().StringVar(keep, "keep", "", "regular expression of relay names to keep")
	cmd.Flags().StringVar(remove, "remove", "", "regular expression of relay names to remove")
	cmd.Flags().IntVar(refresh, "refresh-duration", 0, "remote refresh duration in seconds")
}

func buildRelayGroupFromFlags(name, providerFile, remoteURL, selectMode, keep, remove string, refresh int) (relayGroupConfig, error) {
	group := relayGroupConfig{
		Name:            name,
		Select:          selectMode,
		Keep:            keep,
		Remove:          remove,
		RefreshDuration: refresh,
	}
	switch {
	case strings.TrimSpace(remoteURL) != "" && strings.TrimSpace(providerFile) != "":
		return relayGroupConfig{}, fmt.Errorf("--url and --provider-file are mutually exclusive")
	case strings.TrimSpace(remoteURL) != "":
		group.Type = "remote"
		group.URL = strings.TrimSpace(remoteURL)
	case strings.TrimSpace(providerFile) != "":
		provider, err := readRelayProvider(providerFile)
		if err != nil {
			return relayGroupConfig{}, err
		}
		group.Type = "inline"
		group.Proxies = provider.Proxies
	default:
		return relayGroupConfig{}, fmt.Errorf("provide --url for a remote group or --provider-file for an inline group")
	}
	return group, nil
}

func readRelayProvider(path string) (relayProviderFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return relayProviderFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var provider relayProviderFile
	if err := yaml.Unmarshal(data, &provider); err != nil {
		return relayProviderFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(provider.Proxies) == 0 {
		return relayProviderFile{}, fmt.Errorf("%s has no proxies", path)
	}
	provider.Proxies = dedupeProxySpecs(provider.Proxies)
	return provider, nil
}

func readRelayFile(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var provider relayProviderFile
	if err := yaml.Unmarshal(data, &provider); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(provider.Proxies) > 0 {
		return dedupeProxySpecs(provider.Proxies), nil
	}
	var proxy map[string]any
	if err := yaml.Unmarshal(data, &proxy); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if name, _ := proxy["name"].(string); strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%s is neither a provider YAML with proxies nor a relay YAML with name", path)
	}
	return []map[string]any{proxy}, nil
}

func dedupeProxySpecs(proxies []map[string]any) []map[string]any {
	used := make(map[string]struct{}, len(proxies))
	result := make([]map[string]any, 0, len(proxies))
	for _, proxy := range proxies {
		name, _ := proxy["name"].(string)
		if name == "" {
			result = append(result, proxy)
			continue
		}
		next := name
		if _, ok := used[next]; ok {
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("%s-%d", name, i)
				if _, exists := used[candidate]; !exists {
					next = candidate
					break
				}
			}
			clone := make(map[string]any, len(proxy))
			for k, v := range proxy {
				clone[k] = v
			}
			clone["name"] = next
			proxy = clone
		}
		used[next] = struct{}{}
		result = append(result, proxy)
	}
	return result
}

func selectRelaySpec(proxies []map[string]any, name string) (map[string]any, error) {
	if len(proxies) == 1 {
		if proxyName, _ := proxies[0]["name"].(string); proxyName == "" {
			proxies[0]["name"] = name
		}
		return proxies[0], nil
	}
	for _, proxy := range proxies {
		if proxyName, _ := proxy["name"].(string); proxyName == name {
			return proxy, nil
		}
	}
	return nil, fmt.Errorf("provider contains multiple proxies and none named %q", name)
}

func fetchRelayGroups(ctx context.Context, cfg commandConfig) ([]relayGroupStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/relaygroups")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var groups []relayGroupStatus
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("decode relay groups: %w", err)
	}
	return groups, nil
}

func fetchRelayGroup(ctx context.Context, cfg commandConfig, name string) (relayGroupStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/relaygroups/"+url.PathEscape(name))
	if err != nil {
		return relayGroupStatus{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return relayGroupStatus{}, errRelayGroupNotFound
		}
		return relayGroupStatus{}, err
	}
	defer resp.Body.Close()
	var group relayGroupStatus
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		return relayGroupStatus{}, fmt.Errorf("decode relay group: %w", err)
	}
	return group, nil
}

func createRelayGroup(ctx context.Context, cfg commandConfig, group relayGroupConfig) error {
	return sendJSON(ctx, cfg, http.MethodPost, "/api/relaygroups", group, http.StatusCreated, http.StatusOK)
}

func updateRelayGroup(ctx context.Context, cfg commandConfig, name string, group relayGroupConfig) error {
	return sendJSON(ctx, cfg, http.MethodPut, "/api/relaygroups/"+url.PathEscape(name), group, http.StatusOK)
}

func deleteRelayGroup(ctx context.Context, cfg commandConfig, name string) error {
	err := sendJSON(ctx, cfg, http.MethodDelete, "/api/relaygroups/"+url.PathEscape(name), nil, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRelayGroupNotFound
	}
	return err
}

func refreshRelayGroups(ctx context.Context, cfg commandConfig, args []string, all bool) error {
	path := ""
	if all {
		path = "/api/relaygroups/refresh?all=true"
	} else {
		path = "/api/relaygroups/" + url.PathEscape(args[0]) + "/refresh"
	}
	err := sendJSON(ctx, cfg, http.MethodPost, path, nil, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRelayGroupNotFound
	}
	return err
}

func checkRelayGroups(ctx context.Context, cfg commandConfig, args []string, all bool) error {
	path := ""
	if all {
		path = "/api/relaygroups/check?all=true"
	} else {
		path = "/api/relaygroups/" + url.PathEscape(args[0]) + "/check"
	}
	err := sendJSON(ctx, cfg, http.MethodPost, path, nil, http.StatusOK, http.StatusAccepted)
	if errors.Is(err, errAPINotFound) {
		return errRelayGroupNotFound
	}
	return err
}

func selectRelayGroupByName(ctx context.Context, cfg commandConfig, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/relaygroups/"+url.PathEscape(name)+"/select")
	if err != nil {
		return "", err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return "", errRelayGroupNotFound
		}
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode relay group selection: %w", err)
	}
	if result.Group == "" {
		result.Group = name
	}
	return result.Group, nil
}

func fetchRelays(ctx context.Context, cfg commandConfig) ([]relayHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/relays")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var relays []relayHealth
	if err := json.NewDecoder(resp.Body).Decode(&relays); err != nil {
		return nil, fmt.Errorf("decode relays: %w", err)
	}
	return relays, nil
}

func fetchRelay(ctx context.Context, cfg commandConfig, group, relay string) (relayHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/relaygroups/"+url.PathEscape(group)+"/relays/"+url.PathEscape(relay))
	if err != nil {
		return relayHealth{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return relayHealth{}, errRelayNotFound
		}
		return relayHealth{}, err
	}
	defer resp.Body.Close()
	var relayStatus relayHealth
	if err := json.NewDecoder(resp.Body).Decode(&relayStatus); err != nil {
		return relayHealth{}, fmt.Errorf("decode relay: %w", err)
	}
	return relayStatus, nil
}

func createRelays(ctx context.Context, cfg commandConfig, group string, proxies []map[string]any) error {
	return sendJSON(ctx, cfg, http.MethodPost, "/api/relaygroups/"+url.PathEscape(group)+"/relays", relaysRequest{Relays: proxies}, http.StatusCreated, http.StatusOK)
}

func updateRelay(ctx context.Context, cfg commandConfig, group, relay string, proxy map[string]any) error {
	err := sendJSON(ctx, cfg, http.MethodPut, "/api/relaygroups/"+url.PathEscape(group)+"/relays/"+url.PathEscape(relay), proxy, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRelayNotFound
	}
	return err
}

func deleteRelay(ctx context.Context, cfg commandConfig, group, relay string) error {
	err := sendJSON(ctx, cfg, http.MethodDelete, "/api/relaygroups/"+url.PathEscape(group)+"/relays/"+url.PathEscape(relay), nil, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRelayNotFound
	}
	return err
}

func selectRelayByName(ctx context.Context, cfg commandConfig, relay, group string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	path := "/api/relays/" + url.PathEscape(relay) + "/select"
	if group != "" {
		path += "?group=" + url.QueryEscape(group)
	}
	endpoint, err := apiURL(cfg.addr, path)
	if err != nil {
		return "", err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return "", errRelayNotFound
		}
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Relay string `json:"relay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode relay selection: %w", err)
	}
	if result.Relay == "" {
		result.Relay = relay
	}
	return result.Relay, nil
}

func checkRelay(ctx context.Context, cfg commandConfig, relay, group string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	path := "/api/relays/" + url.PathEscape(relay) + "/check"
	if group != "" {
		path += "?group=" + url.QueryEscape(group)
	}
	endpoint, err := apiURL(cfg.addr, path)
	if err != nil {
		return "", err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, nil, http.StatusOK, http.StatusAccepted)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return "", errRelayNotFound
		}
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		Relay string `json:"relay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode relay check: %w", err)
	}
	if result.Relay == "" {
		result.Relay = relay
	}
	return result.Relay, nil
}

func baselineLastChecked(relays []relayHealth) map[string]time.Time {
	out := make(map[string]time.Time, len(relays))
	for _, r := range relays {
		out[r.Group+"\x00"+r.Name] = r.LastCheckedAt
	}
	return out
}

func waitForRelayChecks(ctx context.Context, cfg commandConfig, filter func(relayHealth) bool, baseline map[string]time.Time, timeout time.Duration) ([]relayHealth, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	interval := 200 * time.Millisecond
	var last []relayHealth
	for {
		relays, err := fetchRelays(ctx, cfg)
		if err != nil {
			return nil, err
		}
		var matching []relayHealth
		allUpdated := true
		for _, r := range relays {
			if !filter(r) {
				continue
			}
			matching = append(matching, r)
			prev := baseline[r.Group+"\x00"+r.Name]
			if !r.LastCheckedAt.After(prev) {
				allUpdated = false
			}
		}
		last = matching
		if len(matching) > 0 && allUpdated {
			return matching, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("timed out waiting for relay checks to complete after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func printCheckResults(w io.Writer, relays []relayHealth) error {
	rows := make([]relayRow, 0, len(relays))
	for _, r := range relays {
		rows = append(rows, relayRow{
			Group:   r.Group,
			Relay:   relayShortName(r.Name, r.Group),
			Status:  formatOptional(r.Status),
			Latency: latencyMS(r.Latency),
		})
	}
	printer, err := klo.PrinterFromFlag("", &klo.Specs{
		DefaultColumnSpec: "RELAY GROUP:{.Group},RELAY:{.Relay},STATUS:{.Status},LATENCY:{.Latency}",
	})
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func ensureRelayNameUnambiguous(ctx context.Context, cfg commandConfig, relay, group string) error {
	relays, err := fetchRelays(ctx, cfg)
	if err != nil {
		return err
	}
	var matches []string
	for _, item := range relays {
		short := relayShortName(item.Name, item.Group)
		if short != relay && item.Name != relay {
			continue
		}
		if group != "" && item.Group != group {
			continue
		}
		matches = append(matches, fmt.Sprintf("%s/%s", item.Group, short))
	}
	if len(matches) == 0 {
		return errRelayNotFound
	}
	if len(matches) > 1 {
		return fmt.Errorf("relay name %q appears in multiple groups (%s); supply --group", relay, strings.Join(matches, ", "))
	}
	return nil
}

func sendJSON(ctx context.Context, cfg commandConfig, method, path string, body any, accept ...int) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, path)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	resp, err := doRequest(ctx, cfg, method, endpoint, reader, accept...)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func writeRelayGroups(w io.Writer, groups []relayGroupStatus, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "NAME:{.Name},TYPE:{.Type},RELAYS:{.Relays},SELECT:{.Select},SELECTED:{.Selected},RELAY:{.Relay},STATUS:{.Status},LATENCY:{.Latency},TTL:{.TTL}",
		WideColumnSpec:    "NAME:{.Name},TYPE:{.Type},RELAYS:{.Relays},SELECT:{.Select},SELECTED:{.Selected},RELAY:{.Relay},STATUS:{.Status},LATENCY:{.Latency},REMOTE:{.Remote},LAST-REFRESHED:{.LastRefreshedAt},NEXT-REFRESH:{.NextRefreshAt},TTL:{.TTL},ERROR:{.Error}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(relayGroupRows(groups), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func writeRelays(w io.Writer, relays []relayHealth, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "GROUP:{.Group},RELAY:{.Relay},TYPE:{.Type},STATUS:{.Status},LATENCY:{.Latency},SELECTED:{.Selected}",
		WideColumnSpec:    "GROUP:{.Group},RELAY:{.Relay},TYPE:{.Type},STATUS:{.Status},LATENCY:{.Latency},SELECTED:{.Selected},ADDR:{.Addr},REMOTE:{.Remote},LAST-CHECKED:{.LastCheckedAt},LAST-REFRESHED:{.LastRefreshedAt},ERROR:{.Error}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(relayRows(relays), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func relayGroupRows(groups []relayGroupStatus) []relayGroupRow {
	now := time.Now()
	rows := make([]relayGroupRow, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, relayGroupRow{
			Name:            group.Name,
			Type:            formatOptional(group.Type),
			Relays:          group.RelayCount,
			Select:          formatOptional(group.Select),
			Selected:        formatBool(group.Selected),
			Relay:           formatOptional(group.CurrentRelay),
			Status:          formatOptional(group.CurrentStatus),
			Latency:         latencyMS(group.CurrentLatency),
			TTL:             formatRemaining(now, group.NextRefreshAt),
			Remote:          formatOptional(group.RemoteAddress),
			LastCheckedAt:   formatTime(group.LastCheckedAt),
			NextCheckAt:     formatTime(group.NextCheckAt),
			LastRefreshedAt: formatTime(group.LastRefreshedAt),
			NextRefreshAt:   formatTime(group.NextRefreshAt),
			Error:           formatOptional(group.Error),
		})
	}
	return rows
}

func relayRows(relays []relayHealth) []relayRow {
	rows := make([]relayRow, 0, len(relays))
	for _, relay := range relays {
		rows = append(rows, relayRow{
			Group:           relay.Group,
			Relay:           relayShortName(relay.Name, relay.Group),
			Type:            formatOptional(relay.Type),
			Status:          formatOptional(relay.Status),
			Latency:         latencyMS(relay.Latency),
			Selected:        formatBool(relay.Selected),
			Addr:            formatOptional(relay.Addr),
			Remote:          formatOptional(relay.GroupSourceURL),
			LastCheckedAt:   formatTime(relay.LastCheckedAt),
			LastRefreshedAt: formatTime(relay.LastRefreshedAt),
			Error:           formatOptional(relay.Error),
		})
	}
	return rows
}

func writeRelayGroupDescribe(w io.Writer, group relayGroupStatus) error {
	now := time.Now()
	fmt.Fprintf(w, "Name:              %s\n", group.Name)
	fmt.Fprintf(w, "Type:              %s\n", formatOptional(group.Type))
	fmt.Fprintf(w, "Relays:            %s\n", formatRelayCount(group.RelayCount, group.Config.Keep, group.Config.Remove))
	fmt.Fprintf(w, "Selected:          %s\n", formatSelectedWithMode(group.Selected, group.Select))
	fmt.Fprintf(w, "Current Relay:     %s\n", formatCurrentRelay(group))
	fmt.Fprintf(w, "Remote Address:    %s\n", formatOptional(group.RemoteAddress))
	fmt.Fprintf(w, "Last Checked:      %s\n", formatScheduledTime(now, group.LastCheckedAt, group.NextCheckAt, group.CheckInterval))
	fmt.Fprintf(w, "Last Refreshed:    %s\n", formatScheduledTime(now, group.LastRefreshedAt, group.NextRefreshAt, group.RefreshInterval))
	fmt.Fprintf(w, "Error:             %s\n", formatOptional(group.Error))
	return nil
}

func writeRelayDescribe(w io.Writer, relay relayHealth) error {
	now := time.Now()
	fmt.Fprintf(w, "Name:            %s\n", formatOptional(relay.Name))
	fmt.Fprintf(w, "Status:          %s\n", formatHealthSummary(relay.Status, relay.Latency))
	fmt.Fprintf(w, "Selected:        %s\n", formatSelectedWithMode(relay.Selected, relay.GroupMode))
	fmt.Fprintf(w, "Last Checked:    %s\n", formatScheduledTime(now, relay.LastCheckedAt, relayNextCheckAt(relay), relay.CheckInterval))
	fmt.Fprintf(w, "Last Refreshed:  %s\n", formatScheduledTime(now, relay.LastRefreshedAt, relay.NextRefreshAt, relay.RefreshInterval))
	fmt.Fprintf(w, "Error:           %s\n", formatOptional(relay.Error))
	fmt.Fprintln(w, "History:")
	if len(relay.History) == 0 {
		fmt.Fprintln(w, "  -")
	} else {
		for _, record := range relay.History {
			fmt.Fprintf(w, "  - %s  %s  latency %s\n",
				formatTime(record.Time),
				formatOptional(record.Status),
				formatLatency(record.Latency),
			)
		}
	}
	fmt.Fprintln(w, "Spec:")
	if len(relay.Spec) == 0 {
		fmt.Fprintln(w, "  -")
		return nil
	}
	return writeYAMLIndented(w, relay.Spec, 2)
}

func formatRelayCount(count int, keep, remove string) string {
	conditions := make([]string, 0, 2)
	if strings.TrimSpace(keep) != "" {
		conditions = append(conditions, "keep "+strings.TrimSpace(keep))
	}
	if strings.TrimSpace(remove) != "" {
		conditions = append(conditions, "remove "+strings.TrimSpace(remove))
	}
	if len(conditions) == 0 {
		return fmt.Sprintf("%d", count)
	}
	return fmt.Sprintf("%d (%s)", count, strings.Join(conditions, ", "))
}

func formatSelectedWithMode(selected bool, mode string) string {
	result := formatBool(selected)
	if strings.TrimSpace(mode) == "" {
		return result
	}
	return fmt.Sprintf("%s (%s)", result, mode)
}

func formatCurrentRelay(group relayGroupStatus) string {
	relay := formatOptional(group.CurrentRelay)
	if relay == "-" {
		return relay
	}
	status := formatOptional(group.CurrentStatus)
	if status == "-" {
		return relay
	}
	return fmt.Sprintf("%s (%s, latency %s)", relay, status, formatLatency(group.CurrentLatency))
}

func formatHealthSummary(status string, latency int64) string {
	status = formatOptional(status)
	if status == "-" {
		return status
	}
	return fmt.Sprintf("%s (latency %s)", status, formatLatency(latency))
}

func formatScheduledTime(now, last, next time.Time, intervalSeconds int64) string {
	if last.IsZero() {
		return "-"
	}
	parts := []string{}
	if intervalSeconds <= 0 && !next.IsZero() && next.After(last) {
		intervalSeconds = int64(next.Sub(last).Seconds())
	}
	if intervalSeconds > 0 {
		parts = append(parts, "every "+formatBriefDuration(time.Duration(intervalSeconds)*time.Second))
	}
	if next.IsZero() && intervalSeconds > 0 {
		next = last.Add(time.Duration(intervalSeconds) * time.Second)
	}
	if !next.IsZero() {
		parts = append(parts, "next in "+formatBriefDuration(next.Sub(now)))
	}
	if len(parts) == 0 {
		return formatTime(last)
	}
	return fmt.Sprintf("%s (%s)", formatTime(last), strings.Join(parts, ", "))
}

func relayNextCheckAt(relay relayHealth) time.Time {
	if relay.LastCheckedAt.IsZero() || relay.CheckInterval <= 0 {
		return time.Time{}
	}
	return relay.LastCheckedAt.Add(time.Duration(relay.CheckInterval) * time.Second)
}

func formatBriefDuration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	d = d.Round(time.Second)
	if d < time.Second {
		d = time.Second
	}
	seconds := int64(d / time.Second)
	units := []struct {
		suffix string
		value  int64
	}{
		{"d", 24 * 60 * 60},
		{"h", 60 * 60},
		{"m", 60},
		{"s", 1},
	}
	parts := make([]string, 0, 2)
	for _, unit := range units {
		if seconds < unit.value {
			continue
		}
		value := seconds / unit.value
		seconds %= unit.value
		parts = append(parts, fmt.Sprintf("%d%s", value, unit.suffix))
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, "")
}

func writeYAMLIndented(w io.Writer, value any, spaces int) error {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	prefix := strings.Repeat(" ", spaces)
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		fmt.Fprintln(w, prefix+line)
	}
	return nil
}

func formatBool(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func relayShortName(name, group string) string {
	return strings.TrimPrefix(name, group+" / ")
}
