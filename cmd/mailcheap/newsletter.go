package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/rules"
	"mailcheap/internal/core/validate"
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

	show := &cobra.Command{
		Use:   "show <slug|id>",
		Short: "Show one newsletter",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				newsletter, err := newsletterByRef(ctx, a, args[0])
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, newsletter)
				}
				printNewsletter(cmd, newsletter)
				return nil
			})
		},
	}

	update := &cobra.Command{
		Use:   "update <slug|id>",
		Short: "Change a newsletter's settings",
		Long: "Only the flags you pass are written: the rest of the newsletter is left as it was. " +
			"This is the difference from a hypothetical `set`, and it is why an update can never " +
			"clear a field by accident.",
		Args: cobra.ExactArgs(1),
		RunE: runNewsletterUpdate,
	}
	updateFlags := update.Flags()
	updateFlags.String("slug", "", "URL slug, unique")
	updateFlags.String("name", "", "display name")
	updateFlags.String("image-url", "", "header image")
	updateFlags.String("from-name", "", "display name on the From header")
	updateFlags.String("from-email", "", "address the newsletter is sent from")
	updateFlags.String("reply-to", "", "Reply-To address")
	updateFlags.String("postal-address", "", "postal address in the footer")
	updateFlags.String("description", "", "archive description")
	updateFlags.String("author-bio", "", "archive author bio")
	updateFlags.String("author-image-url", "", "archive author image")
	updateFlags.StringSlice("social-links", nil, "archive social links")
	updateFlags.Bool("archive-enabled", false, "publish an archive of past issues")
	updateFlags.Bool("require-confirmation", false, "new subscribers confirm by email before joining")
	updateFlags.Bool("cleanup-enabled", false, "prune the least engaged contacts")
	updateFlags.Int("cleanup-issues", 0, "issues a contact may ignore before pruning (0 = default)")
	updateFlags.Int("cleanup-days", 0, "days a contact may stay quiet before pruning (0 = default)")
	updateFlags.Bool("welcome-enabled", false, "send a welcome email to new subscribers")
	updateFlags.String("welcome-subject", "", "welcome email subject")
	updateFlags.String("welcome-file", "", "file holding the welcome email's Markdown")
	updateFlags.String("confirmation-subject", "", "confirmation email subject")
	updateFlags.String("confirmation-file", "", "file holding the confirmation email's Markdown (empty = built-in)")

	cmd.AddCommand(create, list, show, update)
	return cmd
}

// printNewsletter writes the fields an operator changes and the ones a send depends on.
func printNewsletter(cmd *cobra.Command, n model.Newsletter) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "id              %s\n", n.ID)
	_, _ = fmt.Fprintf(out, "slug            %s\n", n.Slug)
	_, _ = fmt.Fprintf(out, "name            %s\n", n.Name)
	_, _ = fmt.Fprintf(out, "from            %s\n", rules.FormatFromAddress(n.FromName, n.FromEmail))
	_, _ = fmt.Fprintf(out, "reply-to        %s\n", n.ReplyTo)
	_, _ = fmt.Fprintf(out, "postal address  %s\n", n.PostalAddress)
	_, _ = fmt.Fprintf(out, "archive         %t\n", n.ArchiveEnabled)
	_, _ = fmt.Fprintf(out, "confirmation    %t\n", n.RequireConfirmation)
	_, _ = fmt.Fprintf(out, "welcome         %t  %q\n", n.WelcomeEnabled, n.Welcome.Subject)
	_, _ = fmt.Fprintf(out, "cleanup         %t  issues %d  days %d\n",
		n.CleanupEnabled, n.CleanupIssues, n.CleanupDays)
	_, _ = fmt.Fprintf(out, "created         %s\n", n.CreatedAt.UTC().Format(time.RFC3339))
}

