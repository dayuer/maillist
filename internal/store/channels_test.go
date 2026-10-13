package store

import (
	"context"
	"errors"
	"testing"
)

func TestChannelRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	row := ChannelRow{
		Name: "aws2", Type: "ses", SecretEnv: "AWS2",
		Params:       map[string]string{"region": "us-east-1", "configuration_set": "news"},
		MaxPerSecond: 20, MaxPerDay: 1000, Enabled: true,
	}
	if err := db.CreateChannel(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "ses" || got.SecretEnv != "AWS2" || got.MaxPerSecond != 20 || got.MaxPerDay != 1000 ||
		!got.Enabled || got.Params["region"] != "us-east-1" || got.Params["configuration_set"] != "news" {
		t.Fatalf("got %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("times not set: %+v", got)
	}
}

func TestChannelNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	row := ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2", Enabled: true}
	if err := db.CreateChannel(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChannel(ctx, row); !errors.Is(err, ErrChannelExists) {
		t.Fatalf("second create = %v, want ErrChannelExists", err)
	}
}

func TestChannelsAreListedByName(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := db.CreateChannel(ctx, ChannelRow{Name: name, Type: "ses", SecretEnv: "X", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Name != "alpha" || rows[1].Name != "mid" || rows[2].Name != "zeta" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSetChannelEnabled(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateChannel(ctx, ChannelRow{Name: "aws2", Type: "ses", SecretEnv: "AWS2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChannelEnabled(ctx, "aws2", false); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "aws2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("still enabled")
	}
	if err := db.SetChannelEnabled(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel = %v, want ErrNotFound", err)
	}
	if _, err := db.Channel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Channel(nope) = %v, want ErrNotFound", err)
	}
}

// A row with no params reads back as an empty map, never nil, so callers can index it.
func TestChannelWithoutParams(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.CreateChannel(ctx, ChannelRow{Name: "bare", Type: "ses", SecretEnv: "B", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Channel(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	if got.Params == nil || len(got.Params) != 0 {
		t.Fatalf("params = %#v", got.Params)
	}
}
