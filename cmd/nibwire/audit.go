package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/audit"
	"nibwire/internal/store"
)

// The audit log answers "who sent that" and "when did this change", which is the question a
// credential's owner cannot answer for themselves.
func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Read the audit log",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List recorded actions, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			action, _ := flags.GetString("action")
			actor, _ := flags.GetString("actor")
			limit, _ := flags.GetInt("limit")
			offset, _ := flags.GetInt("offset")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				entries, err := audit.List(ctx, a.Store, store.AuditFilter{
					Action: action, Actor: actor, Limit: limit, Offset: offset,
				})
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, entries)
				}
				for _, entry := range entries {
					digest := entry.ParamsDigest
					if len(digest) > 12 {
						digest = digest[:12]
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n",
						entry.CreatedAt.UTC().Format(time.RFC3339), entry.ActorKind, entry.ActorName,
						entry.Action, digest, entry.Result)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d entries\n", len(entries))
				return nil
			})
		},
	}
	list.Flags().String("action", "", "only this action, exactly")
	list.Flags().String("actor", "", "only cli, mcp, api or system")
	list.Flags().Int("limit", 100, "how many to show")
	list.Flags().Int("offset", 0, "skip this many")

	cmd.AddCommand(list)
	return cmd
}

// note records an action under the CLI actor. It is the one-liner every writing command ends with.
func note(cmd *cobra.Command, a *app.App, action, result string, params any) {
	audit.Note(cmd.Context(), a.Store, audit.CLI(), action, result, params, a.Log)
}
