package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/route"
	"nibwire/internal/store"
)

func newRouteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Decide which channel each message goes out through",
		Long: "Rules are tried in order and the first one that matches decides the channel; the one " +
			"--default rule matches everything and is always last. Nothing sends by these rules yet: " +
			"this release stores them, and assigning a channel at send time follows.",
	}

	add := &cobra.Command{
		Use:   "add",
		Short: "Add a routing rule",
		Args:  cobra.NoArgs,
		RunE:  runRouteAdd,
	}
	add.Flags().String("match", "", "what it matches: domain:example.org or mailbox:gmail")
	add.Flags().Bool("default", false, "match everything (the default rule is always last)")
	add.Flags().String("to", "", "send through this channel")
	add.Flags().String("split", "", "split between channels by weight: aws2:80,resend:20")

	remove := &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a routing rule",
		Args:  cobra.ExactArgs(1),
		RunE:  runRouteRemove,
	}

	move := &cobra.Command{
		Use:   "move <id>",
		Short: "Put a routing rule in front of another one",
		Args:  cobra.ExactArgs(1),
		RunE:  runRouteMove,
	}
	move.Flags().String("before", "", "the rule it goes in front of (required)")
	if err := move.MarkFlagRequired("before"); err != nil {
		panic(err)
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the rules in the order they are tried",
		Args:  cobra.NoArgs,
		RunE:  runRouteList,
	}

	cmd.AddCommand(add, remove, move, list)
	return cmd
}

// routeView is what --json prints for a rule. A rule's place in the order is its place in the list.
type routeView struct {
	ID      string         `json:"id"`
	Match   string         `json:"match"`
	Targets []route.Target `json:"targets"`
	Problem string         `json:"problem,omitempty"` // a channel that is gone or disabled
}

func viewOfRoute(row store.RouteRow, problem string) routeView {
	return routeView{ID: row.ID, Match: row.Match.String(), Targets: row.Targets, Problem: problem}
}

// targetsText writes targets the way the flags take them: a lone channel, or channel and weight.
func targetsText(targets []route.Target) string {
	if len(targets) == 1 {
		return targets[0].Channel
	}
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, fmt.Sprintf("%s:%d", target.Channel, target.Weight))
	}
	return strings.Join(parts, ",")
}

func runRouteAdd(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	matchText, _ := flags.GetString("match")
	isDefault, _ := flags.GetBool("default")
	to, _ := flags.GetString("to")
	split, _ := flags.GetString("split")

	match, err := routeMatch(matchText, isDefault)
	if err != nil {
		return err
	}
	targets, err := routeTargets(to, split)
	if err != nil {
		return err
	}
	row := store.RouteRow{ID: ids.New(), Match: match, Targets: targets}
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		if err := requireChannels(ctx, a, targets); err != nil {
			return err
		}
		if err := a.Store.CreateRoute(ctx, row); err != nil {
			if errors.Is(err, store.ErrDefaultRouteExists) {
				return errors.New("there is already a default rule: remove it before adding another")
			}
			return err
		}
		note(cmd, a, "route.add", row.ID, map[string]any{"id": row.ID, "match": match.String(),
			"targets": targetsText(targets)})
		if jsonOutput(cmd) {
			return printJSON(cmd, viewOfRoute(row, ""))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added %s: %s -> %s\n", row.ID, match, targetsText(targets))
		return nil
	})
}

func runRouteList(cmd *cobra.Command, _ []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		rows, err := a.Store.Routes(ctx)
		if err != nil {
			return err
		}
		views := make([]routeView, 0, len(rows))
		for _, row := range rows {
			problem, err := routeProblem(ctx, a, row)
			if err != nil {
				return err
			}
			views = append(views, viewOfRoute(row, problem))
		}
		if jsonOutput(cmd) {
			return printJSON(cmd, views)
		}
		if len(views) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(),
				"no rules: every message goes through the default channel")
			return nil
		}
		for _, view := range views {
			state := "ok"
			if view.Problem != "" {
				state = view.Problem
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
				view.ID, view.Match, targetsText(view.Targets), state)
		}
		return nil
	})
}

