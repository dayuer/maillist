// Package jobs is Mailcheap's task queue: the jobs table in internal/store plus the lease, retry and
// wake-up policy. It replaces QStash (design D2) — a scheduled task is a row with a run_at.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/store"
)

// State is where a job is in its life.
type State string

const (
	Pending State = "pending"
	Running State = "running"
	Done    State = "done"
	// Dead is a job whose attempts ran out. It stays for the audit trail and is never leased again.
	Dead State = "dead"
)

// LeaseDuration is how long a leased job stays invisible to other workers. A worker that dies
// without calling Complete or Fail leaves the job to be picked up again after this.
const LeaseDuration = 5 * time.Minute

// Job kinds the queue carries today. Later phases add send_welcome and send_alert.
const (
	KindSendCampaign = "send_campaign"
	KindSendBatch    = "send_batch"
	KindImport       = "import_contacts"
)

// backoff is the wait before each retry; running out of entries means the job is dead.
var backoff = []time.Duration{
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute,
}

// Job is one row of the queue.
type Job struct {
	ID        string
	Kind      string
	Payload   json.RawMessage
	RunAt     time.Time
	State     State
	Attempts  int
	LockedAt  *time.Time
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Queue is the jobs table plus the channel an idle worker waits on.
type Queue struct {
	db   *store.DB
	wake chan struct{}
}

// New returns a Queue over db. The wake channel is buffered, so Enqueue never blocks.
func New(db *store.DB) *Queue {
	return &Queue{db: db, wake: make(chan struct{}, 1)}
}

// Wake is signalled whenever a job becomes runnable now. A poller selects on it together with its
// ticker, so an idle worker starts work immediately instead of on the next tick (design §6.1).
func (q *Queue) Wake() <-chan struct{} { return q.wake }

// Enqueue adds a job to run at runAt. payload is marshalled to JSON (nil means `{}`).
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, runAt time.Time) (string, error) {
	encoded := []byte("{}")
	if payload != nil {
		var err error
		if encoded, err = json.Marshal(payload); err != nil {
			return "", fmt.Errorf("encode %s payload: %w", kind, err)
		}
	}
	id := ids.New()
	now := store.FormatTime(time.Now())
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO jobs
            (id, kind, payload, run_at, state, attempts, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
			id, kind, string(encoded), store.FormatTime(runAt), string(Pending), now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	select {
	case q.wake <- struct{}{}:
	default: // a wake-up is already pending; one is enough for any number of new jobs
	}
	return id, nil
}

// Lease takes the next pending job due at now, or one whose lease expired, and marks it running.
// ok is false when there is nothing to do.
func (q *Queue) Lease(ctx context.Context, now time.Time) (Job, bool, error) {
	var (
		job   Job
		found bool
	)
	err := q.db.Write(ctx, func(tx *sql.Tx) error {
		job, found = Job{}, false
		row := tx.QueryRowContext(ctx, `UPDATE jobs SET state = ?, locked_at = ?, updated_at = ?
            WHERE id = (
                SELECT id FROM jobs
                WHERE (state = ? AND run_at <= ?)
                   OR (state = ? AND locked_at <= ?)
                ORDER BY run_at, id
                LIMIT 1
            )
            RETURNING `+columns,
			string(Running), store.FormatTime(now), store.FormatTime(now),
			string(Pending), store.FormatTime(now),
			string(Running), store.FormatTime(now.Add(-LeaseDuration)))
		leased, err := scanJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job, found = leased, true
		return nil
	})
	if err != nil {
		return Job{}, false, err
	}
	return job, found, nil
}

// Complete marks a job done.
func (q *Queue) Complete(ctx context.Context, id string) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE jobs SET state = ?, locked_at = NULL, updated_at = ? WHERE id = ?",
			string(Done), store.FormatTime(time.Now()), id)
		return err
	})
}

// Fail records a failed attempt: the job goes back to pending after the next backoff, or becomes
// dead once the attempts run out.
func (q *Queue) Fail(ctx context.Context, id string, cause error, now time.Time) error {
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		var attempts int
		if err := tx.QueryRowContext(ctx, "SELECT attempts FROM jobs WHERE id = ?", id).Scan(&attempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("job %s: %w", id, store.ErrNotFound)
			}
			return err
		}
		attempts++
		state, runAt := Dead, now
		if attempts <= len(backoff) {
			state, runAt = Pending, now.Add(backoff[attempts-1])
		}
		var message string
		if cause != nil {
			message = truncate(cause.Error(), 500)
		}
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, attempts = ?, run_at = ?,
            locked_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`,
			string(state), attempts, store.FormatTime(runAt), message, store.FormatTime(now), id)
		return err
	})
}

// Get loads one job.
func (q *Queue) Get(ctx context.Context, id string) (Job, error) {
	job, err := scanJob(q.db.Row(ctx, "SELECT "+columns+" FROM jobs WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job %s: %w", id, store.ErrNotFound)
	}
	return job, err
}

// Pending lists the jobs that are still waiting, oldest first.
func (q *Queue) Pending(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.db.Rows(ctx,
		"SELECT "+columns+" FROM jobs WHERE state IN (?, ?) ORDER BY run_at, id LIMIT ?",
		string(Pending), string(Running), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// columns is the SELECT list every jobs query shares, in scanJob order.
const columns = `id, kind, payload, run_at, state, attempts, locked_at, last_error, created_at, updated_at`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var (
		job       Job
		payload   string
		state     string
		runAt     string
		lockedAt  sql.NullString
		createdAt string
		updatedAt string
	)
	err := row.Scan(&job.ID, &job.Kind, &payload, &runAt, &state, &job.Attempts, &lockedAt,
		&job.LastError, &createdAt, &updatedAt)
	if err != nil {
		return Job{}, err
	}
	job.Payload = json.RawMessage(payload)
	job.State = State(state)
	if job.RunAt, err = store.ParseTime(runAt); err != nil {
		return Job{}, err
	}
	if job.LockedAt, err = store.ParseTimePtr(lockedAt); err != nil {
		return Job{}, err
	}
	if job.CreatedAt, err = store.ParseTime(createdAt); err != nil {
		return Job{}, err
	}
	if job.UpdatedAt, err = store.ParseTime(updatedAt); err != nil {
		return Job{}, err
	}
	return job, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
