package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/model"
	"nibwire/internal/core/validate"
)

// The suppression list is global: an address that bounced or complained is out for every newsletter,
// which is what SES expects of an account with more than one sending identity.
func newSuppressionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suppression",
		Short: "Manage the suppression list",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List suppressed addresses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				suppressed, err := a.Store.Suppressions(ctx)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, suppressed)
				}
				for _, entry := range suppressed {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", entry.Email, entry.Reason, entry.Detail)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d suppressed\n", len(suppressed))
				return nil
			})
		},
	}

	add := &cobra.Command{
		Use:   "add <email>",
		Short: "Suppress an address",
		Long: "An address added by hand is suppressed as `manual`. It stays suppressed until somebody " +
			"removes it: sending is what the audience query excludes it from, and no command can send to " +
			"a suppressed address.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reasonFlag, _ := cmd.Flags().GetString("reason")
			detail, _ := cmd.Flags().GetString("detail")
			reason, err := parseSuppressionReason(reasonFlag)
			if err != nil {
				return err
			}
			email := validate.NormalizeEmail(args[0])
			if email == "" {
				return fmt.Errorf("%q is not an address SES can take", args[0])
			}
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				if err := a.Store.AddSuppression(ctx, email, reason, detail); err != nil {
					return err
				}
				note(cmd, a, "suppression.add", email,
					map[string]any{"email": email, "reason": string(reason)})
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "suppressed %s (%s)\n", email, reason)
				return nil
			})
		},
	}
	add.Flags().String("reason", string(model.SuppressManual), "manual, bounce or complaint")
	add.Flags().String("detail", "", "why, for the next person to look")

	remove := &cobra.Command{
		Use:   "remove <email>",
		Short: "Remove an address from the suppression list",
		Long: "Removing a bounce or complaint suppression does not put the address back on the list " +
			"anywhere: the contacts keep the status the event gave them, and only an explicit " +
			"resubscribe brings one back.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := validate.NormalizeEmail(args[0])
			if email == "" {
				return fmt.Errorf("%q is not an address SES can take", args[0])
			}
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				removed, err := a.Store.RemoveSuppression(ctx, email)
				if err != nil {
					return err
				}
				if !removed {
					return fmt.Errorf("%s is not on the suppression list", email)
				}
				note(cmd, a, "suppression.remove", email, map[string]any{"email": email})
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unsuppressed %s\n", email)
				return nil
			})
		},
	}

	cmd.AddCommand(list, add, remove)
	return cmd
}

func parseSuppressionReason(value string) (model.SuppressionReason, error) {
	reason := model.SuppressionReason(strings.ToLower(strings.TrimSpace(value)))
	switch reason {
	case model.SuppressManual, model.SuppressBounce, model.SuppressComplaint:
		return reason, nil
	default:
		return "", errors.New("--reason must be manual, bounce or complaint")
	}
}
