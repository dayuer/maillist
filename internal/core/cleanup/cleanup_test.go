package cleanup

import (
	"slices"
	"testing"
	"time"

	"mailcheap/internal/core/model"
)

// 夹具与上游 tests/cleanup.test.mjs 相同：同一个 NOW、同一批联系人、同一组设置。
var now = time.Date(2027, 6, 1, 12, 0, 0, 0, time.UTC)

var on = Settings{Enabled: true, Issues: 6, Days: 180}

func daysAgo(days float64) time.Time {
	return now.Add(-time.Duration(days * float64(24*time.Hour)))
}

func at(t time.Time) *time.Time { return &t }

func contact(id string, tweak func(*model.Contact)) model.Contact {
	c := model.Contact{ID: id, Email: id + "@example.com", Status: model.ContactActive, SubscribedAt: at(daysAgo(400)), CreatedAt: daysAgo(400)}
	if tweak != nil {
		tweak(&c)
	}
	return c
}

func ids(contacts []model.Contact) []string {
	out := make([]string, 0, len(contacts))
	for _, c := range contacts {
		out = append(out, c.ID)
	}
	return out
}

func sameIDs(got []model.Contact, want ...string) bool {
	return slices.Equal(ids(got), want)
}

// 默认关闭；库里的数字赢过默认值。
func TestCleanupIsOffByDefaultAndSavedNumbersWin(t *testing.T) {
	if got := SettingsOf(model.Newsletter{Slug: "weekly"}); got != (Settings{false, 6, 180}) {
		t.Errorf("got %+v", got)
	}
	if got := SettingsOf(model.Newsletter{CleanupEnabled: true, CleanupIssues: 8, CleanupDays: 60}); got != (Settings{true, 8, 60}) {
		t.Errorf("got %+v", got)
	}
	// 0 与负数不是数字，落回默认。
	if got := SettingsOf(model.Newsletter{CleanupEnabled: true, CleanupIssues: 0, CleanupDays: -1}); got != (Settings{true, 6, 180}) {
		t.Errorf("got %+v", got)
	}
}

