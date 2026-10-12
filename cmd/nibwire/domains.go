package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/mailer"
)

// verifiedDomains is the list of domains SES considers verified, or an error the operator can act
// on. A send must not proceed on an unverified domain: SES accepts the API call and then drops the
// mail, which looks like a successful send and is not.
//
// The filtering itself is mailer.VerifiedDomains, which is what the MCP surface's pre-send checks
// use: two implementations of "which of these may send" would be two answers to one question
// (phase 9 #44).
func verifiedDomains(ctx context.Context, a *app.App) ([]string, error) {
	identities, err := a.Identities.Identities(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask SES for the sending identities: %w", err)
	}
	return mailer.VerifiedDomains(identities), nil
}

func newDomainsCmd() *cobra.Command {
	domains := &cobra.Command{
		Use:   "domains",
		Short: "Show the sending identities and their DNS records",
		Long: "What SES thinks of the sending domains: whether they are verified, what DKIM says, and " +
			"the records that still have to be published. The pre-send check asks the same source, so " +
			"a domain that is not verified here cannot send.",
		Args: cobra.NoArgs,
		RunE: runDomains,
	}
	domains.Flags().Bool("fresh", false, "ask SES again instead of using the five-minute cache")
	return domains
}

func runDomains(cmd *cobra.Command, _ []string) error {
	fresh, _ := cmd.Flags().GetBool("fresh")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if fresh {
			if resettable, ok := a.Identities.(*mailer.SESIdentity); ok {
				resettable.Invalidate()
			}
		}
		identities, err := a.Identities.Identities(ctx)
		if err != nil {
			// Not swallowed: a send refuses when the domain state cannot be read, so the operator
			// needs to know that asking failed rather than seeing an empty list.
			return fmt.Errorf("ask SES for the sending identities: %w", err)
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, identities)
		}
		if len(identities) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no sending identities: verify a domain in SES first")
			return nil
		}
		for _, identity := range identities {
			state := "not verified"
			if identity.VerifiedForSend {
				state = "verified"
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s   %s   DKIM %s", identity.Domain, state, identity.DKIMStatus)
			if identity.MailFromDomain != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "   MAIL FROM %s (%s)",
					identity.MailFromDomain, identity.MailFromStatus)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
			for _, record := range identity.Records {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-5s %s   %s\n", record.Type, record.Name, record.Value)
			}
		}
		return nil
	})
}
