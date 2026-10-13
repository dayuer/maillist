package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"nibwire/internal/core/route"
)

// RouteRow is one routing rule (design 2026-10-12-multi-channel-sending §4.1). Rules are tried in
// the order Routes returns them.
type RouteRow struct {
	ID        string
	Match     route.Match
	Targets   []route.Target
	CreatedAt time.Time
}

var (
	// ErrRouteExists is CreateRoute refusing an id that is taken.
	ErrRouteExists = errors.New("route already exists")
	// ErrDefaultRouteExists is CreateRoute refusing a second default rule.
	ErrDefaultRouteExists = errors.New("a default rule already exists")
	// ErrDefaultRouteIsLast is MoveRoute refusing to move the default rule out of last place.
	ErrDefaultRouteIsLast = errors.New("the default rule is always last")
)

// routeColumns is the read side of the table, in the order scanRoute expects it. position is not in
// it: the order is the order Routes returns and the order every write rewrites, so a row never has
// to be looked at on its own.
const routeColumns = "id, match, targets, created_at"

// routeQuerier is what reading rules needs; the pool and a transaction both have it.
type routeQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// CreateRoute writes a new rule at the end of the order — in front of the default rule, when there
// is one, because the default rule is always last.
func (db *DB) CreateRoute(ctx context.Context, row RouteRow) error {
	encoded, err := json.Marshal(row.Targets)
	if err != nil {
		return fmt.Errorf("route %s targets: %w", row.ID, err)
	}
	created := nonZero(row.CreatedAt).UTC()
	return db.Write(ctx, func(tx *sql.Tx) error {
		rules, err := readRoutes(ctx, tx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(rules, func(r RouteRow) bool { return r.ID == row.ID }) {
			return fmt.Errorf("route %s: %w", row.ID, ErrRouteExists)
		}
		if row.Match.Default && slices.ContainsFunc(rules, func(r RouteRow) bool { return r.Match.Default }) {
			return fmt.Errorf("route %s: %w", row.ID, ErrDefaultRouteExists)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO routes (id, position, match, targets, created_at) VALUES (?, ?, ?, ?, ?)",
			row.ID, len(rules), row.Match.String(), string(encoded), formatTime(created)); err != nil {
			return err
		}
		return renumberRoutes(ctx, tx, orderWith(rules, row))
	})
}

// Route loads one rule by id.
func (db *DB) Route(ctx context.Context, id string) (RouteRow, error) {
	row, err := scanRoute(db.sql.QueryRowContext(ctx,
		"SELECT "+routeColumns+" FROM routes WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return RouteRow{}, fmt.Errorf("route %s: %w", id, ErrNotFound)
	}
	return row, err
}

// Routes lists every rule in the order they are tried.
func (db *DB) Routes(ctx context.Context) ([]RouteRow, error) {
	return readRoutes(ctx, db.sql)
}

// DeleteRoute removes a rule and closes the gap it leaves in the order.
func (db *DB) DeleteRoute(ctx context.Context, id string) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM routes WHERE id = ?", id)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "route", id); err != nil {
			return err
		}
		rules, err := readRoutes(ctx, tx)
		if err != nil {
			return err
		}
		return renumberRoutes(ctx, tx, ruleIDs(rules))
	})
}

// MoveRoute puts one rule immediately in front of another. The default rule is last by definition,
// so moving it is refused rather than being quietly undone by the next read.
func (db *DB) MoveRoute(ctx context.Context, id, beforeID string) error {
	if id == beforeID {
		return fmt.Errorf("route %s cannot move in front of itself", id)
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		rules, err := readRoutes(ctx, tx)
		if err != nil {
			return err
		}
		from := indexOfRule(rules, id)
		if from < 0 {
			return fmt.Errorf("route %s: %w", id, ErrNotFound)
		}
		if rules[from].Match.Default {
			return fmt.Errorf("route %s: %w", id, ErrDefaultRouteIsLast)
		}
		moved := rules[from]
		rest := slices.Delete(slices.Clone(rules), from, from+1)
		to := indexOfRule(rest, beforeID)
		if to < 0 {
			return fmt.Errorf("route %s: %w", beforeID, ErrNotFound)
		}
		return renumberRoutes(ctx, tx, ruleIDs(slices.Insert(rest, to, moved)))
	})
}

// readRoutes is every rule in the order they are tried, through the pool or inside a transaction.
func readRoutes(ctx context.Context, q routeQuerier) ([]RouteRow, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+routeColumns+" FROM routes ORDER BY position")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RouteRow
	for rows.Next() {
		row, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanRoute(row interface{ Scan(...any) error }) (RouteRow, error) {
	var (
		out            RouteRow
		match, targets string
		created        string
	)
	if err := row.Scan(&out.ID, &match, &targets, &created); err != nil {
		return RouteRow{}, err
	}
	parsed, err := route.ParseMatch(match)
	if err != nil {
		return RouteRow{}, fmt.Errorf("route %s: %w", out.ID, err)
	}
	out.Match = parsed
	if err := json.Unmarshal([]byte(targets), &out.Targets); err != nil {
		return RouteRow{}, fmt.Errorf("route %s targets: %w", out.ID, err)
	}
	if out.CreatedAt, err = parseTime(created); err != nil {
		return RouteRow{}, fmt.Errorf("route %s: %w", out.ID, err)
	}
	return out, nil
}

// renumberRoutes writes position 0..n-1 in the order it is given: every change to the rules rewrites
// the whole order, so positions stay contiguous and the order is what the list says it is.
func renumberRoutes(ctx context.Context, tx *sql.Tx, order []string) error {
	for position, id := range order {
		if _, err := tx.ExecContext(ctx,
			"UPDATE routes SET position = ? WHERE id = ?", position, id); err != nil {
			return err
		}
	}
	return nil
}

// orderWith is the order after adding a rule: at the end, or in front of the default rule.
func orderWith(rules []RouteRow, row RouteRow) []string {
	at := len(rules)
	if !row.Match.Default {
		for i, existing := range rules {
			if existing.Match.Default {
				at = i
				break
			}
		}
	}
	order := make([]string, 0, len(rules)+1)
	for i, existing := range rules {
		if i == at {
			order = append(order, row.ID)
		}
		order = append(order, existing.ID)
	}
	if at == len(rules) {
		order = append(order, row.ID)
	}
	return order
}

func ruleIDs(rules []RouteRow) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.ID)
	}
	return out
}

func indexOfRule(rules []RouteRow, id string) int {
	return slices.IndexFunc(rules, func(r RouteRow) bool { return r.ID == id })
}
