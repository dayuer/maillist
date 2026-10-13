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
	"nibwire/internal/core/route"
	"nibwire/internal/store"
)

// newRouteFallbackCmd is `route fallback`: where a channel's unsent mail goes when the channel is
// down (design §4.4). It lives apart from route.go so that the rule commands and this one can change
// independently.
func newRouteFallbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fallback <channel>",
		Short: "Set the channels a channel's unsent mail moves to when it goes down",
		Long: "The chain is tried in order, and the first channel that is in service, has verified the " +
			"sender's domain and has allowance left takes the mail. A chain is one hop: mail never moves " +
			"on to a fallback's own chain. Either side may be default. Nothing moves mail yet: this " +
			"release stores the chain, and failing over follows.",
		Args: cobra.ExactArgs(1),
		RunE: runRouteFallback,
	}
	cmd.Flags().String("to", "", "the chain, in order: default,resend")
	cmd.Flags().Bool("clear", false, "remove the channel's chain")
	return cmd
}

// fallbackView is what --json prints for one channel's chain.
type fallbackView struct {
	Channel   string   `json:"channel"`
	Fallbacks []string `json:"fallbacks"`
	Problem   string   `json:"problem,omitempty"`
}

func runRouteFallback(cmd *cobra.Command, args []string) error {
	channel := args[0]
	to, _ := cmd.Flags().GetString("to")
	clearChain, _ := cmd.Flags().GetBool("clear")
	var chain []string
	switch {
	case to != "" && clearChain:
		return errors.New("give either --to or --clear, not both")
	case clearChain:
	case to == "":
		return errors.New("give --to <channel>[,<channel>...], or --clear")
	default:
		for _, name := range strings.Split(to, ",") {
			chain = append(chain, strings.TrimSpace(name))
		}
		if err := channelspec.ValidateFallbacks(channel, chain); err != nil {
			return err
		}
	}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if channel != model.DefaultChannel {
			if _, err := a.Store.Channel(ctx, channel); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no channel named %s", channel)
			} else if err != nil {
				return err
			}
		}
		if err := requireChannels(ctx, a, fallbackTargets(chain)); err != nil {
			return err
		}
		if err := a.Store.SetChannelFallbacks(ctx, channel, chain); err != nil {
			return err
		}
		note(cmd, a, "route.fallback", channel, map[string]any{"channel": channel, "fallbacks": chain})
		if jsonOutput(cmd) {
			return printJSON(cmd, fallbackView{Channel: channel, Fallbacks: nonNil(chain)})
		}
		if len(chain) == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s has no fallback chain\n", channel)
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s falls back to %s\n", channel, strings.Join(chain, ","))
		return nil
	})
}

// fallbackTargets lets the rule checks (requireChannels, routeProblem) read a chain.
func fallbackTargets(chain []string) []route.Target {
	targets := make([]route.Target, 0, len(chain))
	for _, name := range chain {
		targets = append(targets, route.Target{Channel: name, Weight: 1})
	}
	return targets
}

// printFallbacks is the part of `route list` after the rules: one line per channel with a chain,
// and what is wrong with the chain, if anything.
func printFallbacks(ctx context.Context, cmd *cobra.Command, a *app.App) error {
	all, err := a.Store.AllChannelFallbacks(ctx)
	if err != nil {
		return err
	}
	channels := make([]string, 0, len(all))
	for channel := range all {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		problem, err := routeProblem(ctx, a, store.RouteRow{Targets: fallbackTargets(all[channel])})
		if err != nil {
			return err
		}
		state := "ok"
		if problem != "" {
			state = problem
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "fallback\t%s\t%s\t%s\n",
			channel, strings.Join(all[channel], ","), state)
	}
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
