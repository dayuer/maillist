package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
)

// TestMain lets this test binary act as the child writer of TestConcurrentProcessWrites.
func TestMain(m *testing.M) {
	if path := os.Getenv("MAILCHEAP_TEST_CHILD_DB"); path != "" {
		os.Exit(runChildWriter(path))
	}
	os.Exit(m.Run())
}

// TestStoreChild never runs in the normal suite: TestMain turns the process into the child writer
// when MAILCHEAP_TEST_CHILD_DB is set and exits before m.Run() ever gets here.
func TestStoreChild(t *testing.T) {
	t.Skip("runs as the child writer through TestMain")
}

// runChildWriter opens the same file a parent is already writing and adds 500 rows, as a second
// process (docker compose exec while serve runs) would.
func runChildWriter(path string) int {
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child open:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	for i := 0; i < 500; i++ {
		err := db.Write(ctx, func(tx *sql.Tx) error {
			return insertNewsletter(ctx, tx, "child")
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "child write:", err)
			return 1
		}
	}
	return 0
}

func insertNewsletter(ctx context.Context, tx *sql.Tx, source string) error {
	slug := source + "-" + ids.New()
	now := formatTime(time.Now())
	_, err := tx.ExecContext(ctx, `INSERT INTO newsletters (id, slug, name, from_email, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		ids.New(), slug, source, source+"@example.com", now, now)
	return err
}

// 1500 writes from two processes must all land: the writer retries a busy database instead of
// failing, because the writer lock is held across processes and the in-process writer goroutine
// cannot serialize it (design §5.2).
func TestConcurrentProcessWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	path := filepath.Join(t.TempDir(), "mailcheap.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	child := exec.Command(os.Args[0], "-test.run=TestStoreChild") //nolint:gosec // re-running this test binary as the child writer
	child.Env = append(os.Environ(), "MAILCHEAP_TEST_CHILD_DB="+path)
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for slot := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 250; n++ {
				if err := db.Write(ctx, func(tx *sql.Tx) error {
					return insertNewsletter(ctx, tx, "parent")
				}); err != nil {
					errs[slot] = err
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	for slot, err := range errs {
		if err != nil {
			t.Fatalf("parent writer %d: %v", slot, err)
		}
	}

	var count int
	if err := db.sql.QueryRow("SELECT count(*) FROM newsletters").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1500 {
		t.Fatalf("rows = %d, want 1500", count)
	}
}

// retry must give up with ErrBusy once the budget runs out, not loop forever.
func TestRetryGivesUpWithErrBusy(t *testing.T) {
	attempts := 0
	budget := 50 * time.Millisecond
	err := retry(context.Background(), func() error {
		attempts++
		return errAlwaysBusy
	}, func(error) bool { return true }, budget)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want several", attempts)
	}
}

var errAlwaysBusy = errors.New("database is locked (5) (SQLITE_BUSY)")

// Anything that is not retryable is returned as it is, without retrying.
func TestRetryReturnsOtherErrorsImmediately(t *testing.T) {
	wanted := errors.New("syntax error")
	attempts := 0
	err := retry(context.Background(), func() error {
		attempts++
		return wanted
	}, func(error) bool { return false }, time.Second)
	if !errors.Is(err, wanted) {
		t.Fatalf("err = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// A successful attempt after a busy one is not an error at all.
func TestRetrySucceedsAfterABusyAttempt(t *testing.T) {
	attempts := 0
	err := retry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errAlwaysBusy
		}
		return nil
	}, func(error) bool { return true }, time.Second)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

// isBusy has to recognise the driver's error, spelled the way the driver actually spells it.
func TestIsBusyMatchesTheDriverError(t *testing.T) {
	if isBusy(errors.New("unrelated")) {
		t.Fatal("an unrelated error is not busy")
	}
	if isBusy(nil) {
		t.Fatal("nil is not busy")
	}
}

// A cancelled context stops the retry loop instead of running out the budget.
func TestRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempted := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- retry(ctx, func() error {
			select {
			case attempted <- struct{}{}:
			default:
			}
			return errAlwaysBusy
		}, func(error) bool { return true }, time.Minute)
	}()
	<-attempted // the first attempt happened; cancel so the loop gives up
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
