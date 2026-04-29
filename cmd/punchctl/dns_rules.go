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
	"time"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

var errRuleNotFound = errors.New("rule not found")

type ruleStats struct {
	Index       int       `json:"index" yaml:"index"`
	Decision    string    `json:"decision" yaml:"decision"`
	Source      string    `json:"source" yaml:"source"`
	Type        string    `json:"type,omitempty" yaml:"type,omitempty"`
	Count       int       `json:"count" yaml:"count"`
	Hits        int64     `json:"hits" yaml:"hits"`
	LastUpdated time.Time `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	NextUpdate  time.Time `json:"next_update,omitempty" yaml:"next_update,omitempty"`
	Default     bool      `json:"default,omitempty" yaml:"default,omitempty"`
}

type ruleRow struct {
	Index       int    `json:"index" yaml:"index"`
	Decision    string `json:"decision" yaml:"decision"`
	Type        string `json:"type" yaml:"type"`
	Count       int    `json:"count" yaml:"count"`
	Hits        int64  `json:"hits" yaml:"hits"`
	LastUpdated string `json:"last_updated,omitempty" yaml:"last_updated,omitempty"`
	NextUpdate  string `json:"next_update,omitempty" yaml:"next_update,omitempty"`
	Source      string `json:"source" yaml:"source"`
}

type rulePayload struct {
	Decision  string `json:"decision,omitempty"`
	Source    string `json:"source,omitempty"`
	NewSource string `json:"new_source,omitempty"`
	Index     *int   `json:"index,omitempty"`
}

type ruleCommandConfig struct {
	use       string
	short     string
	endpoint  string
	noun      string
	decisions string
	source    string
}

func newRulesCommand(cfg *commandConfig) *cobra.Command {
	return newRuleCollectionCommand(cfg, ruleCommandConfig{
		use:       "rules",
		short:     "Manage DNS rules",
		endpoint:  "/api/dns/rules",
		noun:      "rule",
		decisions: "reject, relay, direct",
		source:    `SOURCE is a URL/file path, inline domain rule like "domain:example.com", "keyword:stun", "full:x.com", or "regexp:...", or qtype rule like "qtype:65" or "qtype:ptr".`,
	})
}

func newRoutesCommand(cfg *commandConfig) *cobra.Command {
	return newRuleCollectionCommand(cfg, ruleCommandConfig{
		use:       "routes",
		short:     "Manage DNS CIDR routes",
		endpoint:  "/api/dns/routes",
		noun:      "route",
		decisions: "direct, reject",
		source:    `SOURCE is a URL/file path containing CIDRs or an inline CIDR like "10.0.0.0/8".`,
	})
}

func newRuleCollectionCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	var flags listFlags
	var decision string
	cmd := &cobra.Command{
		Use:   rc.use,
		Short: rc.short,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			rules, err := fetchRuleEntries(c.Context(), *cfg, rc.endpoint)
			if err != nil {
				return err
			}
			rules = filterRuleEntries(rules, decision)
			return writeRules(c.OutOrStdout(), rules, flags)
		},
	}
	addListFlags(cmd, &flags)
	cmd.Flags().StringVar(&decision, "decision", "", "filter by decision")

	cmd.AddCommand(newRuleCreateCommand(cfg, rc))
	cmd.AddCommand(newRuleSetCommand(cfg, rc))
	cmd.AddCommand(newRuleDeleteCommand(cfg, rc))
	cmd.AddCommand(newRuleMoveCommand(cfg, rc))
	cmd.AddCommand(newRuleRefreshCommand(cfg, rc))
	return cmd
}

func newRuleCreateCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	var index int
	cmd := &cobra.Command{
		Use:   "create DECISION SOURCE",
		Short: "Create a DNS " + rc.noun,
		Long: fmt.Sprintf(`Create a DNS %s.

DECISION is one of: %s.
%s

Use --index to insert at a specific position; defaults to appending at the end.`, rc.noun, rc.decisions, rc.source),
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			payload := rulePayload{Decision: args[0], Source: args[1]}
			if c.Flags().Changed("index") {
				idx := index
				payload.Index = &idx
			}
			if err := createRule(c.Context(), *cfg, rc.endpoint, payload); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s %q created\n", rc.noun, args[1])
			return nil
		},
	}
	cmd.Flags().IntVar(&index, "index", 0, "position to insert at (0-based)")
	return cmd
}

func newRuleSetCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set INDEX DECISION SOURCE",
		Short: "Replace a DNS " + rc.noun,
		Args:  cobra.ExactArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			idx, err := parseRuleIndexArg(args[0])
			if err != nil {
				return err
			}
			payload := rulePayload{Decision: args[1], Source: args[2]}
			if err := updateRule(c.Context(), *cfg, rc.endpoint, idx, payload); err != nil {
				if errors.Is(err, errRuleNotFound) {
					return fmt.Errorf("%s index %d not found", rc.noun, idx)
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s index %d updated\n", rc.noun, idx)
			return nil
		},
	}
	return cmd
}

func newRuleDeleteCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "delete INDEX",
		Short: "Delete a DNS " + rc.noun,
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			idx, err := parseRuleIndexArg(args[0])
			if err != nil {
				return err
			}
			if err := deleteRule(c.Context(), *cfg, rc.endpoint, idx); err != nil {
				if errors.Is(err, errRuleNotFound) {
					return fmt.Errorf("%s index %d not found", rc.noun, idx)
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s index %d deleted\n", rc.noun, idx)
			return nil
		},
	}
}

func newRuleMoveCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	var index int
	cmd := &cobra.Command{
		Use:   "move INDEX --index N",
		Short: "Move a DNS " + rc.noun,
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !c.Flags().Changed("index") {
				return fmt.Errorf("--index is required")
			}
			from, err := parseRuleIndexArg(args[0])
			if err != nil {
				return err
			}
			payload := rulePayload{Index: &index}
			if err := moveRule(c.Context(), *cfg, rc.endpoint, from, payload); err != nil {
				if errors.Is(err, errRuleNotFound) {
					return fmt.Errorf("%s index %d not found", rc.noun, from)
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s index %d moved to index %d\n", rc.noun, from, index)
			return nil
		},
	}
	cmd.Flags().IntVar(&index, "index", 0, "new position (0-based)")
	return cmd
}

func newRuleRefreshCommand(cfg *commandConfig, rc ruleCommandConfig) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "refresh [INDEX]",
		Short: "Refresh remote DNS " + rc.noun + " sources",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("INDEX cannot be used with --all")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			idx := -1
			if !all {
				var err error
				idx, err = parseRuleIndexArg(args[0])
				if err != nil {
					return err
				}
			}
			if err := refreshRule(c.Context(), *cfg, rc.endpoint, idx, all); err != nil {
				if errors.Is(err, errRuleNotFound) {
					return fmt.Errorf("%s index %d not found", rc.noun, idx)
				}
				return err
			}
			if all {
				fmt.Fprintf(c.OutOrStdout(), "%ss refreshed\n", rc.noun)
			} else {
				fmt.Fprintf(c.OutOrStdout(), "%s index %d refreshed\n", rc.noun, idx)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "refresh all remote sources")
	return cmd
}

func parseRuleIndexArg(value string) (int, error) {
	idx, err := strconv.Atoi(value)
	if err != nil || idx < 0 {
		return 0, fmt.Errorf("invalid index %q", value)
	}
	return idx, nil
}

func fetchRuleEntries(ctx context.Context, cfg commandConfig, endpointPath string) ([]ruleStats, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, endpointPath)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rules []ruleStats
	if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil {
		return nil, fmt.Errorf("decode rules: %w", err)
	}
	return rules, nil
}

func createRule(ctx context.Context, cfg commandConfig, endpointPath string, payload rulePayload) error {
	return sendRule(ctx, cfg, http.MethodPost, endpointPath, payload, http.StatusCreated, http.StatusOK)
}

func updateRule(ctx context.Context, cfg commandConfig, endpointPath string, index int, payload rulePayload) error {
	err := sendRule(ctx, cfg, http.MethodPut, withIndex(endpointPath, index), payload, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRuleNotFound
	}
	return err
}

func deleteRule(ctx context.Context, cfg commandConfig, endpointPath string, index int) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, withIndex(endpointPath, index))
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodDelete, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errRuleNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func moveRule(ctx context.Context, cfg commandConfig, endpointPath string, from int, payload rulePayload) error {
	err := sendRule(ctx, cfg, http.MethodPost, withIndex(endpointPath+"/move", from), payload, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRuleNotFound
	}
	return err
}

func refreshRule(ctx context.Context, cfg commandConfig, endpointPath string, index int, all bool) error {
	path := endpointPath + "/refresh"
	if all {
		path += "?all=true"
	} else {
		path = withIndex(path, index)
	}
	err := sendRule(ctx, cfg, http.MethodPost, path, rulePayload{}, http.StatusOK)
	if errors.Is(err, errAPINotFound) {
		return errRuleNotFound
	}
	return err
}

func withIndex(endpointPath string, index int) string {
	reqURL := &url.URL{Path: endpointPath}
	query := reqURL.Query()
	query.Set("index", strconv.Itoa(index))
	reqURL.RawQuery = query.Encode()
	return reqURL.String()
}

func sendRule(ctx context.Context, cfg commandConfig, method, path string, payload rulePayload, accept ...int) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, method, endpoint, strings.NewReader(string(body)), accept...)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func filterRuleEntries(rules []ruleStats, decision string) []ruleStats {
	decision = strings.TrimSpace(decision)
	if decision == "" {
		return rules
	}
	out := make([]ruleStats, 0, len(rules))
	for _, rule := range rules {
		if rule.Decision == decision {
			out = append(out, rule)
		}
	}
	return out
}

func writeRules(w io.Writer, rules []ruleStats, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "INDEX:{.Index},DECISION:{.Decision},TYPE:{.Type},COUNT:{.Count},HITS:{.Hits},LAST-UPDATED:{.LastUpdated},NEXT-UPDATE:{.NextUpdate},SOURCE:{.Source}",
		WideColumnSpec:    "INDEX:{.Index},DECISION:{.Decision},TYPE:{.Type},COUNT:{.Count},HITS:{.Hits},LAST-UPDATED:{.LastUpdated},NEXT-UPDATE:{.NextUpdate},SOURCE:{.Source}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(ruleRows(rules), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func ruleRows(rules []ruleStats) []ruleRow {
	rows := make([]ruleRow, 0, len(rules))
	for _, rule := range rules {
		rows = append(rows, ruleRow{
			Index:       rule.Index,
			Decision:    formatOptional(rule.Decision),
			Type:        formatOptional(rule.Type),
			Count:       rule.Count,
			Hits:        rule.Hits,
			LastUpdated: formatTime(rule.LastUpdated),
			NextUpdate:  formatTime(rule.NextUpdate),
			Source:      rule.Source,
		})
	}
	return rows
}
