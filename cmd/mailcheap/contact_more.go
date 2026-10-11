package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/validate"
	"mailcheap/internal/store"
)

// exportColumns are the CSV headers `contact export` writes, in order.
//
// Every one of them is a name core/importer.MapColumns already recognises (it folds case and drops
// punctuation), which is what makes the exported file importable again — the round trip is the
// point, and it is tested. The counters after the importable six are for humans and reporting; an
// import ignores them.
var exportColumns = []string{
	"email", "status", "subscribed_at", "emails_received", "open_rate", "click_rate",
	"id", "source", "confirmed_at", "unsubscribed_at", "last_open_at", "last_click_at",
	"sent", "opens", "clicks",
}

func newContactListCmd() *cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: "List contacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			ref, _ := flags.GetString("newsletter")
			status, _ := flags.GetString("status")
			prefix, _ := flags.GetString("prefix")
			limit, _ := flags.GetInt("limit")
			offset, _ := flags.GetInt("offset")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				filter, err := contactFilter(ctx, a, ref, status, prefix, limit, offset)
				if err != nil {
					return err
				}
				contacts, err := a.Store.Contacts(ctx, filter)
				if err != nil {
					return err
				}
				total, err := a.Store.CountContacts(ctx, filter)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, map[string]any{"contacts": contacts, "total": total})
				}
				for _, contact := range contacts {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n",
						contact.ID, contact.Email, contact.Status, contact.Source, formatOptional(contact.SubscribedAt))
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%d shown of %d\n", len(contacts), total)
				return nil
			})
		},
	}
	flags := list.Flags()
	flags.String("newsletter", "", "only this newsletter's contacts")
	flags.String("status", "", "active, pending, unsubscribed, bounced, complained or dormant")
	flags.String("prefix", "", "email prefix search")
	flags.Int("limit", 100, "how many to show")
	flags.Int("offset", 0, "skip this many")
	return list
}

func newContactShowCmd() *cobra.Command {
	show := &cobra.Command{
		Use:   "show <id|email>",
		Short: "Show one contact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, _ := cmd.Flags().GetString("newsletter")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				contact, err := contactByRef(ctx, a, args[0], ref)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, contact)
				}
				printContact(cmd, contact)
				return nil
			})
		},
	}
	show.Flags().String("newsletter", "", "which newsletter's contact, when you pass an email")
	return show
}

func newContactUpdateCmd() *cobra.Command {
	update := &cobra.Command{
		Use:   "update <id|email>",
		Short: "Change a contact",
		Long: "Only the flags you pass are written. Changing the status this way is an operator's " +
			"deliberate edit: it is not how a reader unsubscribes, and it does not touch the " +
			"suppression list — a bounced or complained address stays suppressed either way.",
		Args: cobra.ExactArgs(1),
		RunE: runContactUpdate,
	}
	flags := update.Flags()
	flags.String("newsletter", "", "which newsletter's contact, when you pass an email")
	flags.String("email", "", "new address")
	flags.String("status", "", "new status")
	flags.String("source", "", "where the contact came from")
	return update
}

func runContactUpdate(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	ref, _ := flags.GetString("newsletter")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		contact, err := contactByRef(ctx, a, args[0], ref)
		if err != nil {
			return err
		}
		var changed []string
		if flags.Changed("email") {
			email, _ := flags.GetString("email")
			normalized := validate.NormalizeEmail(email)
			if normalized == "" {
				return fmt.Errorf("%q is not an address SES can take", email)
			}
			contact.Email = normalized
			changed = append(changed, "email")
		}
		if flags.Changed("source") {
			contact.Source, _ = flags.GetString("source")
			changed = append(changed, "source")
		}
		if flags.Changed("status") {
			status, _ := flags.GetString("status")
			parsed, err := parseContactStatus(status)
			if err != nil {
				return err
			}
			if parsed != contact.Status {
				moved, err := a.Store.UpdateContactStatus(ctx, contact.ID, parsed, nil)
				if err != nil {
					return err
				}
				if !moved {
					return fmt.Errorf("contact %s: %w", contact.ID, store.ErrNotFound)
				}
				contact.Status = parsed
			}
			changed = append(changed, "status")
		}
		if len(changed) == 0 {
			return errors.New("nothing to change: pass at least one flag")
		}
		if err := a.Store.UpdateContact(ctx, contact); err != nil {
			return err
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, contact)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updated %s (%s)\n", contact.ID, strings.Join(changed, ", "))
		return nil
	})
}

