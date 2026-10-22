package agent

import (
	"testing"
	"time"
)

// The Claude messages are what Claude Code 2.1.308's rate limit message
// builds; the Codex ones are Codex 0.163.0's UsageLimitReachedError.
func TestUsageLimit(t *testing.T) {
	bel, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	ny, _ := time.LoadLocation("America/New_York")
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = bel
	now := time.Date(2026, 10, 22, 13, 20, 0, 0, bel)
	late := time.Date(2026, 10, 22, 23, 50, 0, 0, bel)
	at := func(loc *time.Location, y int, mo time.Month, d, h, m int) time.Time {
		return time.Date(y, mo, d, h, m, 0, 0, loc)
	}
	for _, tc := range []struct {
		name, text string
		now        time.Time
		ok         bool
		want       time.Time
	}{
		{"claude session", "  ⎿  You've hit your session limit · resets 3pm (Europe/Belgrade)", now, true, at(bel, 2026, 10, 22, 15, 0)},
		{"claude minutes", "⎿ You've hit your session limit · resets 3:30pm (Europe/Belgrade)", now, true, at(bel, 2026, 10, 22, 15, 30)},
		{"claude other zone", "You've hit your session limit · resets 9am (America/New_York)", now, true, at(ny, 2026, 10, 22, 9, 0)},
		{"claude weekly dated", "You've hit your weekly limit · resets Oct 25, 3pm (Europe/Belgrade)", now, true, at(bel, 2026, 10, 25, 15, 0)},
		{"claude next year", "You've hit your Opus limit · resets Jan 2, 2027, 9:15am (Europe/Belgrade)", now, true, at(bel, 2027, 1, 2, 9, 15)},
		{"claude across midnight", "You've hit your session limit · resets 2am (Europe/Belgrade)", late, true, at(bel, 2026, 10, 23, 2, 0)},
		{"claude just reset", "You've hit your session limit · resets 1pm (Europe/Belgrade)", now, true, at(bel, 2026, 10, 22, 13, 0)},
		{"claude no time", "You've hit your weekly limit", now, true, time.Time{}},
		{"claude old", "Claude usage limit reached. Your limit will reset at 5pm (Europe/Belgrade).", now, true, at(bel, 2026, 10, 22, 17, 0)},
		{"claude wrapped", "⎿  You've hit your session limit · resets\n   11pm (Europe/Belgrade)", now, true, at(bel, 2026, 10, 22, 23, 0)},
		{"codex same day", "■ You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at 3:04 PM.", now, true, at(bel, 2026, 10, 22, 15, 4)},
		{"codex dated", "■ You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at Oct 25th, 2026 9:04 AM.", now, true, at(bel, 2026, 10, 25, 9, 4)},
		{"codex wrapped", "■ You’ve hit your usage limit. Upgrade to Plus to continue using Codex\n(https://chatgpt.com/explore/plus), or try again at 12:30\nAM.", late, true, at(bel, 2026, 10, 23, 0, 30)},
		{"codex later", "■ You’ve hit your usage limit. Try again later.", now, true, time.Time{}},
		{"24h", "You’ve hit your usage limit. Try again at 18:45.", now, true, at(bel, 2026, 10, 22, 18, 45)},
		{"just past midnight", "You've hit your session limit · resets 11:50pm (Europe/Belgrade)", time.Date(2026, 10, 23, 0, 20, 0, 0, bel), true, at(bel, 2026, 10, 22, 23, 50)},
		{"date in next year", "You've hit your weekly limit · resets Jan 2, 3pm (Europe/Belgrade)", time.Date(2026, 12, 31, 12, 0, 0, 0, bel), true, at(bel, 2027, 1, 2, 15, 0)},
		{"blockquote", "> You've hit your session limit · resets 3pm (Europe/Belgrade)", now, false, time.Time{}},
		{"list item", "- You've hit your session limit · resets 3pm (Europe/Belgrade)", now, false, time.Time{}},
		{"fast mode is no stop", "You've hit your fast limit · resets in 5m", now, false, time.Time{}},
		{"credits are no plan limit", "You've hit your usage credit limit · resets Nov 1 (Europe/Belgrade)", now, false, time.Time{}},
		{"quoted in a reply", `The test checks "You've hit your session limit · resets 3pm" lines.`, now, false, time.Time{}},
		{"nothing", "> continue\n✻ Worked for 5s", now, false, time.Time{}},
	} {
		msg, got, ok := UsageLimit(tc.text, tc.now)
		if ok != tc.ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v %v (%q), want %v %v", tc.name, got, ok, msg, tc.want, tc.ok)
		}
	}
	if msg, _, _ := UsageLimit("  ⎿  You've hit your session limit · resets\n   3pm (Europe/Belgrade)\n\n> ", now); msg != "You've hit your session limit · resets 3pm (Europe/Belgrade)" {
		t.Errorf("msg = %q", msg)
	}
}