func runRouteRemove(cmd *cobra.Command, args []string) error {
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		row, err := a.Store.Route(ctx, args[0])
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no route with id %s", args[0])
		}
		if err != nil {
			return err
		}
		if err := a.Store.DeleteRoute(ctx, args[0]); err != nil {
			return err
		}
		note(cmd, a, "route.remove", row.ID, map[string]any{"id": row.ID, "match": row.Match.String()})
		if jsonOutput(cmd) {
			return printJSON(cmd, viewOfRoute(row, ""))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %s: %s -> %s\n",
			row.ID, row.Match, targetsText(row.Targets))
		return nil
	})
}

func runRouteMove(cmd *cobra.Command, args []string) error {
	before, _ := cmd.Flags().GetString("before")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		// Both ids are named before anything moves, so that a typo says which one it did not find.
		for _, id := range []string{args[0], before} {
			if _, err := a.Store.Route(ctx, id); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no route with id %s", id)
			} else if err != nil {
				return err
			}
		}
		if err := a.Store.MoveRoute(ctx, args[0], before); err != nil {
			return err
		}
		note(cmd, a, "route.move", args[0], map[string]any{"id": args[0], "before": before})
		if jsonOutput(cmd) {
			row, err := a.Store.Route(ctx, args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd, viewOfRoute(row, ""))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "moved %s in front of %s\n", args[0], before)
		return nil
	})
}

// routeMatch reads the two ways of saying what a rule matches, which are mutually exclusive.
func routeMatch(text string, isDefault bool) (route.Match, error) {
	switch {
	case isDefault && text != "":
		return route.Match{}, errors.New("give either --default or --match, not both")
	case isDefault:
		return route.Match{Default: true}, nil
	case text == "":
		return route.Match{}, errors.New("give --match, or --default for a rule that matches everything")
	}
	return route.ParseMatch(text)
}

// routeTargets reads the two ways of saying where a rule sends, which are mutually exclusive.
func routeTargets(to, split string) ([]route.Target, error) {
	switch {
	case to != "" && split != "":
		return nil, errors.New("give either --to or --split, not both")
	case split != "":
		return route.ParseSplit(split)
	case to != "":
		// One channel: the weight is not a share of anything, and 1 is what the stored form carries.
		return []route.Target{{Channel: to, Weight: 1}}, nil
	default:
		return nil, errors.New("give --to <channel>, or --split <channel:weight,...>")
	}
}

// requireChannels refuses a rule that points at a channel that cannot send: a name nobody
// registered, or one that is turned off. The default channel is the SES_* settings — always there.
func requireChannels(ctx context.Context, a *app.App, targets []route.Target) error {
	for _, target := range targets {
		if target.Channel == model.DefaultChannel {
			continue
		}
		row, err := a.Store.Channel(ctx, target.Channel)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no channel named %s", target.Channel)
		}
		if err != nil {
			return err
		}
		if !row.Enabled {
			return fmt.Errorf("channel %s is disabled", target.Channel)
		}
	}
	return nil
}

// routeProblem is why a rule cannot send as it stands: a channel that was never registered, or one
// that has since been turned off. An empty answer is a rule that is fine.
func routeProblem(ctx context.Context, a *app.App, row store.RouteRow) (string, error) {
	var problems []string
	for _, target := range row.Targets {
		if target.Channel == model.DefaultChannel {
			continue
		}
		channel, err := a.Store.Channel(ctx, target.Channel)
		switch {
		case errors.Is(err, store.ErrNotFound):
			problems = append(problems, "no channel named "+target.Channel)
		case err != nil:
			return "", err
		case !channel.Enabled:
			problems = append(problems, "channel "+target.Channel+" is disabled")
		}
	}
	return strings.Join(problems, "; "), nil
}
