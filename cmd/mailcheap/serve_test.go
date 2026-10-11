package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/core/model"
	"mailcheap/internal/store"
)

// freeAddr picks a port for the child to listen on.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

// seedSendingCampaign prepares a campaign that is mid-send: a sending campaign, one active contact
// per message, and the messages already queued.
func seedSendingCampaign(t *testing.T, dbPath string, messages int) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	newsletter := model.Newsletter{
		ID: ids.New(), Slug: "field-notes", Name: "Field notes",
		FromEmail: "jane@example.net", PostalAddress: "1 Main St, Paris", CreatedAt: now,
	}
	if err := db.CreateNewsletter(ctx, newsletter); err != nil {
		t.Fatal(err)
	}
	campaign := model.NewCampaign(newsletter.ID, "Issue 12", now)
	campaign.Body = "# Hello"
	campaign.Status = model.CampaignSending
	campaign.StartedAt = &now
	if err := db.CreateCampaign(ctx, campaign); err != nil {
		t.Fatal(err)
	}

	queued := make([]model.Message, 0, messages)
	for i := 0; i < messages; i++ {
		contact := model.Contact{
			ID: ids.New(), NewsletterID: newsletter.ID,
			Email: fmt.Sprintf("reader%d@example.com", i), Status: model.ContactActive, CreatedAt: now,
		}
		if err := db.CreateContact(ctx, contact); err != nil {
			t.Fatal(err)
		}
		queued = append(queued, model.Message{
			ID: ids.New(), Kind: model.KindCampaign, CampaignID: campaign.ID,
			NewsletterID: newsletter.ID, ContactID: contact.ID, Email: contact.Email,
			Status: model.MessagePending, CreatedAt: now,
		})
	}
	if _, err := db.InsertMessages(ctx, queued); err != nil {
		t.Fatal(err)
	}
}

// messageCounts is the state the shutdown invariant is about.
func messageCounts(t *testing.T, dbPath string) (claimed, unknown, sent, pending int) {
	t.Helper()
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for status, target := range map[string]*int{
		"claimed": &claimed, "unknown": &unknown, "sent": &sent, "pending": &pending,
	} {
		if err := db.Row(context.Background(),
			"SELECT count(*) FROM messages WHERE status = ?", status).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	return claimed, unknown, sent, pending
}

// TestServeShutsDownCleanly is the phase's shutdown requirement: SIGTERM stops the process within
// ten seconds and leaves no claimed messages — they are sent, or back in the queue, and never
// unknown, which would mean a message went missing (D14, design §3.1).
func TestServeShutsDownCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mailcheap.db")
	const messages = 20
	seedSendingCampaign(t, dbPath, messages)

	addr := freeAddr(t)
	child := exec.Command(os.Args[0], "-test.run=TestServeChild") //nolint:gosec // G204: re-running this test binary as the serve child
	child.Env = append(os.Environ(),
		"MAILCHEAP_TEST_SERVE_ADDR="+addr,
		"SESSION_SECRET=test-session-secret-with-at-least-32-characters",
		"APP_URL=https://mail.example.com",
		"SENDING_ENABLED=true",
		"MAILCHEAP_DB="+dbPath,
		"MAILCHEAP_FAKE_SES_LOG="+filepath.Join(dir, "sent.jsonl"),
		// One send per second: the batch is still in flight when the signal arrives.
		"SEND_RATE_PER_SECOND=1",
	)
	child.Stdout = io.Discard
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the server to answer, so the signal lands while it is serving.
	waitForHealth(t, addr)

	// Let a few sends happen, then stop it the way a container runtime does.
	time.Sleep(3500 * time.Millisecond)
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the child exited with %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("the process did not exit within ten seconds of SIGTERM")
	}

	claimed, unknown, sent, pending := messageCounts(t, dbPath)
	if claimed != 0 {
		t.Errorf("claimed = %d after shutdown, want 0", claimed)
	}
	if unknown != 0 {
		t.Errorf("unknown = %d, want 0: a graceful stop must not lose messages", unknown)
	}
	if sent+pending != messages {
		t.Errorf("sent %d + pending %d != %d", sent, pending, messages)
	}
	if sent == 0 {
		t.Error("nothing was sent: the test did not exercise a batch in flight")
	}
	if sent == messages {
		t.Error("everything was sent: the signal did not land mid-batch")
	}
}

// 一个组件起不来，serve 整体退出——不是半死不活地继续跑。
//
// ST-I2 from the phase 1-3 audit: the HTTP listener could fail to bind and the process would keep
// running with a sender and a scheduler and no surface at all, healthz included. A container
// platform restarts what exits; nothing restarts what looks healthy.
func TestServeExitsWhenAComponentFails(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	// Somebody else has the port: the HTTP component cannot start.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestServeChild") //nolint:gosec // G204: re-running this test binary as the serve child
	child.Env = append(os.Environ(),
		"MAILCHEAP_TEST_SERVE_ADDR="+listener.Addr().String(),
		"SESSION_SECRET=test-session-secret-with-at-least-32-characters",
		"APP_URL=https://mail.example.com",
		"SENDING_ENABLED=true",
		"MAILCHEAP_DB="+filepath.Join(dir, "mailcheap.db"),
		// The fake sender: nothing in this repository talks to AWS, tests included.
		"MAILCHEAP_FAKE_SES_LOG="+filepath.Join(dir, "sent.jsonl"),
	)
	var stderr bytes.Buffer
	child.Stdout = io.Discard
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			t.Fatalf("serve exited 0 with no HTTP listener; stderr:\n%s", stderr.String())
		}
	case <-time.After(15 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("serve kept running after a component failed; stderr:\n" + stderr.String())
	}
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + addr + "/healthz") //nolint:gosec,noctx // a test against its own child
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the server never answered /healthz")
}
