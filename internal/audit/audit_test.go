package audit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mailcheap/internal/store"
)

func TestParamsDigestIsCanonical(t *testing.T) {
	// The same arguments assembled in a different order are the same action.
	first := map[string]any{"id": "abc", "limit": 10, "dryRun": false}
	second := map[string]any{"dryRun": false, "limit": 10, "id": "abc"}
	if ParamsDigest(first) != ParamsDigest(second) {
		t.Fatal("the digest depends on how the map was built")
	}
	again := map[string]any{"id": "abc", "limit": 10, "dryRun": false}
	if ParamsDigest(first) != ParamsDigest(again) {
		t.Fatal("the digest is not stable")
	}
	// A changed argument changes it.
	if ParamsDigest(first) == ParamsDigest(map[string]any{"id": "abc", "limit": 11, "dryRun": false}) {
		t.Fatal("a changed argument did not change the digest")
	}
	// No arguments is the empty digest, which is distinguishable from "some arguments".
	if ParamsDigest(nil) != "" {
		t.Fatalf("ParamsDigest(nil) = %q", ParamsDigest(nil))
	}
	if ParamsDigest(map[string]any{}) == "" {
		t.Fatal("an empty argument map must still have a digest")
	}
	if len(ParamsDigest(first)) != 64 {
		t.Fatalf("digest = %q, want 64 hex characters", ParamsDigest(first))
	}
	// Something that cannot be marshalled is still recorded as itself rather than as nothing.
	if digest := ParamsDigest(func() {}); digest == "" || digest == ParamsDigest(nil) {
		t.Fatalf("an unencodable parameter produced %q", digest)
	}
}

func TestNoteRecordsAndNeverFails(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "mailcheap.db"))
	if err != nil {
		t.Fatal(err)
	}
	Note(ctx, db, CLI(), "campaign.send", "queued 3 recipients", map[string]any{"id": "abc"}, nil)

	entries, err := List(ctx, db, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	entry := entries[0]
	if entry.ActorKind != "cli" || entry.ActorName != "cli" || entry.Action != "campaign.send" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Result != "queued 3 recipients" || entry.ParamsDigest != ParamsDigest(map[string]any{"id": "abc"}) {
		t.Fatalf("entry = %+v", entry)
	}

	// A closed database: the note is lost, and the caller's action is not turned into a failure.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	Note(ctx, db, CLI(), "campaign.send", "queued 3 recipients", nil, nil)
	if err := Record(ctx, db, CLI(), "campaign.send", "", nil, nil); err == nil {
		t.Fatal("Record did not report the failure it hit")
	}
}

func TestActors(t *testing.T) {
	if API("site").Name != "api:site" || API("site").Kind != "api" {
		t.Fatalf("API actor = %+v", API("site"))
	}
	if MCP("bot").Name != "mcp:bot" || MCP("bot").Kind != "mcp" {
		t.Fatalf("MCP actor = %+v", MCP("bot"))
	}
	if !strings.HasPrefix(CLI().Name, "cli") {
		t.Fatalf("CLI actor = %+v", CLI())
	}
}
