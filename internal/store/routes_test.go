package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"nibwire/internal/core/route"
)

// Fixed ids, so the tests below can talk about the order they expect.
const (
	routeA = "aaaaaaaaaaaaaaaaaaaaaaaa"
	routeB = "bbbbbbbbbbbbbbbbbbbbbbbb"
	routeC = "cccccccccccccccccccccccc"
	routeD = "dddddddddddddddddddddddd"
)

// routeRow builds a one-target rule out of the match the CLI would have parsed.
func routeRow(t *testing.T, id, match, channel string) RouteRow {
	t.Helper()
	parsed, err := route.ParseMatch(match)
	if err != nil {
		t.Fatal(err)
	}
	return RouteRow{ID: id, Match: parsed, Targets: []route.Target{{Channel: channel, Weight: 1}}}
}

func TestRouteRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	split := RouteRow{ID: routeA, Match: route.Match{Default: true}, Targets: []route.Target{
		{Channel: "aws2", Weight: 80}, {Channel: "default", Weight: 20}}}
	if err := db.CreateRoute(ctx, split); err != nil {
		t.Fatal(err)
	}
	got, err := db.Route(ctx, routeA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Match.String() != "default" || !slices.Equal(got.Targets, split.Targets) {
		t.Fatalf("got %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("created_at not set: %+v", got)
	}
}

// The match goes into the column in its canonical form, so one condition has one spelling on disk.
func TestRouteStoresTheCanonicalMatch(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	parsed, err := route.ParseMatch("domain:EXAMPLE.org,EXAMPLE.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoute(ctx, RouteRow{ID: routeA, Match: parsed, Targets: []route.Target{{Channel: "aws2", Weight: 1}}}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.sql.QueryRow("SELECT match FROM routes WHERE id = ?", routeA).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "domain:example.org" {
		t.Fatalf("stored match = %q, want %q", stored, "domain:example.org")
	}
}

func TestRouteIDsAreUnique(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateRoute(ctx, routeRow(t, routeA, "domain:example.org", "aws2")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoute(ctx, routeRow(t, routeA, "domain:a.co", "aws2")); !errors.Is(err, ErrRouteExists) {
		t.Fatalf("second create = %v, want ErrRouteExists", err)
	}
}

// Rules are listed in the order they are tried, and a new rule goes in front of the default one.
func TestRoutesKeepTheDefaultRuleLast(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, row := range []RouteRow{
		routeRow(t, routeA, "domain:example.org", "aws2"),
		routeRow(t, routeB, "default", "default"),
		routeRow(t, routeC, "mailbox:gmail", "back"),
	} {
		if err := db.CreateRoute(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{routeA, routeC, routeB}
	if got := routeIDs(rows); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if positions := routePositions(t, db); !slices.Equal(positions, []int{0, 1, 2}) {
		t.Fatalf("positions = %v, want 0, 1, 2", positions)
	}
}

func TestThereIsOnlyOneDefaultRoute(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateRoute(ctx, routeRow(t, routeA, "default", "default")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoute(ctx, routeRow(t, routeB, "default", "back")); !errors.Is(err, ErrDefaultRouteExists) {
		t.Fatalf("second default = %v, want ErrDefaultRouteExists", err)
	}
	// The rule that was refused was not written.
	if _, err := db.Route(ctx, routeB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Route(%s) = %v, want ErrNotFound", routeB, err)
	}
}

func TestMoveRoutePutsARuleInFrontOfAnother(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, row := range []RouteRow{
		routeRow(t, routeA, "domain:example.org", "aws2"),
		routeRow(t, routeC, "mailbox:gmail", "back"),
		routeRow(t, routeB, "default", "default"),
	} {
		if err := db.CreateRoute(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MoveRoute(ctx, routeC, routeA); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{routeC, routeA, routeB}
	if got := routeIDs(rows); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if positions := routePositions(t, db); !slices.Equal(positions, []int{0, 1, 2}) {
		t.Fatalf("positions = %v, want 0, 1, 2", positions)
	}
}

func TestMoveRouteRefusesWhatItCannotDo(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, row := range []RouteRow{
		routeRow(t, routeA, "domain:example.org", "aws2"),
		routeRow(t, routeB, "default", "default"),
	} {
		if err := db.CreateRoute(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MoveRoute(ctx, routeD, routeA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown rule = %v, want ErrNotFound", err)
	}
	if err := db.MoveRoute(ctx, routeA, routeD); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown --before = %v, want ErrNotFound", err)
	}
	if err := db.MoveRoute(ctx, routeA, routeA); err == nil {
		t.Fatal("moving a rule before itself was allowed")
	}
	// The default rule is always last, so it cannot be moved out of the way.
	if err := db.MoveRoute(ctx, routeB, routeA); !errors.Is(err, ErrDefaultRouteIsLast) {
		t.Fatalf("moving the default rule = %v, want ErrDefaultRouteIsLast", err)
	}
}

func TestDeleteRouteClosesTheGapInTheOrder(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, row := range []RouteRow{
		routeRow(t, routeA, "domain:example.org", "aws2"),
		routeRow(t, routeC, "mailbox:gmail", "back"),
		routeRow(t, routeB, "default", "default"),
	} {
		if err := db.CreateRoute(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.DeleteRoute(ctx, routeC); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := routeIDs(rows), []string{routeA, routeB}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if positions := routePositions(t, db); !slices.Equal(positions, []int{0, 1}) {
		t.Fatalf("positions = %v, want 0, 1", positions)
	}
	if err := db.DeleteRoute(ctx, routeC); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
}

// routeIDs is the order the rules are tried in, which is what the tests above are about.
func routeIDs(rows []RouteRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func routePositions(t *testing.T, db *DB) []int {
	t.Helper()
	rows, err := db.sql.Query("SELECT position FROM routes ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var position int
		if err := rows.Scan(&position); err != nil {
			t.Fatal(err)
		}
		out = append(out, position)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
