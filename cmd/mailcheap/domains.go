package main

import (
	"context"
	"fmt"

	"mailcheap/internal/app"
)

// verifiedDomains is the list of domains SES considers verified, or an error the operator can act
// on. A send must not proceed on an unverified domain: SES accepts the API call and then drops the
// mail, which looks like a successful send and is not. The `domains` command (phase 4, task 4.7)
// prints the same list with the DKIM and MAIL FROM state.
func verifiedDomains(ctx context.Context, a *app.App) ([]string, error) {
	identities, err := a.Identities.Identities(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask SES for the sending identities: %w", err)
	}
	var verified []string
	for _, identity := range identities {
		if identity.VerifiedForSend {
			verified = append(verified, identity.Domain)
		}
	}
	return verified, nil
}
