package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"nibwire/internal/creds"
)

// Credential is one row of the credentials table. The token itself is never stored and never read
// back: only the SHA-256 of it is here, which is what makes a database leak not a key leak.
type Credential struct {
	ID          string
	Kind        string // api | mcp
	Name        string
	Prefix      string // "mc_abcdefg…", so a list can name a key without showing it
	TokenHash   string
	Scopes      []creds.Scope
	DailyBudget int // 0 = the global budget is the only limit
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Revoked reports whether the credential has been taken out of service.
func (c Credential) Revoked() bool { return c.RevokedAt != nil }

//nolint:gosec // G101: a list of column names, not a credential.
const credentialColumns = `id, kind, name, prefix, token_hash, scopes, daily_budget,
    last_used_at, revoked_at, created_at, updated_at`

// CreateCredential stores one credential. scopes must already be encoded JSON.
func (db *DB) CreateCredential(ctx context.Context, c Credential) error {
	encoded, err := creds.Encode(c.Scopes)
	if err != nil {
		return err
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO credentials (`+credentialColumns+`) VALUES (
            ?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.Kind, c.Name, c.Prefix, c.TokenHash, encoded, c.DailyBudget,
			nil, nil, formatTime(nonZero(c.CreatedAt)), formatTime(time.Now()))
		return err
	})
}

// Credential loads one by id.
func (db *DB) Credential(ctx context.Context, id string) (Credential, error) {
	c, err := scanCredential(db.sql.QueryRowContext(ctx,
		"SELECT "+credentialColumns+" FROM credentials WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, fmt.Errorf("credential %s: %w", id, ErrNotFound)
	}
	return c, err
}

// CredentialByHash is the per-request lookup. The hash column is unique, so the index does the work,
// and a revoked row is not found: deleting or revoking is what makes a token stop working (D8 — the
// token carries no claims of its own).
func (db *DB) CredentialByHash(ctx context.Context, hash string) (Credential, error) {
	c, err := scanCredential(db.sql.QueryRowContext(ctx,
		"SELECT "+credentialColumns+" FROM credentials WHERE token_hash = ? AND revoked_at IS NULL", hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, fmt.Errorf("credential for that token: %w", ErrNotFound)
	}
	return c, err
}

// Credentials lists every credential, live ones first and newest first inside each group.
func (db *DB) Credentials(ctx context.Context) ([]Credential, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT "+credentialColumns+` FROM credentials
        ORDER BY (revoked_at IS NOT NULL), created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CredentialByPrefix finds a credential by the prefix shown in `credentials list`, which is how an
// operator names one they can read but cannot copy.
func (db *DB) CredentialByPrefix(ctx context.Context, prefix string) (Credential, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+credentialColumns+" FROM credentials WHERE prefix LIKE ? ORDER BY created_at DESC LIMIT 2",
		strings.TrimSuffix(prefix, "…")+"%")
	if err != nil {
		return Credential{}, err
	}
	defer func() { _ = rows.Close() }()
	var found []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return Credential{}, err
		}
		found = append(found, c)
	}
	if err := rows.Err(); err != nil {
		return Credential{}, err
	}
	switch len(found) {
	case 0:
		return Credential{}, fmt.Errorf("credential %s: %w", prefix, ErrNotFound)
	case 1:
		return found[0], nil
	default:
		return Credential{}, fmt.Errorf("credential %s is ambiguous: use the id", prefix)
	}
}

// CredentialByName finds a credential by the name an operator gave it, which is what they remember.
// Two credentials may share a name, so an ambiguous one is an error naming the id: revoking the
// wrong key because two were called "site" is not a mistake a password store should allow.
func (db *DB) CredentialByName(ctx context.Context, name string) (Credential, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+credentialColumns+` FROM credentials WHERE name = ?
         ORDER BY (revoked_at IS NOT NULL), created_at DESC, id LIMIT 2`, name)
	if err != nil {
		return Credential{}, err
	}
	defer func() { _ = rows.Close() }()
	var found []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return Credential{}, err
		}
		found = append(found, c)
	}
	if err := rows.Err(); err != nil {
		return Credential{}, err
	}
	switch len(found) {
	case 0:
		return Credential{}, fmt.Errorf("credential %q: %w", name, ErrNotFound)
	case 1:
		return found[0], nil
	default:
		return Credential{}, fmt.Errorf("two credentials are called %q: use the id", name)
	}
}

// RevokeCredential takes a credential out of service. ok is false when it was already revoked.
func (db *DB) RevokeCredential(ctx context.Context, id string, at time.Time) (bool, error) {
	revoked := false
	err := db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			"UPDATE credentials SET revoked_at = ?, updated_at = ? WHERE id = ? AND revoked_at IS NULL",
			formatTime(at), formatTime(time.Now()), id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		revoked = affected > 0
		return nil
	})
	return revoked, err
}

// TouchCredential records that a credential was used. The caller decides how often (creds's
// ShouldTouchLastUsed), because a write per request would be one write per request.
func (db *DB) TouchCredential(ctx context.Context, id string, at time.Time) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE credentials SET last_used_at = ? WHERE id = ?",
			formatTime(at), id)
		return err
	})
}

func scanCredential(row interface{ Scan(...any) error }) (Credential, error) {
	var (
		c                     Credential
		scopes                string
		lastUsedAt, revokedAt sql.NullString
		createdAt, updatedAt  string
	)
	err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.Prefix, &c.TokenHash, &scopes, &c.DailyBudget,
		&lastUsedAt, &revokedAt, &createdAt, &updatedAt)
	if err != nil {
		return Credential{}, err
	}
	if c.Scopes, err = creds.Decode(scopes); err != nil {
		return Credential{}, err
	}
	if c.LastUsedAt, err = parseTimePtr(lastUsedAt); err != nil {
		return Credential{}, err
	}
	if c.RevokedAt, err = parseTimePtr(revokedAt); err != nil {
		return Credential{}, err
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return Credential{}, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Credential{}, err
	}
	return c, nil
}
