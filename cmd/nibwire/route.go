package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"nibwire/internal/app"
	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
	"nibwire/internal/core/route"
	"nibwire/internal/core/rules"
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
	cmd.AddCommand(newRouteFallbackCmd()) // route_fallback.go

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

	test := &cobra.Command{
		Use:   "test",
		Short: "Show where a newsletter's next issue would go, and what would stop it",
		Long: "It reads the live audience and applies the rules to it, rule by rule, so the numbers are " +
			"the ones the next send would produce. Nothing is queued and nothing is sent.",
		Args: cobra.NoArgs,
		RunE: runRouteTest,
	}
	test.Flags().String("newsletter", "", "the newsletter's slug (required)")
	if err := test.MarkFlagRequired("newsletter"); err != nil {
		panic(err)
	}

	cmd.AddCommand(add, remove, move, list, test)
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
			return printFallbacks(ctx, cmd, a)
		}
		for _, view := range views {
			state := "ok"
			if view.Problem != "" {
				state = view.Problem
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n",
				view.ID, view.Match, targetsText(view.Targets), state)
		}
		return printFallbacks(ctx, cmd, a)
	})
}

// routeTestView is what --json prints: where a newsletter's next issue would go, and what would stop
// those messages.
type routeTestView struct {
	Newsletter string          `json:"newsletter"`
	Audience   int             `json:"audience"`
	Rules      []routeRuleView `json:"rules"`
	Unrouted   int             `json:"unrouted"` // came to no rule: the default channel
	Problems   []string        `json:"problems,omitempty"`
}

// routeRuleView is one rule, and what it takes of the audience.
type routeRuleView struct {
	ID      string         `json:"id"`
	Match   string         `json:"match"`
	Targets []route.Target `json:"targets"`
	Count   int            `json:"count"`
	Split   map[string]int `json:"split,omitempty"` // per channel, when the rule splits
}

func runRouteTest(cmd *cobra.Command, _ []string) error {
	slug, _ := cmd.Flags().GetString("newsletter")
	return withApp(cmd, func(ctx context.Context, a *app.App) error {
		newsletter, err := a.Store.NewsletterBySlug(ctx, slug)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no newsletter with slug %s", slug)
		}
		if err != nil {
			return err
		}
		rules, err := a.Store.RoutesAsRules(ctx)
		if err != nil {
			return err
		}
		audience, err := a.Store.SelectAudience(ctx, newsletter.ID, 0)
		if err != nil {
			return err
		}

		// One pass over the audience, the same decision the queue would make for each of them.
		view := routeTestView{Newsletter: newsletter.Slug, Audience: len(audience),
			Rules: make([]routeRuleView, 0, len(rules))}
		splits := make([]map[string]int, len(rules))
		for i, rule := range rules {
			splits[i] = map[string]int{}
			view.Rules = append(view.Rules, routeRuleView{ID: rule.ID, Match: rule.Match.String(),
				Targets: rule.Targets})
		}
		positions := map[string]int{}
		for i, rule := range rules {
			positions[rule.ID] = i
		}
		for _, contact := range audience {
			rule, ok := route.PickRule(rules, contact.ID, contact.Email)
			if !ok {
				view.Unrouted++
				continue
			}
			index := positions[rule.ID]
			splits[index][rule.Channel(contact.ID, contact.Email)]++
			view.Rules[index].Count++
		}
		for i, rule := range rules {
			if len(rule.Targets) <= 1 {
				continue
			}
			// Every channel the rule splits between, including the ones this audience put nothing on:
			// "aws2 0" is the answer an operator is looking for when a share looks wrong.
			for _, target := range rule.Targets {
				if _, seen := splits[i][target.Channel]; !seen {
					splits[i][target.Channel] = 0
				}
			}
			view.Rules[i].Split = splits[i]
		}

		problems, err := routeTestProblems(ctx, a, newsletter, view)
		if err != nil {
			return err
		}
		view.Problems = problems

		if jsonOutput(cmd) {
			return printJSON(cmd, view)
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "audience %d\n", view.Audience)
		for _, rule := range view.Rules {
			line := fmt.Sprintf("%s\t%s\t-> %s\t%d", rule.ID, rule.Match, targetsText(rule.Targets), rule.Count)
			if len(rule.Split) > 1 {
				line += fmt.Sprintf(" (%s)", splitText(rule.Split))
			}
			_, _ = fmt.Fprintln(out, line)
		}
		_, _ = fmt.Fprintf(out, "(no rule)\t-> %s\t%d\n", model.DefaultChannel, view.Unrouted)
		for _, problem := range problems {
			_, _ = fmt.Fprintln(out, problem)
		}
		return nil
	})
}

