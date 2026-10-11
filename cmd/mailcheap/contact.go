package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/core/validate"
	"mailcheap/internal/store"
)

func newContactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contact",
		Short: "Manage contacts",
	}
	add := &cobra.Command{
		Use:   "add <email>",
		Short: "Add one contact",
		Args:  cobra.ExactArgs(1),
		RunE:  runContactAdd,
	}
	add.Flags().String("newsletter", "", "newsletter slug or id (required)")
	add.Flags().String("source", "cli", "where the contact came from")
	if err := add.MarkFlagRequired("newsletter"); err != nil {
		panic(err)
	}
	cmd.AddCommand(add, newContactListCmd(), newContactShowCmd(),
		newContactUpdateCmd(), newContactImportCmd(), newContactExportCmd())
	return cmd
}

func runContactAdd(cmd *cobra.Command, args []string) error {
	slug, _ := cmd.Flags().GetString("newsletter")
	source, _ := cmd.Flags().GetString("source")

	email := validate.NormalizeEmail(args[0])
	if email == "" {
		return fmt.Errorf("%q is not an address SES can take", args[0])
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := newsletterByRef(ctx, a, slug)
		if err != nil {
			return err
		}
		// A suppressed address never becomes active again; it joins with the status its suppression
		// implies, exactly as upstream's upsertContact does.
		status := model.ContactActive
		if newsletter.RequireConfirmation {
			status = model.ContactPending
		}
		if reason, suppressed, err := a.Store.Suppression(ctx, email); err != nil {
			return err
		} else if suppressed {
			switch reason {
			case model.SuppressBounce:
				status = model.ContactBounced
			case model.SuppressComplaint:
				status = model.ContactComplained
			default:
				status = model.ContactUnsubscribed
			}
		}

		now := time.Now().UTC()
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID, Email: email,
			Status: status, Source: source, SubscribedAt: &now, CreatedAt: now,
		}
		if err := a.Store.CreateContact(ctx, contact); err != nil {
			return err
		}
		note(cmd, a, "contact.add", contact.Email, map[string]any{
			"id": contact.ID, "newsletter": newsletter.Slug, "email": contact.Email, "source": source,
		})
		if jsonOutput(cmd) {
			return printJSON(cmd, contact)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), contact.ID)
		return nil
	})
}

// newsletterByRef accepts either a slug or an id, whichever the operator pasted.
func newsletterByRef(ctx context.Context, a *app.App, ref string) (model.Newsletter, error) {
	if ids.Valid(ref) {
		return a.Store.Newsletter(ctx, ref)
	}
	newsletter, err := a.Store.NewsletterBySlug(ctx, ref)
	if errors.Is(err, store.ErrNotFound) {
		return model.Newsletter{}, fmt.Errorf("no newsletter %q", ref)
	}
	return newsletter, err
}
