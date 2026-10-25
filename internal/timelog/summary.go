package timelog

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Summary is a window of the log as Settings shows it. Times are ms.
type Summary struct {
	From, To    time.Time
	Turns       int
	Median, P90 int64 // a turn's wall time
	Agent, Wait int64 // the agents' time, waiting on the user left out, and that wait
	Reply       int64 // the median time the user took to send the next prompt
	Replies     int
	Parts       map[string]int64 // Model, Wait and each tool kind
	Days        []Day
	Sinks       []Sink
	Tabs        []Tab
}

// Day is the parts of the turns that ended on Date, local time.
type Day struct {
	Date  time.Time
	Parts map[string]int64
	Total int64
}

// Sink is the kept runs of one command, its numbers, paths and quoted
// text set aside.
type Sink struct {
	Command, Kind string
	Count         int
	Median, Total int64
}

// Tab is one tab's turns in the window, and its milestones from the whole
// log: zero times are ones it has not reached.
type Tab struct {
	ID, Name, Provider       string
	Last                     time.Time // its latest turn's end
	Turns                    int
	Agent, Tools, You        int64 // You is waiting on the user and the user's replies
	Prompt, Opened, CI, Done time.Time
	PR                       int
	CIState                  string
}

// sinkLen caps a sink's command.
const sinkLen = 90

// Placeholders for SinkKey, applied in order: a leading "cd dir &&",
// quoted text, absolute paths, hex ids, then numbers. Relative paths
// stay, as they say which package or script ran.
var (
	cdPrefix = regexp.MustCompile(`^(?:cd\s+\S+\s*(?:&&|;)\s*)+`)
	quoted   = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	absPath  = regexp.MustCompile(`(^|[\s=:])[~/][^\s:;|&)]*`)
	hexID    = regexp.MustCompile(`\b[0-9a-f]{7,}\b`)
	number   = regexp.MustCompile(`(^|[\s=:#])\d+(?:\.\d+)?\b`)
)

// SinkKey is what makes two commands the same time sink: cmd with its
// leading cd dropped and its quoted text, absolute paths, hex ids and
// numbers made placeholders, cut to sinkLen runes.
func SinkKey(cmd string) string {
	k := cdPrefix.ReplaceAllString(strings.TrimSpace(cmd), "")
	k = quoted.ReplaceAllString(k, "<str>")
	k = absPath.ReplaceAllString(k, "$1<path>")
	k = hexID.ReplaceAllStringFunc(k, func(m string) string {
		if strings.ContainsAny(m, "abcdef") && strings.ContainsAny(m, "0123456789") {
			return "<hex>"
		}
		return m
	})
	k = number.ReplaceAllString(k, "${1}N")
	k = strings.Join(strings.Fields(k), " ")
	if r := []rune(k); len(r) > sinkLen {
		k = string(r[:sinkLen-1]) + "…"
	}
	return k
}

// Summarize works out the turns that ended from from to to, oldest first
// in evs, with each tab's milestones read from all of evs. Days are cut at
// midnight in loc.
func Summarize(evs []Event, from, to time.Time, loc *time.Location) Summary {
	s := Summary{From: from, To: to, Parts: map[string]int64{}}
	day0 := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
	for d := day0; !d.After(to); d = d.AddDate(0, 0, 1) {
		s.Days = append(s.Days, Day{Date: d, Parts: map[string]int64{}})
	}
	var walls, replies []int64
	sinks := map[string]*Sink{}
	runs := map[string][]int64{}
	tabs := map[string]*Tab{}
	for _, e := range evs {
		if e.Kind != Turn || e.T.Before(from) || e.T.After(to) {
			continue
		}
		s.Turns++
		walls = append(walls, e.Ms)
		if e.Reply > 0 {
			replies = append(replies, e.Reply)
		}
		s.Agent += e.Agent()
		s.Wait += e.Wait
		var tools int64
		parts := map[string]int64{Model: e.Model, Wait: e.Wait}
		for k, v := range e.Tools {
			parts[k] += v
			tools += v
		}
		di := int(e.T.In(loc).Sub(day0).Hours() / 24)
		for k, v := range parts {
			s.Parts[k] += v
			if di >= 0 && di < len(s.Days) {
				s.Days[di].Parts[k] += v
				s.Days[di].Total += v
			}
		}
		for _, c := range e.Slow {
			k := SinkKey(c.Command)
			if sinks[k] == nil {
				sinks[k] = &Sink{Command: k, Kind: c.Kind}
			}
			sinks[k].Count++
			sinks[k].Total += c.Ms
			runs[k] = append(runs[k], c.Ms)
		}
		if e.Tab == "" {
			continue
		}
		t := tabs[e.Tab]
		if t == nil {
			t = &Tab{ID: e.Tab}
			tabs[e.Tab] = t
		}
		t.Name, t.Provider, t.Last = cmp.Or(e.Branch, e.Name, t.Name), cmp.Or(e.Provider, t.Provider), e.T
		t.Turns++
		t.Agent += e.Agent()
		t.Tools += tools
		t.You += e.Wait + e.Reply
	}
	s.Median, s.P90 = quantile(walls, 0.5), quantile(walls, 0.9)
	s.Reply, s.Replies = quantile(replies, 0.5), len(replies)
	for k, sk := range sinks {
		sk.Median = quantile(runs[k], 0.5)
		s.Sinks = append(s.Sinks, *sk)
	}
	slices.SortFunc(s.Sinks, func(a, b Sink) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), strings.Compare(a.Command, b.Command))
	})
	for _, e := range evs {
		t := tabs[e.Tab]
		if t == nil {
			continue
		}
		first := func(at *time.Time, v time.Time) {
			if at.IsZero() || v.Before(*at) {
				*at = v
			}
		}
		switch e.Kind {
		case Prompt:
			first(&t.Prompt, e.T)
		case Turn:
			first(&t.Prompt, e.Start) // a tab older than the log has no prompt line
		case PR:
			if t.PR == 0 || e.PR == t.PR {
				t.PR = e.PR
				first(&t.Opened, e.T)
			}
		case Checks:
			if t.Done.IsZero() || !e.T.After(t.Done) {
				t.CI, t.CIState = e.T, e.CI // the last run before the merge
			}
		case Merged:
			first(&t.Done, e.T)
		}
	}
	for _, t := range tabs {
		s.Tabs = append(s.Tabs, *t)
	}
	slices.SortFunc(s.Tabs, func(a, b Tab) int { return cmp.Or(b.Last.Compare(a.Last), strings.Compare(a.ID, b.ID)) })
	return s
}