// runNewsletterUpdate applies only the flags the operator actually passed.
func runNewsletterUpdate(cmd *cobra.Command, args []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := newsletterByRef(ctx, a, args[0])
		if err != nil {
			return err
		}
		flags := cmd.Flags()
		var changed []string
		apply := func(name string, set func(string)) {
			if !flags.Changed(name) {
				return
			}
			value, _ := flags.GetString(name)
			set(value)
			changed = append(changed, name)
		}
		apply("slug", func(v string) { newsletter.Slug = strings.ToLower(strings.TrimSpace(v)) })
		apply("name", func(v string) { newsletter.Name = strings.TrimSpace(v) })
		apply("image-url", func(v string) { newsletter.ImageURL = v })
		apply("from-name", func(v string) { newsletter.FromName = strings.TrimSpace(v) })
		apply("from-email", func(v string) { newsletter.FromEmail = strings.ToLower(strings.TrimSpace(v)) })
		apply("reply-to", func(v string) { newsletter.ReplyTo = v })
		apply("postal-address", func(v string) { newsletter.PostalAddress = v })
		apply("description", func(v string) { newsletter.Description = v })
		apply("author-bio", func(v string) { newsletter.AuthorBio = v })
		apply("author-image-url", func(v string) { newsletter.AuthorImageURL = v })
		apply("welcome-subject", func(v string) { newsletter.Welcome.Subject = strings.TrimSpace(v) })
		apply("confirmation-subject", func(v string) { newsletter.Confirmation.Subject = strings.TrimSpace(v) })
		for _, name := range []string{"archive-enabled", "require-confirmation", "cleanup-enabled", "welcome-enabled"} {
			if !flags.Changed(name) {
				continue
			}
			value, _ := flags.GetBool(name)
			switch name {
			case "archive-enabled":
				newsletter.ArchiveEnabled = value
			case "require-confirmation":
				newsletter.RequireConfirmation = value
			case "cleanup-enabled":
				newsletter.CleanupEnabled = value
			case "welcome-enabled":
				newsletter.WelcomeEnabled = value
			}
			changed = append(changed, name)
		}
		for _, name := range []string{"cleanup-issues", "cleanup-days"} {
			if !flags.Changed(name) {
				continue
			}
			value, _ := flags.GetInt(name)
			if value < 0 {
				return fmt.Errorf("--%s cannot be negative", name)
			}
			if name == "cleanup-issues" {
				newsletter.CleanupIssues = value
			} else {
				newsletter.CleanupDays = value
			}
			changed = append(changed, name)
		}
		if flags.Changed("social-links") {
			newsletter.SocialLinks, _ = flags.GetStringSlice("social-links")
			changed = append(changed, "social-links")
		}
		// The two email bodies come from files: a subject with no body is the mistake this prevents.
		for _, pair := range []struct {
			flag, label string
			body        *string
		}{
			{"welcome-file", "welcome", &newsletter.Welcome.Markdown},
			{"confirmation-file", "confirmation", &newsletter.Confirmation.Markdown},
		} {
			if !flags.Changed(pair.flag) {
				continue
			}
			path, _ := flags.GetString(pair.flag)
			body, err := readBodyFile(path)
			if err != nil {
				return err
			}
			*pair.body = body
			changed = append(changed, pair.flag)
		}
		if newsletter.FromEmail == "" {
			return fmt.Errorf("the newsletter needs a from address")
		}
		if len(changed) == 0 {
			return fmt.Errorf("nothing to change: pass at least one flag")
		}
		newsletter.UpdatedAt = time.Now().UTC()
		if err := a.Store.UpdateNewsletter(ctx, newsletter); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, newsletter)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updated %s (%s)\n", newsletter.Slug, strings.Join(changed, ", "))
		return nil
	})
}

// readBodyFile reads a Markdown body: empty means "leave it blank", which is how the confirmation
// email goes back to the built-in text.
func readBodyFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return "", err
	}
	if len(body) > validate.MaxBodyBytes {
		return "", fmt.Errorf("%s is %d bytes; the limit is %d", path, len(body), validate.MaxBodyBytes)
	}
	return string(body), nil
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

	// The address SES sees on the wire goes through the same normalization as every other address
	// the operator types: a From: header with a trailing dot or a doubled @ is a send failure waiting
	// for the first issue (the phase 1-3 audit's Minor pool).
	fromEmail = validate.NormalizeEmail(fromEmail)
	if fromEmail == "" {
		return fmt.Errorf("--from-email is required, and must be an address SES can send from")
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