// 「冷」是两件事同时成立：够多的忽略次数，和够久没有动静。
func TestColdMeansBothEnoughIssuesAndAnOldSignOfLife(t *testing.T) {
	cases := []struct {
		name    string
		contact model.Contact
		want    Action
	}{
		{"six ignored", contact("a", func(c *model.Contact) { c.IgnoredIssues = 6 }), LastChance},
		{"one short", contact("b", func(c *model.Contact) { c.IgnoredIssues = 5 }), Issue},
		{"issues alone", contact("c", func(c *model.Contact) {
			c.IgnoredIssues = 40
			c.SubscribedAt = at(daysAgo(179))
		}), Issue},
		{"time alone", contact("d", func(c *model.Contact) { c.SubscribedAt = at(daysAgo(3000)) }), Issue},
		{"no issue counted yet", contact("f", nil), Issue},
	}
	for _, tc := range cases {
		if got := ActionOf(tc.contact, on, now); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := ActionOf(contact("e", func(c *model.Contact) { c.IgnoredIssues = 6 }), Settings{Enabled: false}, now); got != Issue {
		t.Errorf("cleanup off: got %q", got)
	}
}

// 任何一次动静都算，而加入日期代替「从没动过的人」。
func TestAnySignOfLifeCounts(t *testing.T) {
	for i, field := range []string{"open", "click", "engaged", "confirmed"} {
		_ = i
		mark := func(c *model.Contact, at time.Time) {
			switch field {
			case "open":
				c.LastOpenAt = &at
			case "click":
				c.LastClickAt = &at
			case "engaged":
				c.LastEngagedAt = &at
			case "confirmed":
				c.ConfirmedAt = &at
			}
		}
		recent := contact(field, func(c *model.Contact) {
			c.IgnoredIssues = 9
			c.SubscribedAt = at(daysAgo(500))
			mark(c, daysAgo(30))
		})
		if got := ActionOf(recent, on, now); got != Issue {
			t.Errorf("%s 30 days ago: got %q, want issue", field, got)
		}
		old := contact(field, func(c *model.Contact) {
			c.IgnoredIssues = 9
			c.SubscribedAt = at(daysAgo(500))
			mark(c, daysAgo(200))
		})
		if got := ActionOf(old, on, now); got != LastChance {
			t.Errorf("%s 200 days ago: got %q, want lastChance", field, got)
		}
	}

	// subscribedAt 缺席时用 createdAt；两者都没有时是零值。
	if got := LastSignOfLife(model.Contact{CreatedAt: daysAgo(10)}); !got.Equal(daysAgo(10)) {
		t.Errorf("got %v", got)
	}
	if got := LastSignOfLife(model.Contact{}); !got.IsZero() {
		t.Errorf("got %v", got)
	}
}

// 最后一次机会之后：等待，以及「14 天或更久之后的又一次发送」变成 dormant。
func TestAfterTheLastChanceEmail(t *testing.T) {
	warned := func(days float64, tweak func(*model.Contact)) model.Contact {
		return contact("w", func(c *model.Contact) {
			c.IgnoredIssues = 6
			c.LastOpenAt = at(daysAgo(300))
			c.LastChanceAt = at(daysAgo(days))
			if tweak != nil {
				tweak(c)
			}
		})
	}
	if got := ActionOf(warned(0, nil), on, now); got != Waiting {
		t.Errorf("today: %q", got)
	}
	if got := ActionOf(warned(LastChanceDays-0.01, nil), on, now); got != Waiting {
		t.Errorf("just under: %q", got)
	}
	if got := ActionOf(warned(LastChanceDays, nil), on, now); got != Dormant {
		t.Errorf("at 14 days: %q", got)
	}
	if got := ActionOf(warned(90, nil), on, now); got != Dormant {
		t.Errorf("90 days: %q", got)
	}

	// 「Yes, keep sending it」、一次点击、一次打开：计数归零，于是又能收信。
	if got := ActionOf(warned(20, func(c *model.Contact) {
		c.IgnoredIssues = 0
		c.LastEngagedAt = at(daysAgo(19))
	}), on, now); got != Issue {
		t.Errorf("kept: %q", got)
	}
	if got := ActionOf(warned(20, func(c *model.Contact) {
		c.IgnoredIssues = 0
		c.LastOpenAt = at(daysAgo(2))
	}), on, now); got != Issue {
		t.Errorf("opened: %q", got)
	}
	// 几个月后又能变冷，并拿到一封新的 last-chance。
	if got := ActionOf(warned(400, func(c *model.Contact) {
		c.IgnoredIssues = 6
		c.LastEngagedAt = at(daysAgo(390))
	}), on, now); got != LastChance {
		t.Errorf("cold again: %q", got)
	}
}

// 发送时的切分：这一期、last-chance、等待中、以及 dormant。最活跃优先，按发送的走法。
func TestTheSendTimeSplit(t *testing.T) {
	audience := []model.Contact{
		contact("engaged1", func(c *model.Contact) { c.LastOpenAt = at(daysAgo(3)) }),
		contact("cold1", func(c *model.Contact) { c.IgnoredIssues = 7 }),
		contact("engaged2", func(c *model.Contact) { c.IgnoredIssues = 1; c.LastClickAt = at(daysAgo(40)) }),
		contact("waiting1", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastChanceAt = at(daysAgo(7)) }),
		contact("new1", func(c *model.Contact) { c.SubscribedAt = at(daysAgo(2)) }),
		contact("expired1", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastChanceAt = at(daysAgo(15)) }),
		contact("cold2", func(c *model.Contact) { c.IgnoredIssues = 12 }),
		contact("engaged3", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastOpenAt = at(daysAgo(100)) }),
	}

	split := SplitAudience(audience, SplitInput{Settings: on, Now: now})
	if !sameIDs(split.Issue, "engaged1", "engaged2", "new1", "engaged3") {
		t.Errorf("issue = %v", ids(split.Issue))
	}
	if !sameIDs(split.LastChance, "cold1", "cold2") {
		t.Errorf("lastChance = %v", ids(split.LastChance))
	}
	if !sameIDs(split.Waiting, "waiting1") {
		t.Errorf("waiting = %v", ids(split.Waiting))
	}
	if !sameIDs(split.Dormant, "expired1") {
		t.Errorf("dormant = %v", ids(split.Dormant))
	}
	if split.Received != 0 {
		t.Errorf("received = %d", split.Received)
	}

	off := SplitAudience(audience, SplitInput{Settings: Settings{Enabled: false}, Now: now})
	if len(off.Issue) != len(audience) {
		t.Errorf("switched off: %v", ids(off.Issue))
	}
}