// quantile is the q quantile of xs by nearest rank, 0 for none.
func quantile(xs []int64, q float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	ys := slices.Sorted(slices.Values(xs))
	return ys[min(len(ys)-1, int(q*float64(len(ys))))]
}

// Dur is ms for a person: 840ms, 42s, 3m 20s, 1h 05m.
func Dur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// Hours is ms as hours: 0.4h, 12.3h.
func Hours(ms int64) string { return fmt.Sprintf("%.1fh", float64(ms)/3.6e6) }

// Pct is part of whole as a percentage: 42%, <1%.
func Pct(part, whole int64) string {
	if whole <= 0 {
		return "0%"
	}
	p := float64(part) * 100 / float64(whole)
	if p > 0 && p < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", p)
}

// Between is the time from a to b, "" when either is missing or b comes
// first.
func Between(a, b time.Time) string {
	if a.IsZero() || b.IsZero() || b.Before(a) {
		return ""
	}
	return Dur(b.Sub(a).Milliseconds())
}

// ByTime is the kinds in s.Parts with any time, the most first.
func (s Summary) ByTime() []string {
	var ks []string
	for _, k := range Kinds {
		if s.Parts[k] > 0 {
			ks = append(ks, k)
		}
	}
	slices.SortStableFunc(ks, func(a, b string) int { return cmp.Compare(s.Parts[b], s.Parts[a]) })
	return ks
}

// Report is s as Markdown, to paste into an agent and ask what to speed
// up; at most n sinks and tabs. span names the window, such as "the last
// 7 days".
func Report(s Summary, n int, span string) string {
	var b strings.Builder
	total := s.Agent + s.Wait
	fmt.Fprintf(&b, "# Where my coding agents' time went, %s\n\n", span)
	fmt.Fprintf(&b, "I run several coding agents in parallel, one tab per task. pitwall timed their turns from my prompt to the agent stopping. "+
		"Find what takes the most time and tell me what to change to cut it: my CLAUDE.md or AGENTS.md, tests, builds, CI or how I review.\n\n")
	fmt.Fprintf(&b, "- %d turns, median %s, p90 %s\n", s.Turns, Dur(s.Median), Dur(s.P90))
	fmt.Fprintf(&b, "- Agents worked %s; inside turns they waited %s on me (approvals, questions, plans)\n", Hours(s.Agent), Hours(s.Wait))
	if s.Replies > 0 {
		fmt.Fprintf(&b, "- After a turn ended, I took a median %s to send the next prompt (%d replies)\n", Dur(s.Reply), s.Replies)
	}
	b.WriteString("\n## Where the time goes\n\n| Part | Hours | Share |\n| --- | --- | --- |\n")
	for _, k := range s.ByTime() {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", Label(k), Hours(s.Parts[k]), Pct(s.Parts[k], total))
	}
	if len(s.Sinks) > 0 {
		b.WriteString("\n## Slowest commands, by total time\n\nEach turn keeps its five slowest commands of a second or more.\n\n| Command | Kind | Runs | Median | Total |\n| --- | --- | --- | --- | --- |\n")
		for _, k := range s.Sinks[:min(n, len(s.Sinks))] {
			fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %s |\n", strings.ReplaceAll(k.Command, "|", `\|`), Label(k.Kind), k.Count, Dur(k.Median), Dur(k.Total))
		}
	}
	if len(s.Tabs) > 0 {
		b.WriteString("\n## Tasks, latest first\n\n| Tab | Turns | Agent | Tools | Me | Prompt to PR | PR to CI | CI to merged |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, t := range s.Tabs[:min(n, len(s.Tabs))] {
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s |\n", cmp.Or(t.Name, t.ID), t.Turns, Dur(t.Agent), Dur(t.Tools), Dur(t.You),
				cmp.Or(Between(t.Prompt, t.Opened), "-"), cmp.Or(Between(t.Opened, t.CI), "-"), cmp.Or(Between(t.CI, t.Done), "-"))
		}
	}
	return b.String()
}
