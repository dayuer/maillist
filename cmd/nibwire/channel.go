package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/channelspec"
	"nibwire/internal/core/model"
	"nibwire/internal/core/rules"
	"nibwire/internal/store"
)

func newChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Configure the channels mail can go out through",
		Long: "The default channel is the SES_* settings. Other channels are stored here; their secrets " +
			"stay in environment variables, and only the variables' prefix is stored.",
	}

	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Configure a channel (a running server loads it on restart)",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannelAdd,
	}
	add.Flags().String("type", "", "ses (required)")
	add.Flags().String("secret-env", "", "prefix of the variables that hold its secrets (required)")
	add.Flags().String("region", "", "ses: the AWS region")
	add.Flags().String("configuration-set", "", "ses: the configuration set events are published through")
	add.Flags().String("tenant", "", "ses: the tenant to send through")
	add.Flags().String("endpoint", "", "ses: a service endpoint other than AWS's own")
	add.Flags().Int("max-per-second", 0, "this channel's own send rate ceiling (0 = none)")
	add.Flags().Int("max-per-day", 0, "this channel's own daily ceiling (0 = none)")
	for _, required := range []string{"type", "secret-env"} {
		if err := add.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the channels, default first",
		Args:  cobra.NoArgs,
		RunE:  runChannelList,
	}

	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one channel and whether its secret variables are set",
		Args:  cobra.ExactArgs(1),
		RunE:  runChannelShow,
	}

	test := &cobra.Command{
		Use:   "test <name>",
		Short: "Send one email through a channel, outside any campaign",
		Long: "The email bypasses the suppression list and carries no message id, so delivery, bounce " +
			"and complaint events for it are not recorded. Use an address you own.",
		Args: cobra.ExactArgs(1),
		RunE: runChannelTest,
	}
	test.Flags().String("from", "", "sender address on a domain the channel has verified (required)")
	test.Flags().String("to", "", "recipient (required)")
	for _, required := range []string{"from", "to"} {
		if err := test.MarkFlagRequired(required); err != nil {
			panic(err)
		}
	}

	enable := &cobra.Command{
		Use:   "enable <name>",
		Short: "Put a channel back in service",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return setChannelEnabled(cmd, args[0], true) },
	}
	disable := &cobra.Command{
		Use:   "disable <name>",
		Short: "Take a channel out of service",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return setChannelEnabled(cmd, args[0], false) },
	}

	cmd.AddCommand(add, list, show, test, enable, disable)
	return cmd
}

// channelView is what --json prints for a channel. Secrets appear only as their variable names and
// whether they are set.
type channelView struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Enabled      bool              `json:"enabled"`
	Problem      string            `json:"problem,omitempty"` // why an enabled channel could not be built
	Source       string            `json:"source"`            // "SES_* settings" or the secret prefix
	Params       map[string]string `json:"params,omitempty"`
	Secrets      map[string]bool   `json:"secrets,omitempty"` // variable name → set
	MaxPerSecond int               `json:"maxPerSecond"`
	MaxPerDay    int               `json:"maxPerDay"`
}

const defaultSource = "SES_* settings"

func defaultView() channelView {
	return channelView{Name: model.DefaultChannel, Type: channelspec.TypeSES, Enabled: true, Source: defaultSource}
}

func viewOf(a *app.App, row store.ChannelRow) channelView {
	id, secret := channelspec.SESKeys(row.SecretEnv)
	problem := ""
	if err := a.ChannelProblems[row.Name]; err != nil {
		problem = err.Error()
	}
	return channelView{
		Name: row.Name, Type: row.Type, Enabled: row.Enabled, Problem: problem, Source: row.SecretEnv, Params: row.Params,
		Secrets:      map[string]bool{id: a.Config.Secret(id) != "", secret: a.Config.Secret(secret) != ""},
		MaxPerSecond: row.MaxPerSecond, MaxPerDay: row.MaxPerDay,
	}
}

