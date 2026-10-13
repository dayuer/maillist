package store

import (
	"context"
	"sort"
	"strings"
	"testing"

	"nibwire/internal/core/model"
)

// claimedEmails is the claimed messages' addresses, sorted, so a test can compare sets.
func claimedEmails(t *testing.T, claimed []string) string {
	t.Helper()
	sort.Strings(claimed)
	return strings.Join(claimed, ",")
}

// 领取查询必须走 messages_claim_channel（迁移 0008），而不是每次把全部待发行排一遍序：20 万待发时，
// 旧计划把领取卡在约 180/s（#76 复审的第二条要求）。
//
// Both claims filter on status and want the oldest waiting rows in created_at, id order: one for a
// channel, one for everything outside a set of them. messages_claimable(status, kind, id) serves
// neither order, so this index is what keeps a claim a walk instead of a sort.
func TestTheClaimQueriesUseTheChannelIndex(t *testing.T) {
	db := openTemp(t)
	for name, claim := range map[string]struct {
		sql  string
		args []any
	}{
		"one channel": {
			sql: "SELECT id FROM messages WHERE status = ? AND " + claimableCampaign +
				" AND channel = ? ORDER BY created_at, id LIMIT ?",
			args: append(append([]any{string(model.MessagePending)}, claimableArgs()...), "aws2", 100),
		},
		"every channel but these": {
			sql: "SELECT id FROM messages WHERE status = ? AND " + claimableCampaign +
				" AND channel NOT IN (?,?) ORDER BY channel, created_at, id LIMIT ?",
			args: append(append([]any{string(model.MessagePending)}, claimableArgs()...), "aws2", "back", 100),
		},
	} {
		plan := explainQuery(t, db, claim.sql, claim.args...)
		if !strings.Contains(plan, "messages_claim_channel") {
			t.Errorf("%s does not use messages_claim_channel:\n%s", name, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s sorts the waiting set before it walks it:\n%s", name, plan)
		}
	}
}

// explainQuery is the plan SQLite would run for a query, one line per step.
func explainQuery(t *testing.T, db *DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.sql.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// Each channel's lane claims only its own messages, so a channel that is slow or throttled never
// holds another channel's mail.
func TestClaimChannelMessagesTakesOnlyThatChannel(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[3].Channel = "aws2"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimChannelMessages(ctx, "aws2", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		if message.Channel != "aws2" {
			t.Fatalf("claimed a %s message", message.Channel)
		}
		got = append(got, message.Email)
	}
	if emails := claimedEmails(t, got); emails != "reader1@example.com,reader3@example.com" {
		t.Fatalf("claimed %s", emails)
	}
	// The default channel's two are still there for the default lane.
	claimed, err = db.ClaimChannelMessages(ctx, "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("default claimed %d, want 2", len(claimed))
	}
}

// Mail recorded on a channel this process has not loaded is claimed by nobody's lane; the engine
// claims it separately, to fail it or to pause its campaign with a reason.
func TestClaimMessagesOutsideTakesTheChannelsNobodyServes(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	messages := campaignMessages(t, db, sendingCampaign(t, db), 4)
	messages[1].Channel = "aws2"
	messages[2].Channel = "gone"
	messages[3].Channel = "off"
	if _, err := db.InsertMessages(ctx, messages); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.ClaimMessagesOutside(ctx, []string{"default", "aws2"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range claimed {
		got = append(got, message.Channel)
	}
	if channels := claimedEmails(t, got); channels != "gone,off" {
		t.Fatalf("claimed messages on %s, want gone and off", channels)
	}
	// With no channels named, every pending message is outside: nothing panics on an empty list.
	rest, err := db.ClaimMessagesOutside(ctx, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 {
		t.Fatalf("claimed %d, want the 2 left", len(rest))
	}
}
