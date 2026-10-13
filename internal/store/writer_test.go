package store

import (
	"bufio"
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

	"nibwire/internal/core/ids"
	"nibwire/internal/core/model"
)

// TestMain lets this test binary act as one of two child processes: the second writer of
// TestConcurrentProcessWrites, or the step writer of
// TestAStepKilledInTheMiddleIsWrittenAndChargedTogetherOrNotAtAll.
func TestMain(m *testing.M) {
	if path := os.Getenv("NIBWIRE_TEST_CHILD_DB"); path != "" {
		if os.Getenv("NIBWIRE_TEST_CHILD_MODE") == "reserve-step" {
			os.Exit(runChildReservingAStep(path))
		}
		os.Exit(runChildWriter(path))
	}
	os.Exit(m.Run())
}

// crashStepMessages is big enough that writing it takes long enough for the parent to kill the child
// in the middle rather than before it starts.
const crashStepMessages = 50_000

// runChildReservingAStep writes one step's messages through ReserveAndInsert. It says so on stdout
// first, so the parent can aim its signal at the writing rather than at process startup.
func runChildReservingAStep(path string) int {
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child open:", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	campaignID := os.Getenv("NIBWIRE_TEST_CHILD_CAMPAIGN")
	now := time.Now().UTC()
	messages := make([]model.Message, 0, crashStepMessages)
	for i := 0; i < crashStepMessages; i++ {
		messages = append(messages, model.Message{
			ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaignID,
			NewsletterID: campaignID, ContactID: ids.New(),
			Email: fmt.Sprintf("reader%d@example.com", i), Status: model.MessagePending,
			CreatedAt: now,
		})
	}
	fmt.Println("writing")
	if _, _, err := db.ReserveAndInsert(ctx, Day(now), "", Limits{Global: 100_000},
		messages); err != nil {
		fmt.Fprintln(os.Stderr, "child reserve:", err)
		return 1
	}
	fmt.Println("written")
	return 0
}

// TestStoreChild never runs in the normal suite: TestMain turns the process into the child writer
// when NIBWIRE_TEST_CHILD_DB is set and exits before m.Run() ever gets here.
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

// 整步的预留与第一块消息同一个事务，其余块各自提交：进程在中途被 SIGKILL 时，当天账本要么整步都占着
// （reserved == N，消息是 1000 的整数倍），要么什么都没有。**绝不会出现"消息在、预算没占"**——预算是
// 对发送的承诺，承诺错了方向就是超发（#38，复审后的口径）。
//
// The child is the real ReserveAndInsert, killed by the parent rather than by a fault injected into
// the store: what is being tested is what a crash leaves behind, and a crash is a process. Both
// outcomes are correct, so the test cannot flake; the one it must never see is messages that nothing
// paid for.
func TestAStepKilledInTheMiddleLeavesTheDayChargedForTheWholeStep(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and kills a subprocess")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nibwire.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	newsletter := seedNewsletter(t, db)
	campaign := model.NewCampaign(newsletter.ID, "Issue", time.Now().UTC())
	campaign.Status = model.CampaignSending
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	child := exec.Command(os.Args[0], "-test.run=TestStoreChild") //nolint:gosec // re-running this test binary as the child writer
	child.Env = append(os.Environ(),
		"NIBWIRE_TEST_CHILD_DB="+path,
		"NIBWIRE_TEST_CHILD_MODE=reserve-step",
		"NIBWIRE_TEST_CHILD_CAMPAIGN="+campaign.ID,
	)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || lines.Text() != "writing" {
		t.Fatalf("the child never said it was writing: %q", lines.Text())
	}
	// Long enough to be inside a fifty-thousand-row insert, short enough that it usually is.
	time.Sleep(100 * time.Millisecond)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	after, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = after.Close() }()
	var written int
	if err := after.Row(ctx, "SELECT count(*) FROM messages WHERE campaign_id = ?",
		campaign.ID).Scan(&written); err != nil {
		t.Fatal(err)
	}
	reserved, err := after.Reserved(ctx, Day(time.Now().UTC()), "")
	if err != nil {
		t.Fatal(err)
	}
	if written > reserved {
		t.Fatalf("%d messages written and only %d charged: the day is charged for less than exists",
			written, reserved)
	}
	switch {
	case written == 0 && reserved == 0:
		// Killed before the first transaction committed. Nothing happened, which is the point.
	case reserved == crashStepMessages && written%InsertChunkSize == 0:
		// The step is reserved and partly written: over-reserved, which is the direction a budget may
		// be wrong in. A chunk boundary, because each chunk is a transaction of its own.
	default:
		t.Fatalf("reserved %d for %d written messages, want the whole step (%d) reserved and a chunk "+
			"boundary written", reserved, written, crashStepMessages)
	}
}

// 1500 writes from two processes must all land: the writer retries a busy database instead of
// failing, because the writer lock is held across processes and the in-process writer goroutine
// cannot serialize it (design §5.2).
func TestConcurrentProcessWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	path := filepath.Join(t.TempDir(), "nibwire.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	child := exec.Command(os.Args[0], "-test.run=TestStoreChild") //nolint:gosec // re-running this test binary as the child writer
	child.Env = append(os.Environ(), "NIBWIRE_TEST_CHILD_DB="+path)
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
