package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
)

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Report whether the database answers",
		Long:  "What a container HEALTHCHECK runs: one query against the database. Exit code 0 when it answers.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				if err := a.Store.Ping(ctx); err != nil {
					return fmt.Errorf("degraded: %w", err)
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, map[string]any{"status": "ok", "db": a.Store.Path()})
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "ok")
				return nil
			})
		},
	}
}
