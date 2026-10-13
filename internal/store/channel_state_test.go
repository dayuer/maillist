package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"nibwire/internal/core/model"
)

func createTestChannel(t *testing.T, db *DB, name string) {
	t.Helper()
	if err := db.CreateChannel(context.Background(),
		ChannelRow{Name: name, Type: "ses", SecretEnv: "X", Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// 标记后不可用；down_until 过后自动算恢复，不需要谁去清除。
func TestAChannelMarkedDownIsDownUntilItsTimeIsUp(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	state, err := db.ChannelDown(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if state.DownAt(time.Now()) || state.Reason != "" || state.Since != nil || state.Until != nil {
		t.Fatalf("a fresh channel is down: %+v", state)
	}

	until := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	if err := db.MarkChannelDown(ctx, "aws2", "daily quota used up", &until); err != nil {
		t.Fatal(err)
	}
	state, err = db.ChannelDown(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if !state.DownAt(time.Now()) || state.Reason != "daily quota used up" || state.Since == nil ||
		state.Until == nil || !state.Until.Equal(until) {
		t.Fatalf("state = %+v", state)
	}
	if state.DownAt(until.Add(time.Second)) {
		t.Fatal("still down after down_until")
	}

	// The row carries it too, so that a listing does not need a second query per channel.
	row, err := db.Channel(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if row.Down.Reason != "daily quota used up" {
		t.Fatalf("row.Down = %+v", row.Down)
	}
}

// 已不可用时再次标记：原因与恢复时间更新，down_since 保留第一次的时间。
func TestMarkingAgainKeepsWhenItWentDown(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	if err := db.MarkChannelDown(ctx, "aws2", "account paused", nil); err != nil {
		t.Fatal(err)
	}
	first, err := db.ChannelDown(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := db.MarkChannelDown(ctx, "aws2", "sender domain not verified", nil); err != nil {
		t.Fatal(err)
	}
	second, err := db.ChannelDown(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if second.Reason != "sender domain not verified" || !second.Since.Equal(*first.Since) {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	// No until: down until somebody puts it back.
	if !second.DownAt(time.Now().Add(365 * 24 * time.Hour)) {
		t.Fatal("a pause without an until recovered on its own")
	}
}

// 不可用状态过期后再次标记：那是新的一次故障，down_since 重新记。配置的渠道与 default 都一样。
func TestMarkingAfterExpiryStartsANewEpisode(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	for _, name := range []string{"aws2", model.DefaultChannel} {
		expired := time.Now().UTC().Add(-time.Second)
		if err := db.MarkChannelDown(ctx, name, "daily quota used up", &expired); err != nil {
			t.Fatal(err)
		}
		first, err := db.ChannelDown(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if first.DownAt(time.Now()) {
			t.Fatalf("%s: an expired state is down: %+v", name, first)
		}
		time.Sleep(5 * time.Millisecond)
		if err := db.MarkChannelDown(ctx, name, "account paused", nil); err != nil {
			t.Fatal(err)
		}
		second, err := db.ChannelDown(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if second.Reason != "account paused" || second.Since == nil || !second.Since.After(*first.Since) {
			t.Fatalf("%s: re-marking after expiry kept the old since: first %+v, second %+v", name, first, second)
		}
	}
}

func TestClearingSaysWhetherTheChannelWasDown(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	if was, err := db.ClearChannelDown(ctx, "aws2"); err != nil || was {
		t.Fatalf("clearing a channel that was up = %v, %v", was, err)
	}
	if err := db.MarkChannelDown(ctx, "aws2", "account paused", nil); err != nil {
		t.Fatal(err)
	}
	if was, err := db.ClearChannelDown(ctx, "aws2"); err != nil || !was {
		t.Fatalf("clearing a channel that was down = %v, %v", was, err)
	}
	state, err := db.ChannelDown(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if state != (ChannelDownState{}) {
		t.Fatalf("state after clearing = %+v", state)
	}
}

// default 不是 channels 的一行，但它走同一套接口。
func TestTheDefaultChannelHasADownStateToo(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	if err := db.MarkChannelDown(ctx, model.DefaultChannel, "account paused", nil); err != nil {
		t.Fatal(err)
	}
	state, err := db.ChannelDown(ctx, model.DefaultChannel)
	if err != nil {
		t.Fatal(err)
	}
	if !state.DownAt(time.Now()) || state.Reason != "account paused" {
		t.Fatalf("state = %+v", state)
	}
	if was, err := db.ClearChannelDown(ctx, model.DefaultChannel); err != nil || !was {
		t.Fatalf("clear = %v, %v", was, err)
	}
	state, err = db.ChannelDown(ctx, model.DefaultChannel)
	if err != nil {
		t.Fatal(err)
	}
	if state.DownAt(time.Now()) {
		t.Fatalf("still down: %+v", state)
	}
	// default is still not a row.
	rows, err := db.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDownStateRefusesWhatItCannotRecord(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	if err := db.MarkChannelDown(ctx, "missing", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking an unknown channel = %v", err)
	}
	if _, err := db.ClearChannelDown(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clearing an unknown channel = %v", err)
	}
	if _, err := db.ChannelDown(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading an unknown channel = %v", err)
	}
	if err := db.MarkChannelDown(ctx, "aws2", "  ", nil); err == nil {
		t.Fatal("an empty reason was accepted")
	}
}

// 设计第 4.4 节：ErrorPause 由运维 `channel enable` 恢复。启用清除不可用状态，停用不清除。
func TestEnablingAChannelClearsItsDownState(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	createTestChannel(t, db, "aws2")

	if err := db.MarkChannelDown(ctx, "aws2", "account paused", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChannelEnabled(ctx, "aws2", false); err != nil {
		t.Fatal(err)
	}
	if state, _ := db.ChannelDown(ctx, "aws2"); state.Reason == "" {
		t.Fatal("disabling cleared the down state")
	}
	if err := db.SetChannelEnabled(ctx, "aws2", true); err != nil {
		t.Fatal(err)
	}
	if state, _ := db.ChannelDown(ctx, "aws2"); state != (ChannelDownState{}) {
		t.Fatalf("enabling left %+v", state)
	}
}

func TestFallbackChainsAreReplacedWhole(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	if chain, err := db.ChannelFallbacks(ctx, "aws2"); err != nil || len(chain) != 0 {
		t.Fatalf("no chain yet = %v, %v", chain, err)
	}
	if err := db.SetChannelFallbacks(ctx, "aws2", []string{"resend", model.DefaultChannel, "back"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChannelFallbacks(ctx, model.DefaultChannel, []string{"aws2"}); err != nil {
		t.Fatal(err)
	}
	chain, err := db.ChannelFallbacks(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chain, []string{"resend", model.DefaultChannel, "back"}) {
		t.Fatalf("chain = %v", chain)
	}

	// A shorter chain replaces the longer one entirely: no stale tail.
	if err := db.SetChannelFallbacks(ctx, "aws2", []string{"back"}); err != nil {
		t.Fatal(err)
	}
	all, err := db.AllChannelFallbacks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || !slices.Equal(all["aws2"], []string{"back"}) ||
		!slices.Equal(all[model.DefaultChannel], []string{"aws2"}) {
		t.Fatalf("all = %v", all)
	}

	// An empty chain clears it.
	if err := db.SetChannelFallbacks(ctx, "aws2", nil); err != nil {
		t.Fatal(err)
	}
	all, err = db.AllChannelFallbacks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["aws2"]; ok || len(all) != 1 {
		t.Fatalf("all after clearing = %v", all)
	}
}
