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

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

var errConfigKeyNotFound = errors.New("config key not found")

type configEntry struct {
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
}

type configValueRequest struct {
	Value string `json:"value"`
}

func newConfigCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and update global configuration",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			entries, err := fetchConfig(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeConfigEntries(c.OutOrStdout(), entries, flags)
		},
	}
	addListFlags(cmd, &flags)
	cmd.AddCommand(newConfigGetCommand(cfg))
	cmd.AddCommand(newConfigSetCommand(cfg))
	return cmd
}

func newConfigGetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Show one config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			entry, err := fetchConfigKey(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errConfigKeyNotFound) {
					return fmt.Errorf("config key %q not found", args[0])
				}
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), entry.Value)
			return nil
		},
	}
}

func newConfigSetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "Set one config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			entry, err := setConfigKey(c.Context(), *cfg, args[0], args[1])
			if err != nil {
				if errors.Is(err, errConfigKeyNotFound) {
					return fmt.Errorf("config key %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "%s=%s\n", entry.Key, entry.Value)
			return nil
		},
	}
}

func fetchConfig(ctx context.Context, cfg commandConfig) ([]configEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/config")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []configEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return entries, nil
}

func fetchConfigKey(ctx context.Context, cfg commandConfig, key string) (configEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/config?key="+url.QueryEscape(key))
	if err != nil {
		return configEntry{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return configEntry{}, errConfigKeyNotFound
		}
		return configEntry{}, err
	}
	defer resp.Body.Close()
	var entry configEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return configEntry{}, fmt.Errorf("decode config value: %w", err)
	}
	return entry, nil
}

func setConfigKey(ctx context.Context, cfg commandConfig, key, value string) (configEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/config/"+url.PathEscape(key))
	if err != nil {
		return configEntry{}, err
	}
	payload, err := json.Marshal(configValueRequest{Value: value})
	if err != nil {
		return configEntry{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPut, endpoint, bytes.NewReader(payload), http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return configEntry{}, errConfigKeyNotFound
		}
		return configEntry{}, err
	}
	defer resp.Body.Close()
	var entry configEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return configEntry{}, fmt.Errorf("decode config value: %w", err)
	}
	return entry, nil
}

func writeConfigEntries(w io.Writer, entries []configEntry, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "KEY:{.Key},VALUE:{.Value}",
		WideColumnSpec:    "KEY:{.Key},VALUE:{.Value}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(entries, flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}
