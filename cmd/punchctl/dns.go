package main

import "github.com/spf13/cobra"

func newDNSCommand(cfg *commandConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Inspect DNS state",
	}
	cmd.AddCommand(newUpstreamsCommand(cfg))
	cmd.AddCommand(newRulesCommand(cfg))
	cmd.AddCommand(newRoutesCommand(cfg))
	cmd.AddCommand(newFakeIPsCommand(cfg))
	cmd.AddCommand(newCacheCommand(cfg))
	cmd.AddCommand(newResolveCommand(cfg))
	cmd.AddCommand(newTraceCommand(cfg))
	return cmd
}