func newContactExportCmd() *cobra.Command {
	export := &cobra.Command{
		Use:   "export",
		Short: "Write contacts as CSV",
		Long: "Writes to stdout, so `mailcheap contact export --newsletter notes > contacts.csv` is " +
			"the whole story. The first six columns are the ones an import reads back, which makes the " +
			"exported file a working backup of the engagement history.\n\n" +
			"A `pending` contact exports as `pending`, and importing that file again lands it as " +
			"`dormant`: upstream's STATUS_ALIASES read a file's \"pending\" that way, and this port " +
			"keeps that rule.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cmd.Flags()
			ref, _ := flags.GetString("newsletter")
			status, _ := flags.GetString("status")
			prefix, _ := flags.GetString("prefix")
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				// An export is the whole list by definition, so the page limit is the count: the
				// store's own default would cut it at 100 rows.
				filter, err := contactFilter(ctx, a, ref, status, prefix, 0, 0)
				if err != nil {
					return err
				}
				total, err := a.Store.CountContacts(ctx, filter)
				if err != nil {
					return err
				}
				filter.Limit = total
				contacts, err := a.Store.Contacts(ctx, filter)
				if err != nil {
					return err
				}
				writer := csv.NewWriter(cmd.OutOrStdout())
				if err := writer.Write(exportColumns); err != nil {
					return err
				}
				for _, contact := range contacts {
					if err := writer.Write(contactRow(contact)); err != nil {
						return err
					}
				}
				writer.Flush()
				return writer.Error()
			})
		},
	}
	flags := export.Flags()
	flags.String("newsletter", "", "only this newsletter's contacts")
	flags.String("status", "", "only this status")
	flags.String("prefix", "", "email prefix search")
	return export
}

func contactRow(c model.Contact) []string {
	return []string{
		c.Email, string(c.Status), formatOptional(c.SubscribedAt),
		strconv.Itoa(c.EmailsReceived), floatOrBlank(c.OpenRate), floatOrBlank(c.ClickRate),
		c.ID, c.Source, formatOptional(c.ConfirmedAt), formatOptional(c.UnsubscribedAt),
		formatOptional(c.LastOpenAt), formatOptional(c.LastClickAt),
		strconv.Itoa(c.Sent), strconv.Itoa(c.Opens), strconv.Itoa(c.Clicks),
	}
}

func printContact(cmd *cobra.Command, c model.Contact) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "id               %s\n", c.ID)
	_, _ = fmt.Fprintf(out, "newsletter       %s\n", c.NewsletterID)
	_, _ = fmt.Fprintf(out, "email            %s\n", c.Email)
	_, _ = fmt.Fprintf(out, "status           %s\n", c.Status)
	_, _ = fmt.Fprintf(out, "source           %s\n", c.Source)
	_, _ = fmt.Fprintf(out, "subscribed       %s\n", formatOptional(c.SubscribedAt))
	_, _ = fmt.Fprintf(out, "confirmed        %s\n", formatOptional(c.ConfirmedAt))
	_, _ = fmt.Fprintf(out, "unsubscribed     %s\n", formatOptional(c.UnsubscribedAt))
	_, _ = fmt.Fprintf(out, "last sent        %s\n", formatOptional(c.LastSentAt))
	_, _ = fmt.Fprintf(out, "last open        %s\n", formatOptional(c.LastOpenAt))
	_, _ = fmt.Fprintf(out, "last click       %s\n", formatOptional(c.LastClickAt))
	_, _ = fmt.Fprintf(out, "counters         sent %d  opens %d  clicks %d  ignored issues %d\n",
		c.Sent, c.Opens, c.Clicks, c.IgnoredIssues)
	_, _ = fmt.Fprintf(out, "imported         received %d  open %s  click %s\n",
		c.EmailsReceived, formatRate(c.OpenRate), formatRate(c.ClickRate))
}

// contactFilter builds the store filter, resolving a newsletter slug to its id.
func contactFilter(ctx context.Context, a *app.App, ref, status, prefix string,
	limit, offset int) (store.ContactFilter, error) {
	filter := store.ContactFilter{EmailPrefix: prefix, Limit: limit, Offset: offset}
	if ref != "" {
		newsletter, err := newsletterByRef(ctx, a, ref)
		if err != nil {
			return store.ContactFilter{}, err
		}
		filter.NewsletterID = newsletter.ID
	}
	if status != "" {
		parsed, err := parseContactStatus(status)
		if err != nil {
			return store.ContactFilter{}, err
		}
		filter.Status = parsed
	}
	return filter, nil
}

// contactByRef finds a contact by id, or by email inside a newsletter.
func contactByRef(ctx context.Context, a *app.App, ref, newsletterRef string) (model.Contact, error) {
	if ids.Valid(ref) {
		return a.Store.Contact(ctx, ref)
	}
	email := validate.NormalizeEmail(ref)
	if email == "" {
		return model.Contact{}, fmt.Errorf("%q is neither a contact id nor an address", ref)
	}
	if newsletterRef == "" {
		return model.Contact{}, errors.New("pass --newsletter with an email address")
	}
	newsletter, err := newsletterByRef(ctx, a, newsletterRef)
	if err != nil {
		return model.Contact{}, err
	}
	return a.Store.ContactByEmail(ctx, newsletter.ID, email)
}

func parseContactStatus(value string) (model.ContactStatus, error) {
	status := model.ContactStatus(strings.ToLower(strings.TrimSpace(value)))
	switch status {
	case model.ContactActive, model.ContactPending, model.ContactUnsubscribed,
		model.ContactBounced, model.ContactComplained, model.ContactDormant:
		return status, nil
	default:
		return "", fmt.Errorf("unknown status %q (active, pending, unsubscribed, bounced, complained, dormant)", value)
	}
}

func formatOptional(at *time.Time) string {
	if at == nil {
		return "-"
	}
	return at.UTC().Format(time.RFC3339)
}

func formatRate(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func floatOrBlank(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}
