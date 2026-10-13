package main

import (
	"context"
	"strings"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"nibwire/internal/store"
)

// channelDownCmd is the script command `channeldown <channel> <until> <reason...>`: it marks a
// channel down the way the engine will when a provider refuses to send (2d part 2). <until> is a Go
// duration from now (negative for one that has already passed), or "-" for a channel that stays down
// until `channel enable`.
func channelDownCmd(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 3 {
		ts.Fatalf("channeldown takes a channel, an until and a reason")
	}
	var until *time.Time
	if args[1] != "-" {
		after, err := time.ParseDuration(args[1])
		if err != nil {
			ts.Fatalf("until: %v", err)
		}
		at := time.Now().UTC().Add(after)
		until = &at
	}
	ctx := context.Background()
	db, err := store.Open(ctx, ts.Getenv("NIBWIRE_DB"))
	if err != nil {
		ts.Fatalf("open the database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.MarkChannelDown(ctx, args[0], strings.Join(args[2:], " "), until); err != nil {
		ts.Fatalf("mark %s down: %v", args[0], err)
	}
}