// stateOf is enabled, disabled, or broken (enabled but not buildable).
func stateOf(view channelView) string {
	if view.Problem != "" {
		return "broken"
	}
	return enabledWord(view.Enabled)
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func runChannelAdd(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()
	kind, _ := flags.GetString("type")
	secretEnv, _ := flags.GetString("secret-env")
	perSecond, _ := flags.GetInt("max-per-second")
	perDay, _ := flags.GetInt("max-per-day")
	params := map[string]string{}
	for flag, key := range map[string]string{"region": "region", "configuration-set": "configuration_set",
		"tenant": "tenant", "endpoint": "endpoint"} {
		if value, _ := flags.GetString(flag); value != "" {
			params[key] = value
		}
	}
	row := store.ChannelRow{Name: args[0], Type: kind, SecretEnv: secretEnv, Params: params,
		MaxPerSecond: perSecond, MaxPerDay: perDay, Enabled: true}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if err := channelspec.Validate(app.SpecOf(row), a.Config.Secret); err != nil {
			return err
		}
		if err := a.Store.CreateChannel(ctx, row); err != nil {
			return err
		}
		note(cmd, a, "channel.add", row.Name, map[string]any{"name": row.Name, "type": row.Type,
			"secretEnv": row.SecretEnv, "params": row.Params})
		if jsonOutput(cmd) {
			return printJSON(cmd, viewOf(a, row))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added %s; restart serve to load it\n", row.Name)
		return nil
	})
}

func runChannelList(cmd *cobra.Command, _ []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		rows, err := a.Store.Channels(ctx)
		if err != nil {
			return err
		}
		views := []channelView{defaultView()}
		for _, row := range rows {
			views = append(views, viewOf(a, row))
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, views)
		}
		for _, view := range views {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
				view.Name, view.Type, stateOf(view), view.Source)
		}
		return nil
	})
}

func runChannelShow(cmd *cobra.Command, args []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		view := defaultView()
		if args[0] != model.DefaultChannel {
			row, err := a.Store.Channel(ctx, args[0])
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no channel named %s", args[0])
			}
			if err != nil {
				return err
			}
			view = viewOf(a, row)
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, view)
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "name            %s\ntype            %s\nstate           %s\nsource          %s\n",
			view.Name, view.Type, stateOf(view), view.Source)
		if view.Problem != "" {
			_, _ = fmt.Fprintf(out, "problem         %s\n", view.Problem)
		}
		for _, key := range sortedKeys(view.Params) {
			_, _ = fmt.Fprintf(out, "%-15s %s\n", key, view.Params[key])
		}
		for _, name := range sortedSecretNames(view.Secrets) {
			state := "missing"
			if view.Secrets[name] {
				state = "set"
			}
			_, _ = fmt.Fprintf(out, "%s  %s\n", name, state)
		}
		_, _ = fmt.Fprintf(out, "max per second  %d\nmax per day     %d\n", view.MaxPerSecond, view.MaxPerDay)
		return nil
	})
}

func runChannelTest(cmd *cobra.Command, args []string) error {
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		channel, ok := a.Channels.Get(args[0])
		if !ok {
			if problem := a.ChannelProblems[args[0]]; problem != nil {
				return fmt.Errorf("%s cannot be built: %w", args[0], problem)
			}
			if row, err := a.Store.Channel(ctx, args[0]); err == nil && !row.Enabled {
				return fmt.Errorf("%s is disabled", args[0])
			}
			return fmt.Errorf("no channel named %s", args[0])
		}
		subject := "Nibwire channel test: " + channel.Name()
		id, err := channel.Send(ctx, rules.RecipientEmail{
			From: from, To: to, Subject: subject,
			HTML: "<p>This is a test of the " + channel.Name() + " channel.</p>",
			Text: "This is a test of the " + channel.Name() + " channel.",
		}, map[string]string{"kind": string(model.KindTest)})
		if err != nil {
			return fmt.Errorf("%s refused it (%s): %w", channel.Name(), channel.Classify(err), err)
		}
		note(cmd, a, "channel.test", channel.Name(), map[string]any{"channel": channel.Name(), "to": to})
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "sent through %s (id %s)\n", channel.Name(), id)
		return nil
	})
}

func setChannelEnabled(cmd *cobra.Command, name string, enabled bool) error {
	if name == model.DefaultChannel {
		return errors.New("the default channel is the SES_* settings; it is turned on and off with SENDING_ENABLED")
	}
	if !enabled {
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes && !confirm(cmd) {
			return errors.New("aborted")
		}
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		err := a.Store.SetChannelEnabled(ctx, name, enabled)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no channel named %s", name)
		}
		if err != nil {
			return err
		}
		action := "channel." + strings.TrimSuffix(enabledWord(enabled), "d")
		note(cmd, a, action, name, map[string]any{"name": name})
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s; restart serve for it to take effect\n",
			enabledWord(enabled), name)
		return nil
	})
}

func sortedSecretNames(secrets map[string]bool) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) { sort.Strings(values) }
