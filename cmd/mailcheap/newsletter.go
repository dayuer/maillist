package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
)

func newNewsletterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "newsletter",
		Short: "Manage newsletters",
	}
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a newsletter",
		Args:  cobra.NoArgs,
		RunE:  runNewsletterCreate,
	}
	flags := create.Flags()
	flags.String("name", "", "display name (required)")
	flags.String("slug", "", "URL slug, unique (required)")
	flags.String("from-name", "", "display name on the From header")
	flags.String("from-email", "", "address the newsletter is sent from (required)")
	flags.String("reply-to", "", "Reply-To address")
	flags.String("postal-address", "", "postal address in the footer")
	flags.Bool("archive-enabled", false, "publish an archive of past issues")
	flags.Bool("require-confirmation", false, "new subscribers confirm by email before joining")
	for _, required := range []string{"name", "slug", "from-email"} {
		if err := create.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List newsletters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				newsletters, err := a.Store.Newsletters(ctx)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, newsletters)
				}
				for _, newsletter := range newsletters {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
						newsletter.ID, newsletter.Slug, newsletter.Name, newsletter.FromEmail)
				}
				return nil
			})
		},
	}
	cmd.AddCommand(create, list)
	return cmd
}

func runNewsletterCreate(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	name, _ := flags.GetString("name")
	slug, _ := flags.GetString("slug")
	fromName, _ := flags.GetString("from-name")
	fromEmail, _ := flags.GetString("from-email")
	replyTo, _ := flags.GetString("reply-to")
	postal, _ := flags.GetString("postal-address")
	archive, _ := flags.GetBool("archive-enabled")
	confirmation, _ := flags.GetBool("require-confirmation")

	if fromEmail = strings.ToLower(strings.TrimSpace(fromEmail)); fromEmail == "" {
		return fmt.Errorf("--from-email is required")
	}
	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: strings.ToLower(strings.TrimSpace(slug)),
		Name: strings.TrimSpace(name), FromName: strings.TrimSpace(fromName), FromEmail: fromEmail,
		ReplyTo: replyTo, PostalAddress: postal,
		ArchiveEnabled: archive, RequireConfirmation: confirmation,
		CreatedAt: now, UpdatedAt: now,
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if err := a.Store.CreateNewsletter(ctx, newsletter); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, newsletter)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), newsletter.ID)
		return nil
	})
}
