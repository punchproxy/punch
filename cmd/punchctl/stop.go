package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newStopCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Ask punchd to shut down gracefully",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := requestShutdown(c.Context(), *cfg); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "punchd shutdown requested")
			return nil
		},
	}
}

func requestShutdown(ctx context.Context, cfg commandConfig) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/shutdown")
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodPost, endpoint, nil, http.StatusAccepted)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
