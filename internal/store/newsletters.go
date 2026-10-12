package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nibwire/internal/core/model"
)

// newsletterColumns is the SELECT list every newsletter query shares, in scanNewsletter order.
const newsletterColumns = `id, slug, name, image_url, from_name, from_email, reply_to, postal_address,
    archive_enabled, description, social_links, author_bio, author_image_url, require_confirmation,
    cleanup_enabled, cleanup_issues, cleanup_days, welcome_enabled, welcome_subject,
    welcome_preview_text, welcome_markdown, confirmation_subject, confirmation_preview_text,
    confirmation_markdown, created_at, updated_at`

// CreateNewsletter inserts one newsletter.
func (db *DB) CreateNewsletter(ctx context.Context, n model.Newsletter) error {
	links, err := json.Marshal(n.SocialLinks)
	if err != nil {
		return fmt.Errorf("social links: %w", err)
	}
	now := formatTime(time.Now())
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO newsletters (
            id, slug, name, image_url, from_name, from_email, reply_to, postal_address,
            archive_enabled, description, social_links, author_bio, author_image_url,
            require_confirmation, cleanup_enabled, cleanup_issues, cleanup_days,
            welcome_enabled, welcome_subject, welcome_preview_text, welcome_markdown,
            confirmation_subject, confirmation_preview_text, confirmation_markdown,
            created_at, updated_at
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.ID, n.Slug, n.Name, n.ImageURL, n.FromName, n.FromEmail, n.ReplyTo, n.PostalAddress,
			boolInt(n.ArchiveEnabled), n.Description, string(links), n.AuthorBio, n.AuthorImageURL,
			boolInt(n.RequireConfirmation), boolInt(n.CleanupEnabled), n.CleanupIssues, n.CleanupDays,
			boolInt(n.WelcomeEnabled), n.Welcome.Subject, n.Welcome.PreviewText, n.Welcome.Markdown,
			n.Confirmation.Subject, n.Confirmation.PreviewText, n.Confirmation.Markdown,
			now, now)
		return err
	})
}

func scanNewsletter(row interface{ Scan(...any) error }) (model.Newsletter, error) {
	var (
		n         model.Newsletter
		links     string
		archive   int
		confirm   int
		cleanup   int
		welcome   int
		createdAt string
		updatedAt string
	)
	err := row.Scan(&n.ID, &n.Slug, &n.Name, &n.ImageURL, &n.FromName, &n.FromEmail, &n.ReplyTo,
		&n.PostalAddress, &archive, &n.Description, &links, &n.AuthorBio, &n.AuthorImageURL,
		&confirm, &cleanup, &n.CleanupIssues, &n.CleanupDays, &welcome, &n.Welcome.Subject,
		&n.Welcome.PreviewText, &n.Welcome.Markdown, &n.Confirmation.Subject,
		&n.Confirmation.PreviewText, &n.Confirmation.Markdown, &createdAt, &updatedAt)
	if err != nil {
		return model.Newsletter{}, err
	}
	n.ArchiveEnabled = archive != 0
	n.RequireConfirmation = confirm != 0
	n.CleanupEnabled = cleanup != 0
	n.WelcomeEnabled = welcome != 0
	if links != "" {
		if err := json.Unmarshal([]byte(links), &n.SocialLinks); err != nil {
			return model.Newsletter{}, fmt.Errorf("social links: %w", err)
		}
	}
	if n.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Newsletter{}, err
	}
	if n.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.Newsletter{}, err
	}
	return n, nil
}

// Newsletter loads one by id.
func (db *DB) Newsletter(ctx context.Context, id string) (model.Newsletter, error) {
	return db.oneNewsletter(ctx, "SELECT "+newsletterColumns+" FROM newsletters WHERE id = ?", id)
}

// NewsletterBySlug loads one by its unique slug.
func (db *DB) NewsletterBySlug(ctx context.Context, slug string) (model.Newsletter, error) {
	return db.oneNewsletter(ctx, "SELECT "+newsletterColumns+" FROM newsletters WHERE slug = ?", slug)
}

func (db *DB) oneNewsletter(ctx context.Context, query string, arg any) (model.Newsletter, error) {
	n, err := scanNewsletter(db.sql.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Newsletter{}, fmt.Errorf("newsletter %v: %w", arg, ErrNotFound)
	}
	return n, err
}

// Newsletters lists every newsletter in creation order.
func (db *DB) Newsletters(ctx context.Context) ([]model.Newsletter, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+newsletterColumns+" FROM newsletters ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Newsletter
	for rows.Next() {
		n, err := scanNewsletter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNewsletter writes every mutable column of n back. created_at is never rewritten.
func (db *DB) UpdateNewsletter(ctx context.Context, n model.Newsletter) error {
	links, err := json.Marshal(n.SocialLinks)
	if err != nil {
		return fmt.Errorf("social links: %w", err)
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE newsletters SET
            slug = ?, name = ?, image_url = ?, from_name = ?, from_email = ?, reply_to = ?,
            postal_address = ?, archive_enabled = ?, description = ?, social_links = ?,
            author_bio = ?, author_image_url = ?, require_confirmation = ?, cleanup_enabled = ?,
            cleanup_issues = ?, cleanup_days = ?, welcome_enabled = ?, welcome_subject = ?,
            welcome_preview_text = ?, welcome_markdown = ?, confirmation_subject = ?,
            confirmation_preview_text = ?, confirmation_markdown = ?, updated_at = ?
            WHERE id = ?`,
			n.Slug, n.Name, n.ImageURL, n.FromName, n.FromEmail, n.ReplyTo, n.PostalAddress,
			boolInt(n.ArchiveEnabled), n.Description, string(links), n.AuthorBio, n.AuthorImageURL,
			boolInt(n.RequireConfirmation), boolInt(n.CleanupEnabled), n.CleanupIssues, n.CleanupDays,
			boolInt(n.WelcomeEnabled), n.Welcome.Subject, n.Welcome.PreviewText, n.Welcome.Markdown,
			n.Confirmation.Subject, n.Confirmation.PreviewText, n.Confirmation.Markdown,
			formatTime(time.Now()), n.ID)
		if err != nil {
			return err
		}
		return requireOneRow(result, "newsletter", n.ID)
	})
}
