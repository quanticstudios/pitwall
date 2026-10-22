package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// limitLead is what may stand before a limit message on its line.
const limitLead = " \t⎿⏺●■│"

// limitWhen is the reset time in a usage-limit message: Claude Code's
// "3pm", "3:30pm", "Oct 25, 3pm" or "Jan 2, 2027, 3pm", each followed by
// the zone it is in, as "(Europe/Belgrade)"; Codex's "3:04 PM" or "Oct
// 25th, 2026 3:04 PM", in local time; or a 24-hour "15:04".
const limitWhen = `(?:(?P<mon>[a-z]{3}) (?P<day>\d{1,2})(?:st|nd|rd|th)?,? (?:(?P<year>\d{4}),? )?)?` +
	`(?P<h>\d{1,2})(?::(?P<m>\d{2}))?[ \x{202f}]?(?P<ap>[ap]m)?(?: \((?P<tz>[^)\s]+)\))?`

// limitRules are the messages an agent ends a turn with when the plan's
// usage limit stops it, each at the start of a line.
//
// Claude Code 2.1.308 says "You've hit your session limit · resets 3pm
// (Europe/Belgrade)" for its 5-hour limit, and "weekly", "Opus", "Sonnet"
// or "Fable" in place of "session" for the others; versions before 2.0
// said "Claude usage limit reached. Your limit will reset at 3pm
// (Europe/Belgrade).". Codex 0.163.0 says "You’ve hit your usage limit.",
// what to buy, then "or try again at 3:04 PM." or "try again later.".
var limitRules = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^you['’]ve hit your (?:session|weekly|opus|sonnet|fable) limit(?: · resets (?:at )?` + limitWhen + `)?`),
	regexp.MustCompile(`(?i)^claude (?:ai )?usage limit reached\.?(?: your limit will reset at ` + limitWhen + `)?`),
	regexp.MustCompile(`(?i)^you['’]ve hit your usage limit\b(?:.{0,400}?try again at ` + limitWhen + `)?`),
}

// UsageLimit finds the message in text, a hook's last message or the
// bottom of a screen, that says the agent stopped on its plan's usage
// limit. msg is the message, its spaces collapsed; resets is the time it
// names, zero when it names none. A time without a date, or a date
// without a year, is its first occurrence after an hour before now, so a
// message read just after its reset still means that reset.
func UsageLimit(text string, now time.Time) (msg string, resets time.Time, ok bool) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		// why: the message follows only spaces and a glyph such as Claude's
		// ⎿ or ⏺ or Codex's ■; a quote or a list mark means a reply quoting it.
		l = strings.TrimLeft(l, limitLead)
		if !strings.HasPrefix(strings.ToLower(l), "you") && !strings.HasPrefix(strings.ToLower(l), "claude") {
			continue
		}
		// A long message wraps on screen; its next lines carry the time.
		joined := strings.Join(strings.Fields(strings.Join(append([]string{l}, lines[i+1:min(i+5, len(lines))]...), " ")), " ")
		for _, re := range limitRules {
			m := re.FindStringSubmatch(joined)
			if m == nil {
				continue
			}
			return m[0], limitTime(re, m, now), true
		}
	}
	return "", time.Time{}, false
}

// limitTime is the reset that match m of re names, zero for none or one
// that is no time.
func limitTime(re *regexp.Regexp, m []string, now time.Time) time.Time {
	g := func(name string) string { return m[re.SubexpIndex(name)] }
	if g("h") == "" || g("m") == "" && g("ap") == "" {
		return time.Time{} // a bare number is no time
	}
	loc := time.Local
	if z := g("tz"); z != "" {
		if l, err := time.LoadLocation(z); err == nil {
			loc = l
		}
	}
	h, _ := strconv.Atoi(g("h"))
	mins, _ := strconv.Atoi(g("m"))
	switch ap := strings.ToLower(g("ap")); {
	case ap != "" && (h < 1 || h > 12), h > 23, mins > 59:
		return time.Time{}
	case ap == "am":
		h %= 12
	case ap == "pm":
		h = h%12 + 12
	}
	n := now.In(loc)
	if mon := g("mon"); mon != "" {
		month := strings.Index("janfebmaraprmayjunjulaugsepoctnovdec", strings.ToLower(mon))
		day, _ := strconv.Atoi(g("day"))
		if month < 0 || month%3 != 0 || day < 1 || day > 31 {
			return time.Time{}
		}
		if y := g("year"); y != "" {
			year, _ := strconv.Atoi(y)
			return time.Date(year, time.Month(month/3+1), day, h, mins, 0, 0, loc)
		}
		return first(now, func(i int) time.Time { return time.Date(n.Year()+i, time.Month(month/3+1), day, h, mins, 0, 0, loc) })
	}
	return first(now, func(i int) time.Time { return time.Date(n.Year(), n.Month(), n.Day()+i, h, mins, 0, 0, loc) })
}

// first is the earliest of at(-1), at(0) and at(1) that is no more than an
// hour before now.
func first(now time.Time, at func(int) time.Time) time.Time {
	for i := -1; ; i++ {
		if t := at(i); i == 1 || !t.Before(now.Add(-time.Hour)) {
			return t
		}
	}
}
