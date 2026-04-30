package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

const defaultAPIAddr = "http://127.0.0.1:28854"

var version = "dev"

func main() {
	cmd := newRootCommand(commandConfig{
		out:    os.Stdout,
		errOut: os.Stderr,
		client: http.DefaultClient,
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand(cfg commandConfig) *cobra.Command {
	cfg = applyDefaults(cfg)

	root := &cobra.Command{
		Use:           "punchctl",
		Short:         "Interact with a Punch server",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.SetVersionTemplate("punchctl {{.Version}}\n")
	root.SetOut(cfg.out)
	root.SetErr(cfg.errOut)
	root.PersistentFlags().StringVar(&cfg.addr, "addr", cfg.addr, "Punch API address")
	root.PersistentFlags().StringVar(&cfg.token, "token", cfg.token, "API bearer token")
	root.PersistentFlags().DurationVar(&cfg.timeout, "timeout", cfg.timeout, "request timeout")

	root.AddCommand(newConfigCommand(&cfg))
	root.AddCommand(newStatusCommand(&cfg))
	root.AddCommand(newSystemCommand(&cfg))
	root.AddCommand(newDNSCommand(&cfg))
	root.AddCommand(newRelayGroupsCommand(&cfg))
	root.AddCommand(newRelaysCommand(&cfg))
	root.AddCommand(newSessionsCommand(&cfg))
	return root
}
