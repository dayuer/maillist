package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// 回写速率：发送速率的上限是账号给的（D17），所以这条把「写者会不会成为瓶颈」钉成一个数字。
//
// The plan for phase 8 wanted a batcher — several messages' write-back in one transaction — on the
// assumption that one commit per message means one fsync per message. It does not: the store runs WAL
// with synchronous(NORMAL), so a commit is a write into the page cache, and the only way to know what
// that buys is to measure it. It buys a lot: 36,000 write-backs a second here (3,000 under -race)
// against the 450 a second the account in the throughput test allows. Batching measured 1.5× under
// -race and 5× without, which is not worth a second writer path and a new failure mode in the place
// at-most-once lives.
//
// Each iteration is the three statements one sent message owes: the message, the campaign counter and
// the contact's own counter.
func TestWriteBackKeepsUpWithSending(t *testing.T) {
	if testing.Short() {
		t.Skip("a throughput test: -short is for the packages that have to be run one at a time")
	}
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "writerate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	for _, statement := range []string{
		`CREATE TABLE probe (id INTEGER PRIMARY KEY, status TEXT, error TEXT)`,
		`INSERT INTO probe (status, error) VALUES ('claimed', '')`,
	} {
		if err := db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, statement)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	writeBack := func(tx *sql.Tx) error {
		for i := 0; i < 3; i++ {
			if _, err := tx.ExecContext(ctx,
				`UPDATE probe SET status = 'sent', error = '' WHERE id = 1`); err != nil {
				return err
			}
		}
		return nil
	}

	const messages = 2000
	start := time.Now()
	for i := 0; i < messages; i++ {
		if err := db.Write(ctx, writeBack); err != nil {
			t.Fatal(err)
		}
	}
	perSecond := float64(messages) / time.Since(start).Seconds()

	// 1,000 a second is the number the plan asked the batcher to demonstrate, and more than twice what
	// an account allowed 450 sends a second needs. The margin is the point: this is not a benchmark.
	if perSecond < 1000 {
		t.Fatalf("write-back ran at %.0f messages a second, want at least 1000: the send path must not "+
			"be the thing that decides how fast a deployment can send", perSecond)
	}
	t.Logf("write-back: %.0f messages a second (%d statements each)", perSecond, 3)
}