// limit 只数 issue 收件人，并且在那里停下。
func TestALimitCountsIssueRecipientsOnly(t *testing.T) {
	audience := testAudience()
	split := SplitAudience(audience, SplitInput{Settings: on, Limit: 2, Now: now})
	if !sameIDs(split.Issue, "engaged1", "engaged2") {
		t.Errorf("issue = %v", ids(split.Issue))
	}
	if !sameIDs(split.LastChance, "cold1") {
		t.Errorf("lastChance = %v, want cold1 only: cold2 is past the limit", ids(split.LastChance))
	}
	if len(split.Dormant) != 0 {
		t.Errorf("dormant = %v", ids(split.Dormant))
	}
	if len(SplitAudience(audience, SplitInput{Settings: on, Limit: 0, Now: now}).Issue) == 0 {
		t.Error("limit 0 means everyone, not nobody")
	}
}

// 后一步跳过已经拿到这一期或那一封的人，并把已发的算进 limit。
func TestALaterStepSkipsWhoAlreadyGotIt(t *testing.T) {
	previous := map[string]model.MessageKind{
		"engaged1": model.KindCampaign,
		"engaged2": model.KindCampaign,
		"cold1":    model.KindLastChance,
	}
	split := SplitAudience(testAudience(), SplitInput{Settings: on, Limit: 3, Previous: previous, Now: now})
	if split.Received != 2 {
		t.Errorf("received = %d", split.Received)
	}
	if !sameIDs(split.Issue, "new1") {
		t.Errorf("issue = %v", ids(split.Issue))
	}
	if len(split.LastChance) != 0 {
		t.Errorf("lastChance = %v, want none: cold1 got it in the first step", ids(split.LastChance))
	}
	if !sameIDs(split.Waiting, "waiting1") {
		t.Errorf("waiting = %v", ids(split.Waiting))
	}
}

// 预览的计数与发送做的事一致，在任何 limit 下都一致。
func TestThePreviewMatchesTheSendAtEveryLimit(t *testing.T) {
	previous := map[string]model.MessageKind{"engaged1": model.KindCampaign}
	preview := EmptyPreview()
	preview.Add(testAudience(), SplitInput{Settings: on, Previous: previous, Now: now})

	for _, limit := range []int{0, 1, 2, 3, 4, 5, 50} {
		split := SplitAudience(testAudience(), SplitInput{Settings: on, Limit: limit, Previous: previous, Now: now})
		want := Counts{
			Issue: len(split.Issue) + split.Received, LastChance: len(split.LastChance),
			Waiting: len(split.Waiting), Dormant: len(split.Dormant),
		}
		if got := preview.Summarize(limit); got != want {
			t.Errorf("limit %d: preview %+v, send %+v", limit, got, want)
		}
	}

	// 分块累加与一次走完给出同一个预览。
	chunked := EmptyPreview()
	audience := testAudience()
	for index := 0; index < len(audience); index += 3 {
		end := index + 3
		if end > len(audience) {
			end = len(audience)
		}
		chunked.Add(audience[index:end], SplitInput{Settings: on, Previous: previous, Now: now})
	}
	if chunked.Issue != preview.Issue || len(chunked.LastChance) != len(preview.LastChance) ||
		len(chunked.Waiting) != len(preview.Waiting) || len(chunked.Dormant) != len(preview.Dormant) {
		t.Errorf("chunked %+v, whole %+v", chunked, preview)
	}
}

// testAudience is the fixture the split tests share.
func testAudience() []model.Contact {
	return []model.Contact{
		contact("engaged1", func(c *model.Contact) { c.LastOpenAt = at(daysAgo(3)) }),
		contact("cold1", func(c *model.Contact) { c.IgnoredIssues = 7 }),
		contact("engaged2", func(c *model.Contact) { c.IgnoredIssues = 1; c.LastClickAt = at(daysAgo(40)) }),
		contact("waiting1", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastChanceAt = at(daysAgo(7)) }),
		contact("new1", func(c *model.Contact) { c.SubscribedAt = at(daysAgo(2)) }),
		contact("expired1", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastChanceAt = at(daysAgo(15)) }),
		contact("cold2", func(c *model.Contact) { c.IgnoredIssues = 12 }),
		contact("engaged3", func(c *model.Contact) { c.IgnoredIssues = 6; c.LastOpenAt = at(daysAgo(100)) }),
	}
}