// routeTestProblems is what would stop the messages these rules produce: a channel this deployment
// cannot send through, one that has not verified the sending domain, and one whose own day is too
// small for what it would be asked to carry. The account's own 24-hour allowance is not read here:
// `nibwire quota` is where that lives, and it is a question for the provider rather than the database.
func routeTestProblems(ctx context.Context, a *app.App, newsletter model.Newsletter,
	view routeTestView) ([]string, error) {
	used := map[string]int{}
	for _, rule := range view.Rules {
		if len(rule.Split) > 0 {
			for channel, n := range rule.Split {
				used[channel] += n
			}
			continue
		}
		for _, target := range rule.Targets {
			used[target.Channel] += rule.Count
		}
	}
	used[model.DefaultChannel] += view.Unrouted

	domain := rules.DomainOf(newsletter.FromEmail)
	reserved, err := a.Store.ReservedByChannel(ctx, store.Day(time.Now()), "")
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, channel := range sortedChannels(used) {
		if reason, err := channelProblem(ctx, a, channel); err != nil {
			return nil, err
		} else if reason != "" {
			problems = append(problems, channel+" would not send: "+reason)
			continue
		}
		identities, ok := a.IdentitiesOf(channel)
		if !ok {
			problems = append(problems, channel+" would not send: it is not loaded in this process")
			continue
		}
		verified, err := identities.VerifiedDomain(ctx, domain)
		if err != nil {
			return nil, fmt.Errorf("ask about the sending domain for %s: %w", channel, err)
		}
		if !verified {
			problems = append(problems, fmt.Sprintf("%s has not verified %s", channel, domain))
		}
		row, err := a.Store.Channel(ctx, channel)
		if errors.Is(err, store.ErrNotFound) {
			// The default channel, which is the SES_* settings and has no row: its ceiling is the
			// account's quota, which this command does not read.
			continue
		}
		if err != nil {
			return nil, err
		}
		if row.MaxPerDay > 0 && reserved[channel]+used[channel] > row.MaxPerDay {
			problems = append(problems, fmt.Sprintf(
				"%s sends at most %d a day and %d of it is reserved, so there is room for %d more and "+
					"this issue would send %d",
				channel, row.MaxPerDay, reserved[channel], max(row.MaxPerDay-reserved[channel], 0), used[channel]))
		}
	}
	return problems, nil
}

// channelProblem is why a rule's channel cannot send at all, or "" when it can.
func channelProblem(ctx context.Context, a *app.App, channel string) (string, error) {
	if channel == model.DefaultChannel {
		return "", nil
	}
	row, err := a.Store.Channel(ctx, channel)
	if errors.Is(err, store.ErrNotFound) {
		return "no channel is registered under that name", nil
	}
	if err != nil {
		return "", err
	}
	if !row.Enabled {
		return "it is disabled", nil
	}
	if problem := a.ChannelProblems[channel]; problem != nil {
		return fmt.Sprintf("it could not be loaded (%v)", problem), nil
	}
	return "", nil
}

// splitText writes a rule's per-channel counts, in channel order, zeros included.
func splitText(split map[string]int) string {
	channels := make([]string, 0, len(split))
	for channel := range split {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	parts := make([]string, 0, len(channels))
	for _, channel := range channels {
		parts = append(parts, fmt.Sprintf("%s %d", channel, split[channel]))
	}
	return strings.Join(parts, ", ")
}

// sortedChannels lists the channels of a count, in a stable order.
func sortedChannels(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for channel, n := range counts {
		if n > 0 {
			out = append(out, channel)
		}
	}
	sort.Strings(out)
	return out
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

// routeTargets reads the two ways of saying where a rule sends, which are mutually exclusive. Both
// are trimmed first, so that a channel name copied with a space around it is the channel name and
// not a channel nobody registered.
func routeTargets(to, split string) ([]route.Target, error) {
	to, split = strings.TrimSpace(to), strings.TrimSpace(split)
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
