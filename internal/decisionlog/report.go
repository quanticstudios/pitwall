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

// FollowUp is how soon a prompt after a finished turn counts as the turn
// having needed more work.
const FollowUp = 5 * time.Minute

// MinPerArm is how many answered approvals each holdout arm needs before
// the report gives a verdict.
const MinPerArm = 30

// Features as decide names them.
const (
	approvals = "approvals"
	triage    = "triage"
	turnCheck = "turn_check"
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
	Turns     Turns
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

	// Once Ready: median(Shown) - median(Held) in seconds and its 95%
	// bootstrap interval.
	Diff, Lo, Hi float64
}

// Ready reports whether both arms have MinPerArm answers, enough for a
// verdict.
func (a Approvals) Ready() bool { return len(a.Shown) >= MinPerArm && len(a.Held) >= MinPerArm }

// Turns is the turn check: turns prompted again within FollowUp, by
// answer.
type Turns struct{ Check, CheckN, Done, DoneN int }

// Triage is the time until the user focused a pane, per urgency.
type Triage struct {
	Levels    []Level // now, soon, later, fyi; those with a focus
	Unfocused int     // calls with no focus before the pane moved on
}

// Level is one urgency's focus times.
type Level struct {
	Name   string
	N      int
	Median float64 // seconds
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
	s.turns(evs, outs)
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

func (s *Stats) turns(evs []Event, outs map[string]Event) {
	t := &s.Turns
	for _, c := range evs {
		if c.Kind != Call || c.Feature != turnCheck || c.Err != "" || c.T.After(s.To.Add(-FollowUp)) {
			continue // a turn under FollowUp old may still get its prompt
		}
		o, ok := outs[c.ID]
		followed := ok && o.User == Prompted && o.WaitMs <= FollowUp.Milliseconds()
		switch c.Answer {
		case "check":
			t.CheckN++
			if followed {
				t.Check++
			}
		case "done":
			t.DoneN++
			if followed {
				t.Done++
			}
		}
	}
}

func (s *Stats) triage(evs []Event, calls, outs map[string]Event) {
	waits := map[string][]float64{}
	for _, e := range evs {
		if e.Feature != triage {
			continue
		}
		switch {
		case e.Kind == Outcome && e.User == Focused:
			if c, ok := calls[e.ID]; ok && c.Err == "" {
				waits[c.Answer] = append(waits[c.Answer], float64(e.WaitMs)/1000)
			}
		case e.Kind == Call && e.Err == "":
			if _, ok := outs[e.ID]; !ok {
				s.Triage.Unfocused++
			}
		}
	}
	for _, u := range []string{"now", "soon", "later", "fyi"} {
		if xs := waits[u]; len(xs) > 0 {
			s.Triage.Levels = append(s.Triage.Levels, Level{Name: u, N: len(xs), Median: quantile(xs, .5)})
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
	s.writeTurns(w)
	s.writeTriage(w)
	fmt.Fprintln(w)
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
	if len(a.Shown)+len(a.Held)+a.Unknown == 0 {
		return
	}
	fmt.Fprintln(w, "\nApprovals")
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

func (s Stats) writeTurns(w io.Writer) {
	t := s.Turns
	if t.CheckN+t.DoneN == 0 {
		return
	}
	fmt.Fprintf(w, "\nTurn check: turns you prompted again within %d minutes\n", int(FollowUp.Minutes()))
	fmt.Fprintf(w, "  marked Check: %d of %d (%s); marked Done: %d of %d (%s)", t.Check, t.CheckN, Pct(t.Check, t.CheckN), t.Done, t.DoneN, Pct(t.Done, t.DoneN))
	if t.CheckN > 0 && t.DoneN > 0 {
		fmt.Fprintf(w, "; lift %+.0f points", t.Lift())
	}
	fmt.Fprintln(w)
}

// Lift is how many points more often a Check turn than a Done turn was
// prompted again.
func (t Turns) Lift() float64 {
	return 100 * (float64(t.Check)/float64(t.CheckN) - float64(t.Done)/float64(t.DoneN))
}

func (s Stats) writeTriage(w io.Writer) {
	t := s.Triage
	if len(t.Levels) == 0 && t.Unfocused == 0 {
		return
	}
	fmt.Fprintln(w, "\nTriage: time until you focused the pane")
	for _, l := range t.Levels {
		fmt.Fprintf(w, "  %-6s median %s (n=%d)\n", l.Name, Secs(l.Median), l.N)
	}
	if t.Unfocused > 0 {
		fmt.Fprintf(w, "  not focused before the pane moved on: %d\n", t.Unfocused)
	}
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
