package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// AuditEntry is one row of audit_log (design §11, layer 3): every write and every send, by whoever
// made it.
type AuditEntry struct {
	ID           string
	ActorKind    string // cli | mcp | api | system
	ActorName    string // "cli", "mcp:newsletter-bot", "api:site"
	Action       string // "campaign.send", "newsletter.update", …
	ParamsDigest string // hex SHA-256 of the canonical JSON of the arguments
	Result       string // "queued 1204 recipients", "error: …"
	CreatedAt    time.Time
}

// AuditFilter is what `audit list` narrows on.
type AuditFilter struct {
	Action string // exact match when set
	Actor  string // actor_kind, when set
	Limit  int    // 0 = 100
	Offset int
}

func (db *DB) InsertAudit(ctx context.Context, e AuditEntry) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_log
            (id, actor_kind, actor_name, action, params_digest, result, created_at)
            VALUES (?,?,?,?,?,?,?)`,
			e.ID, e.ActorKind, e.ActorName, e.Action, e.ParamsDigest, e.Result,
			formatTime(nonZero(e.CreatedAt)))
		return err
	})
}

// AuditLog lists entries newest first.
func (db *DB) AuditLog(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	query := `SELECT id, actor_kind, actor_name, action, params_digest, result, created_at
        FROM audit_log`
	var (
		clauses []string
		args    []any
	)
	if filter.Action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, filter.Action)
	}
	if filter.Actor != "" {
		clauses = append(clauses, "actor_kind = ?")
		args = append(args, filter.Actor)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ") //nolint:gosec // G202: fixed clauses, values bound
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit, filter.Offset)
	query += " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		var (
			entry     AuditEntry
			createdAt string
		)
		if err := rows.Scan(&entry.ID, &entry.ActorKind, &entry.ActorName, &entry.Action,
			&entry.ParamsDigest, &entry.Result, &createdAt); err != nil {
			return nil, err
		}
		if entry.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
