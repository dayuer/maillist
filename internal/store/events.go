package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"mailcheap/internal/core/ids"
)

// Event is one row of the events table. Only the problems worth keeping are stored — every send
// produces a delivery, and storing those would fill the database within months (upstream
// RECORDED_EVENT_TYPES).
type Event struct {
	ID           string
	Type         string
	SESMessageID string
	MessageID    string
	CampaignID   string
	Email        string
	OccurredAt   time.Time
	CreatedAt    time.Time
	Data         json.RawMessage
}

// InsertEvent appends one event.
func (db *DB) InsertEvent(ctx context.Context, e Event) error {
	if e.ID == "" {
		e.ID = ids.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO events
            (id, type, ses_message_id, message_id, campaign_id, email, occurred_at, data, created_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Type, e.SESMessageID, e.MessageID, e.CampaignID, e.Email,
			formatTime(e.OccurredAt), string(e.Data), formatTime(e.CreatedAt))
		return err
	})
}
