package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mailcheap/internal/app"
	"mailcheap/internal/core/ids"
	"mailcheap/internal/creds"
	"mailcheap/internal/store"
)

// Credentials are the only way in for the two non-CLI surfaces (design §11, layer 1), and the CLI is
// the only place they can be created or revoked: an agent that could mint its own key would have no
// permission model at all.
func newCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Issue and revoke API and MCP credentials",
	}

	create := &cobra.Command{
		Use:   "create",
		Short: "Issue a credential",
		Long: "The token is shown once, here, and only its SHA-256 is stored: a copy of the database is " +
			"not a copy of the keys. Give an MCP or API caller the token, not the database.",
		Args: cobra.NoArgs,
		RunE: runCredentialsCreate,
	}
	create.Flags().String("kind", "", "api or mcp (required)")
	create.Flags().String("name", "", "what this key is for (required)")
	create.Flags().StringSlice("scopes", nil, "read, write and/or send (default read)")
	create.Flags().Int("daily-budget", 0, "credential-level sends per day (0 = only the global limit)")
	if err := create.MarkFlagRequired("kind"); err != nil {
		panic(err)
	}
	if err := create.MarkFlagRequired("name"); err != nil {
		panic(err)
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				credentials, err := a.Store.Credentials(ctx)
				if err != nil {
					return err
				}
				if jsonOutput(cmd) {
					return printJSON(cmd, credentials)
				}
				for _, credential := range credentials {
					state := "active"
					if credential.Revoked() {
						state = "revoked"
					}
					used := "-"
					if credential.LastUsedAt != nil {
						used = credential.LastUsedAt.UTC().Format(time.RFC3339)
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\tbudget %d\tlast used %s\n",
						credential.ID, credential.Kind, credential.Name, credential.Prefix,
						scopeList(credential.Scopes), state, credential.DailyBudget, used)
				}
				return nil
			})
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke <id|prefix>",
		Short: "Take a credential out of service",
		Long: "Revocation is immediate and needs no cooperation from the holder: every request looks " +
			"the credential up by hash, so a revoked row is simply not found.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd, func(ctx context.Context, a *app.App) error {
				credential, err := credentialByRef(ctx, a, args[0])
				if err != nil {
					return err
				}
				revoked, err := a.Store.RevokeCredential(ctx, credential.ID, time.Now().UTC())
				if err != nil {
					return err
				}
				if !revoked {
					return fmt.Errorf("%s was already revoked", credential.Name)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "revoked %s (%s)\n", credential.ID, credential.Name)
				return nil
			})
		},
	}

	cmd.AddCommand(create, list, revoke)
	return cmd
}

// credentialsView is what `create --json` prints: the one and only sight of the token.
type credentialsView struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Name        string        `json:"name"`
	Prefix      string        `json:"prefix"`
	Scopes      []creds.Scope `json:"scopes"`
	DailyBudget int           `json:"dailyBudget"`
	Token       string        `json:"token"`
}

func runCredentialsCreate(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	kind, _ := flags.GetString("kind")
	name, _ := flags.GetString("name")
	scopeValues, _ := flags.GetStringSlice("scopes")
	budget, _ := flags.GetInt("daily-budget")

	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "api" && kind != "mcp" {
		return fmt.Errorf("--kind must be api or mcp, not %q", kind)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("--name is required")
	}
	if budget < 0 {
		return fmt.Errorf("--daily-budget cannot be negative")
	}
	if len(scopeValues) == 0 {
		scopeValues = []string{string(creds.ScopeRead)}
	}
	scopes, err := creds.ParseScopes(scopeValues)
	if err != nil {
		return err
	}

	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		generated := creds.Generate()
		credential := store.Credential{
			ID: ids.New(), Kind: kind, Name: strings.TrimSpace(name),
			Prefix: generated.Prefix, TokenHash: generated.Hash,
			Scopes: scopes, DailyBudget: budget, CreatedAt: time.Now().UTC(),
		}
		if err := a.Store.CreateCredential(ctx, credential); err != nil {
			return err
		}
		view := credentialsView{
			ID: credential.ID, Kind: credential.Kind, Name: credential.Name,
			Prefix: credential.Prefix, Scopes: credential.Scopes,
			DailyBudget: credential.DailyBudget, Token: generated.Token,
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, view)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), generated.Token)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "id %s  kind %s  name %q  scopes %s  daily budget %d\n",
			credential.ID, credential.Kind, credential.Name, scopeList(credential.Scopes), credential.DailyBudget)
		_, _ = fmt.Fprintln(cmd.OutOrStdout(),
			"\nThis token is shown once. Store it now: only its SHA-256 is in the database.")
		return nil
	})
}

// credentialByRef accepts an id, the prefix a list shows, or the name it was created with — the
// three things an operator has to hand. A name that two credentials share is an error rather than a
// guess: revoking the wrong key is not a mistake worth making silently.
func credentialByRef(ctx context.Context, a *app.App, ref string) (store.Credential, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Credential{}, fmt.Errorf("name a credential by id, prefix or name")
	}
	if ids.Valid(ref) {
		return a.Store.Credential(ctx, ref)
	}
	if credential, err := a.Store.CredentialByPrefix(ctx, ref); err == nil {
		return credential, nil
	}
	return a.Store.CredentialByName(ctx, ref)
}

func scopeList(scopes []creds.Scope) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, string(scope))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}
