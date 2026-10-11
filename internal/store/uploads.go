package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Upload is one image on disk under <data-dir>/uploads/. The URL is /uploads/<id>.<ext>, served by
// internal/httpsrv; nothing else about the file is public.
type Upload struct {
	ID        string
	FileName  string
	Slug      string
	MediaType string
	Extension string
	Bytes     int
	CreatedAt time.Time
}

const uploadColumns = `id, file_name, slug, media_type, extension, bytes, created_at`

func (db *DB) CreateUpload(ctx context.Context, u Upload) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO uploads (`+uploadColumns+`) VALUES (?,?,?,?,?,?,?)`,
			u.ID, u.FileName, u.Slug, u.MediaType, u.Extension, u.Bytes,
			formatTime(nonZero(u.CreatedAt)))
		return err
	})
}

// Upload loads one by id.
func (db *DB) Upload(ctx context.Context, id string) (Upload, error) {
	u, err := scanUpload(db.sql.QueryRowContext(ctx,
		"SELECT "+uploadColumns+" FROM uploads WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, fmt.Errorf("upload %s: %w", id, ErrNotFound)
	}
	return u, err
}

// Uploads lists what has been uploaded, newest first.
func (db *DB) Uploads(ctx context.Context, limit int) ([]Upload, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+uploadColumns+" FROM uploads ORDER BY created_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanUpload(row interface{ Scan(...any) error }) (Upload, error) {
	var (
		u         Upload
		createdAt string
	)
	if err := row.Scan(&u.ID, &u.FileName, &u.Slug, &u.MediaType, &u.Extension, &u.Bytes, &createdAt); err != nil {
		return Upload{}, err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return Upload{}, err
	}
	u.CreatedAt = parsed
	return u, nil
}
