package decisionlog

import (
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

// CostPerMTok is Jev's list price in dollars per million input tokens;
// output is free. The report's cost is an estimate from it.
const CostPerMTok = 0.04

// MinPerArm is how many answers each side of a comparison needs before
// the report gives a verdict on it.
const MinPerArm = 30

// Features as decide names them.
const (
	approvals = "approvals"
	triage    = "triage"
	turnCheck = "turn_check"
	check     = "check"
	stuck     = "stuck"
)

// Stats is what the log says about the events from From to To: what
// Report prints and the settings page draws.
type Stats struct {
	From, To        time.Time
	Calls, Outcomes int // decisions with a call, with an outcome

	Features          []Feature      // by name
	Failures          map[string]int // failed calls per kind
	Tokens, Estimated int            // input tokens; of those, guessed from request size
	Dollars           float64        // Tokens at CostPerMTok
	P50               float64        // median latency in ms of every answered call
	Days              []Day          // each local day from From to To

	Approvals Approvals
	Turns     Split // turn check: the model's Check and Done turns, corrected
	TurnRule  Split // the same turns by decide.TurnRule's answer
	Checks    Split // checks the model failed and passed, corrected
	Stuck     Split // asks the model said stuck and working, interrupted
	Triage    Triage
}

// Feature is one feature's calls.
type Feature struct {
	Name                    string
	Calls, Failed, Answered int
	P50, P95                float64 // latency in ms of the answered calls
}

// Day is one local day's calls.
type Day struct {
	Date    time.Time // local midnight
	Calls   int
	Dollars float64
}

// Approvals is the holdout: approvals answered with the suggestion shown
// and held out.
type Approvals struct {
	Shown, Held        []float64 // seconds to answer, per arm
	Agree, N           int       // Jev's verdict matched the user's answer
	HeldAgree, HeldN   int       // of those, held out
	ConfAgree, ConfN   int       // of those, at confidence 0.9 or more
	FlaggedAllowed     int       // Jev said ask or deny; the user allowed
	AllowDenied        int       // Jev said allow; the user denied
	Denied, Caught     int       // the user denied; of those, Jev said ask or deny
	Unknown, Unmatched int
	Ruled              int // prompts pitwall's risk flags answered, left out of the rest

	// Once Ready: median(Shown) - median(Held) in seconds and its 95%
	// bootstrap interval.
	Diff, Lo, Hi float64
}

// Ready reports whether both arms have MinPerArm answers, enough for a
// verdict.
func (a Approvals) Ready() bool { return len(a.Shown) >= MinPerArm && len(a.Held) >= MinPerArm }

// Split is how often what the user did next followed a flag: a turn or
// check corrected in the next prompt, a stuck agent interrupted. Check
// counts the flagged (Check, failed, stuck), Done the rest; CheckN and
// DoneN are all of each that have an outcome.
type Split struct{ Check, CheckN, Done, DoneN int }

// Ready reports whether both sides have MinPerArm, enough for a verdict.
func (t Split) Ready() bool { return t.CheckN >= MinPerArm && t.DoneN >= MinPerArm }

// Triage is the time until the user focused a pane, per urgency, with the
// answer used and held out.
type Triage struct {
	Levels    []Level // now, soon, later, fyi; those with a focus
	Unfocused int     // calls with no focus before the pane moved on
}

// Level is one urgency's focus times, used and held out.
type Level struct {
	Name       string
	N          int
	Median     float64 // seconds
	HeldN      int
	HeldMedian float64
}

// Compute works out the stats of evs, the events logged from from to to.
func Compute(evs []Event, from, to time.Time) Stats {
	s := Stats{From: from, To: to, Failures: map[string]int{}}
	calls := map[string]Event{}
	outs := map[string]Event{}
	for _, e := range evs {
		switch e.Kind {
		case Call:
			calls[e.ID] = e
		case Outcome:
			outs[e.ID] = e
		}
	}
	s.Calls, s.Outcomes = len(calls), len(outs)
	s.calls(evs)
	s.approvals(evs, calls)
	s.Turns = split(evs, outs, turnCheck, func(c Event) string { return c.Answer }, "check", Corrected)
	s.TurnRule = split(evs, outs, turnCheck, func(c Event) string { return c.Base }, "check", Corrected)
	s.Checks = split(evs, outs, check, func(c Event) string { return c.Answer }, "failed", Corrected)
	s.Stuck = split(evs, outs, stuck, func(c Event) string { return c.Answer }, "stuck", Interrupted)
	s.triage(evs, calls, outs)
	return s
}

func (s *Stats) calls(evs []Event) {
	ms := map[string][]float64{}
	feats := map[string]*Feature{}
	var all []float64
	day := map[string]int{} // local date to its index in Days
	for d := midnight(s.From); !d.After(s.To); d = d.AddDate(0, 0, 1) {
		day[d.Format(time.DateOnly)] = len(s.Days)
		s.Days = append(s.Days, Day{Date: d})
	}
	for _, e := range evs {
		if e.Kind != Call {
			continue
		}
		f := feats[e.Feature]
		if f == nil {
			f = &Feature{Name: e.Feature}
			feats[e.Feature] = f
		}
		f.Calls++
		s.Tokens += e.Tokens
		if e.Estimated {
			s.Estimated += e.Tokens
		}
		if i, ok := day[e.T.Local().Format(time.DateOnly)]; ok {
			s.Days[i].Calls++
			s.Days[i].Dollars += float64(e.Tokens) * CostPerMTok / 1e6
		}
		if e.Err != "" {
			f.Failed++
			s.Failures[e.Err]++
			continue
		}
		if e.Base == "rule" {
			continue // no model was asked: no latency
		}
		ms[e.Feature] = append(ms[e.Feature], float64(e.Ms))
		all = append(all, float64(e.Ms))
	}
	for _, name := range slices.Sorted(maps.Keys(feats)) {
		f := feats[name]
		f.Answered, f.P50, f.P95 = len(ms[name]), quantile(ms[name], .5), quantile(ms[name], .95)
		s.Features = append(s.Features, *f)
	}
	s.Dollars = float64(s.Tokens) * CostPerMTok / 1e6
	s.P50 = quantile(all, .5)
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func (s *Stats) approvals(evs []Event, calls map[string]Event) {
	a := &s.Approvals
	for _, o := range evs {
		if o.Kind != Outcome || o.Feature != approvals {
			continue
		}
		if c, ok := calls[o.ID]; ok && c.Base == "rule" {
			a.Ruled++ // never held out, so it would skew the shown arm
			continue
		}
		if o.User != Allowed && o.User != Denied {
			a.Unknown++
			continue
		}
		secs := float64(o.WaitMs) / 1000
		if o.Held {
			a.Held = append(a.Held, secs)
		} else {
			a.Shown = append(a.Shown, secs)
		}
		c, ok := calls[o.ID]
		if !ok || c.Err != "" || c.Answer == "" {
			a.Unmatched++
			continue
		}
		jevAllow, userAllow := c.Answer == "allow", o.User == Allowed
		agree := jevAllow == userAllow
		a.N++
		if agree {
			a.Agree++
		}
		if o.Held {
			a.HeldN++
			if agree {
				a.HeldAgree++
			}
		}
		if c.Conf >= 0.9 {
			a.ConfN++
			if agree {
				a.ConfAgree++
			}
		}
		switch {
		case !jevAllow && userAllow:
			a.FlaggedAllowed++
		case jevAllow && !userAllow:
			a.AllowDenied++
		}
		if !userAllow {
			a.Denied++
			if !jevAllow {
				a.Caught++
			}
		}
	}
	if a.Ready() {
		a.Diff = quantile(a.Shown, .5) - quantile(a.Held, .5)
		a.Lo, a.Hi = medianDiffCI(a.Shown, a.Held)
	}
}

// split counts the answered calls of feature that have an outcome, by
// whether answer(c) is flagged, and how many of each got hit.
func split(evs []Event, outs map[string]Event, feature string, answer func(Event) string, flagged, hit string) Split {
	var t Split
	for _, c := range evs {
		if c.Kind != Call || c.Feature != feature || c.Err != "" || answer(c) == "" {
			continue
		}
		o, ok := outs[c.ID]
		if !ok {
			continue
		}
		h := 0
		if o.User == hit {
			h = 1
		}
		if answer(c) == flagged {
			t.CheckN, t.Check = t.CheckN+1, t.Check+h
		} else {
			t.DoneN, t.Done = t.DoneN+1, t.Done+h
		}
	}
	return t
}

func (s *Stats) triage(evs []Event, calls, outs map[string]Event) {
	waits, held := map[string][]float64{}, map[string][]float64{}
	for _, e := range evs {
		if e.Feature != triage {
			continue
		}
		switch {
		case e.Kind == Outcome && e.User == Focused:
			if c, ok := calls[e.ID]; ok && c.Err == "" {
				if e.Held {
					held[c.Answer] = append(held[c.Answer], float64(e.WaitMs)/1000)
				} else {
					waits[c.Answer] = append(waits[c.Answer], float64(e.WaitMs)/1000)
				}
			}
		case e.Kind == Call && e.Err == "":
			if _, ok := outs[e.ID]; !ok {
				s.Triage.Unfocused++
			}
		}
	}
	for _, u := range []string{"now", "soon", "later", "fyi"} {
		if xs, hs := waits[u], held[u]; len(xs)+len(hs) > 0 {
			s.Triage.Levels = append(s.Triage.Levels, Level{Name: u, N: len(xs), Median: quantile(xs, .5), HeldN: len(hs), HeldMedian: quantile(hs, .5)})
		}
	}
}

// Report writes a plain-text summary of evs, the events logged from from
// to to.
func Report(w io.Writer, evs []Event, from, to time.Time) { Compute(evs, from, to).Write(w) }

// Write prints s as plain text.
func (s Stats) Write(w io.Writer) {
	fmt.Fprintf(w, "Decisions %s to %s: %d calls, %d outcomes.\n", s.From.Local().Format(time.DateOnly), s.To.Local().Format(time.DateOnly), s.Calls, s.Outcomes)
	if s.Calls == 0 {
		fmt.Fprintln(w, "Nothing logged in this period. pitwall logs while a decision provider is on.")
		return
	}
	s.writeCalls(w)
	s.writeApprovals(w)
	s.writeTriage(w)
	s.writeSplit(w, "Turn check: your next prompt read as a correction", "marked Check", "marked Done", s.Turns)
	if t := s.TurnRule; t.CheckN+t.DoneN > 0 {
		writeSplitRow(w, "the rule's Check", "the rule's Done", t)
	}
	s.writeSplit(w, "Check runs: your next prompt read as a correction", "after a failed check", "after a passed one", s.Checks)
	s.writeSplit(w, "Stuck: you pressed Esc or Ctrl+C before the next ask or the turn's end", "said stuck", "said working", s.Stuck)
	fmt.Fprintln(w)
	for _, v := range s.Findings() {
		fmt.Fprintln(w, v)
	}
	fmt.Fprintln(w, s.Verdict())
}

func (s Stats) writeCalls(w io.Writer) {
	fmt.Fprintln(w, "\nCalls")
	const row = "  %-12s %6s %7s %8s %8s\n"
	fmt.Fprintf(w, row, "feature", "calls", "failed", "p50", "p95")
	for _, f := range s.Features {
		p50, p95 := "-", "-"
		if f.Answered > 0 {
			p50, p95 = fmt.Sprintf("%.0f ms", f.P50), fmt.Sprintf("%.0f ms", f.P95)
		}
		fmt.Fprintf(w, row, f.Name, fmt.Sprint(f.Calls), Pct(f.Failed, f.Calls), p50, p95)
	}
	if len(s.Failures) > 0 {
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(s.Failures)) {
			parts = append(parts, fmt.Sprintf("%s %d", k, s.Failures[k]))
		}
		fmt.Fprintln(w, "  failures:", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "  cost: about %s for %d input tokens at $%.2f per million (an estimate", Money(s.Dollars), s.Tokens, CostPerMTok)
	if s.Estimated > 0 {
		fmt.Fprintf(w, "; %s of the tokens are guessed from request size, where the reply gave no count", Pct(s.Estimated, s.Tokens))
	}
	fmt.Fprintln(w, ")")
}

func (s Stats) writeApprovals(w io.Writer) {
	a := s.Approvals
	if len(a.Shown)+len(a.Held)+a.Unknown+a.Ruled == 0 {
		return
	}
	fmt.Fprintln(w, "\nApprovals")
	if a.Ruled > 0 {
		fmt.Fprintf(w, "  answered by pitwall's risk flags, without Jev: %d\n", a.Ruled)
	}
	fmt.Fprintf(w, "  time to answer, suggestion shown:  %s\n", spread(a.Shown))
	fmt.Fprintf(w, "  time to answer, held out:          %s\n", spread(a.Held))
	if a.Unknown > 0 {
		fmt.Fprintf(w, "  left out, no event showed the answer: %d\n", a.Unknown)
	}
	if a.N > 0 {
		fmt.Fprintf(w, "  Jev agreed with you on %s (n=%d); held out only %s (n=%d); at confidence 0.9 or more %s (n=%d)\n",
			Pct(a.Agree, a.N), a.N, Pct(a.HeldAgree, a.HeldN), a.HeldN, Pct(a.ConfAgree, a.ConfN), a.ConfN)
		fmt.Fprintf(w, "  Jev said ask or deny and you allowed: %d; Jev said allow and you denied: %d\n", a.FlaggedAllowed, a.AllowDenied)
		fmt.Fprintf(w, "  of the %d you denied, Jev said ask or deny on %d\n", a.Denied, a.Caught)
	}
}

func (s Stats) writeSplit(w io.Writer, title, flagged, rest string, t Split) {
	if t.CheckN+t.DoneN == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", title)
	writeSplitRow(w, flagged, rest, t)
}

func writeSplitRow(w io.Writer, flagged, rest string, t Split) {
	fmt.Fprintf(w, "  %s: %d of %d (%s); %s: %d of %d (%s)", flagged, t.Check, t.CheckN, Pct(t.Check, t.CheckN), rest, t.Done, t.DoneN, Pct(t.Done, t.DoneN))
	if t.CheckN > 0 && t.DoneN > 0 {
		fmt.Fprintf(w, "; lift %+.0f points", t.Lift())
	}
	fmt.Fprintln(w)
}

// Lift is how many points more often a flagged answer than the rest was
// followed by the hit.
func (t Split) Lift() float64 {
	return 100 * (float64(t.Check)/float64(t.CheckN) - float64(t.Done)/float64(t.DoneN))
}

func (s Stats) writeTriage(w io.Writer) {
	t := s.Triage
	if len(t.Levels) == 0 && t.Unfocused == 0 {
		return
	}
	fmt.Fprintln(w, "\nTriage: time until you focused the pane, answer used and held out")
	for _, l := range t.Levels {
		fmt.Fprintf(w, "  %-6s %s; held out %s\n", l.Name, medianN(l.Median, l.N), medianN(l.HeldMedian, l.HeldN))
	}
	if t.Unfocused > 0 {
		fmt.Fprintf(w, "  not focused before the pane moved on: %d\n", t.Unfocused)
	}
}

func medianN(m float64, n int) string {
	if n == 0 {
		return "none"
	}
	return fmt.Sprintf("median %s (n=%d)", Secs(m), n)
}

// Findings is one line per measured feature but approvals: what it shows
// so far, or how much more data it needs.
func (s Stats) Findings() []string {
	var out []string
	var now Level
	for _, l := range s.Triage.Levels {
		if l.Name == "now" {
			now = l
		}
	}
	if now.N+now.HeldN > 0 {
		if now.N >= MinPerArm && now.HeldN >= MinPerArm {
			out = append(out, fmt.Sprintf("Triage: panes it called now got your focus in %s with the answer used and %s held out; it helps if the first is shorter.",
				Secs(now.Median), Secs(now.HeldMedian)))
		} else {
			out = append(out, fmt.Sprintf("Triage: not enough data yet; now panes need %d focused in each arm, there are %d used and %d held out.", MinPerArm, now.N, now.HeldN))
		}
	}
	split := func(name string, t Split, rule *Split) {
		switch {
		case t.CheckN+t.DoneN == 0:
		case !t.Ready():
			out = append(out, fmt.Sprintf("%s: not enough data yet; it needs %d of each answer with an outcome, there are %d and %d.", name, MinPerArm, t.CheckN, t.DoneN))
		case rule != nil && rule.Ready():
			out = append(out, fmt.Sprintf("%s: lift %+.0f points, against %+.0f for the rule; it earns its place only above the rule.", name, t.Lift(), rule.Lift()))
		default:
			out = append(out, fmt.Sprintf("%s: lift %+.0f points; it earns its place only if that is clearly above 0.", name, t.Lift()))
		}
	}
	split("Turn check", s.Turns, &s.TurnRule)
	split("Check runs", s.Checks, nil)
	split("Stuck", s.Stuck, nil)
	return out
}

// Speed is the verdict on answer time without its numbers, "" until the
// approvals are Ready.
func (s Stats) Speed() string {
	a := s.Approvals
	switch {
	case !a.Ready():
		return ""
	case a.Hi < 0:
		return "you answer faster with the suggestion shown"
	case a.Lo > 0:
		return "you answer slower with the suggestion shown"
	}
	return "no clear difference in answer time"
}

// SpanDays is the period in whole days, at least 1.
func (s Stats) SpanDays() int { return max(1, int(math.Round(s.To.Sub(s.From).Hours()/24))) }

// Verdict is one line on whether showing Jev's verdict changes how fast
// approvals get answered, how often Jev agrees and what it cost.
func (s Stats) Verdict() string {
	a := s.Approvals
	if !a.Ready() {
		v := fmt.Sprintf("Verdict: not enough data yet. It needs %d answered approvals in each arm; there are %d shown and %d held out", MinPerArm, len(a.Shown), len(a.Held))
		if len(a.Held) == 0 && len(a.Shown) >= MinPerArm {
			v += " (holdout under [decisions.approvals] is 0?)"
		}
		return v + "."
	}
	return fmt.Sprintf("Verdict: %s (median %s shown vs %s held out; 95%% interval of the difference %+.1fs to %+.1fs). "+
		"Jev agreed with you on %s of approvals and said ask or deny on %d of the %d you denied. About %s over %d days.",
		s.Speed(), Secs(quantile(a.Shown, .5)), Secs(quantile(a.Held, .5)), a.Lo, a.Hi, Pct(a.Agree, a.N), a.Caught, a.Denied, Money(s.Dollars), s.SpanDays())
}

// medianDiffCI is a 95% bootstrap interval of median(a) - median(b), with
// a fixed seed so a report reads the same twice.
func medianDiffCI(a, b []float64) (lo, hi float64) {
	r := rand.New(rand.NewPCG(1, 2))
	const n = 2000
	d := make([]float64, n)
	ra, rb := make([]float64, len(a)), make([]float64, len(b))
	for i := range d {
		for j := range ra {
			ra[j] = a[r.IntN(len(a))]
		}
		for j := range rb {
			rb[j] = b[r.IntN(len(b))]
		}
		d[i] = quantile(ra, .5) - quantile(rb, .5)
	}
	return quantile(d, .025), quantile(d, .975)
}

// Median is the nearest-rank median of xs, 0 for none.
func Median(xs []float64) float64 { return quantile(xs, .5) }

// quantile is the nearest-rank q-quantile of xs, 0 for none.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	return s[max(0, int(math.Ceil(q*float64(len(s))))-1)]
}

func spread(xs []float64) string {
	if len(xs) == 0 {
		return "none yet"
	}
	return fmt.Sprintf("median %s, p75 %s (n=%d)", Secs(quantile(xs, .5)), Secs(quantile(xs, .75)), len(xs))
}

// Money is dollars as the report writes them: four decimals under $1.
func Money(d float64) string {
	if d < 1 {
		return fmt.Sprintf("$%.4f", d)
	}
	return fmt.Sprintf("$%.2f", d)
}

// Secs is seconds to one decimal: "4.2s".
func Secs(s float64) string { return fmt.Sprintf("%.1fs", s) }

// Pct is n of of as a whole percentage, "-" for none.
func Pct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}
