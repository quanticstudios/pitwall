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

// Report writes a plain-text summary of evs, the events logged from from
// to to.
func Report(w io.Writer, evs []Event, from, to time.Time) {
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
	fmt.Fprintf(w, "Decisions %s to %s: %d calls, %d outcomes.\n", from.Local().Format(time.DateOnly), to.Local().Format(time.DateOnly), len(calls), len(outs))
	if len(calls) == 0 {
		fmt.Fprintln(w, "Nothing logged in this period. pitwall logs while a decision provider is on.")
		return
	}
	dollars := callsSection(w, evs)
	ap := approvalsSection(w, evs, calls)
	turnSection(w, evs, outs, to)
	triageSection(w, evs, calls, outs)
	fmt.Fprintln(w)
	fmt.Fprintln(w, verdict(ap, dollars, to.Sub(from)))
}

// callsSection prints calls, failures and latency per feature, and the
// cost, which it returns.
func callsSection(w io.Writer, evs []Event) float64 {
	type stat struct {
		n, failed int
		ms        []float64
	}
	stats := map[string]*stat{}
	kinds := map[string]int{}
	var tokens, est int
	for _, e := range evs {
		if e.Kind != Call {
			continue
		}
		s := stats[e.Feature]
		if s == nil {
			s = &stat{}
			stats[e.Feature] = s
		}
		s.n++
		tokens += e.Tokens
		if e.Estimated {
			est += e.Tokens
		}
		if e.Err != "" {
			s.failed++
			kinds[e.Err]++
			continue
		}
		s.ms = append(s.ms, float64(e.Ms))
	}
	fmt.Fprintln(w, "\nCalls")
	const row = "  %-12s %6s %7s %8s %8s\n"
	fmt.Fprintf(w, row, "feature", "calls", "failed", "p50", "p95")
	for _, f := range slices.Sorted(maps.Keys(stats)) {
		s := stats[f]
		p50, p95 := "-", "-"
		if len(s.ms) > 0 {
			p50, p95 = fmt.Sprintf("%.0f ms", quantile(s.ms, .5)), fmt.Sprintf("%.0f ms", quantile(s.ms, .95))
		}
		fmt.Fprintf(w, row, f, fmt.Sprint(s.n), pct(s.failed, s.n), p50, p95)
	}
	if len(kinds) > 0 {
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(kinds)) {
			parts = append(parts, fmt.Sprintf("%s %d", k, kinds[k]))
		}
		fmt.Fprintln(w, "  failures:", strings.Join(parts, ", "))
	}
	dollars := float64(tokens) * CostPerMTok / 1e6
	fmt.Fprintf(w, "  cost: about %s for %d input tokens at $%.2f per million (an estimate", money(dollars), tokens, CostPerMTok)
	if est > 0 {
		fmt.Fprintf(w, "; %s of the tokens are guessed from request size, where the reply gave no count", pct(est, tokens))
	}
	fmt.Fprintln(w, ")")
	return dollars
}

// approvalStats is what the verdict needs from the approvals section.
type approvalStats struct {
	shown, held        []float64 // seconds to answer, per arm
	agree, n           int       // Jev's verdict matched the user's answer
	flaggedAllowed     int       // Jev said ask or deny; the user allowed
	allowDenied        int       // Jev said allow; the user denied
	denied, caught     int       // the user denied; of those, Jev said ask or deny
	heldAgree, heldN   int
	confAgree, confN   int
	unknown, unmatched int
}

func approvalsSection(w io.Writer, evs []Event, calls map[string]Event) approvalStats {
	var s approvalStats
	for _, o := range evs {
		if o.Kind != Outcome || o.Feature != approvals {
			continue
		}
		if o.User != Allowed && o.User != Denied {
			s.unknown++
			continue
		}
		secs := float64(o.WaitMs) / 1000
		if o.Held {
			s.held = append(s.held, secs)
		} else {
			s.shown = append(s.shown, secs)
		}
		c, ok := calls[o.ID]
		if !ok || c.Err != "" || c.Answer == "" {
			s.unmatched++
			continue
		}
		jevAllow, userAllow := c.Answer == "allow", o.User == Allowed
		agree := jevAllow == userAllow
		s.n++
		if agree {
			s.agree++
		}
		if o.Held {
			s.heldN++
			if agree {
				s.heldAgree++
			}
		}
		if c.Conf >= 0.9 {
			s.confN++
			if agree {
				s.confAgree++
			}
		}
		switch {
		case !jevAllow && userAllow:
			s.flaggedAllowed++
		case jevAllow && !userAllow:
			s.allowDenied++
		}
		if !userAllow {
			s.denied++
			if !jevAllow {
				s.caught++
			}
		}
	}
	if len(s.shown)+len(s.held)+s.unknown == 0 {
		return s
	}
	fmt.Fprintln(w, "\nApprovals")
	fmt.Fprintf(w, "  time to answer, suggestion shown:  %s\n", spread(s.shown))
	fmt.Fprintf(w, "  time to answer, held out:          %s\n", spread(s.held))
	if s.unknown > 0 {
		fmt.Fprintf(w, "  left out, no event showed the answer: %d\n", s.unknown)
	}
	if s.n > 0 {
		fmt.Fprintf(w, "  Jev agreed with you on %s (n=%d); held out only %s (n=%d); at confidence 0.9 or more %s (n=%d)\n",
			pct(s.agree, s.n), s.n, pct(s.heldAgree, s.heldN), s.heldN, pct(s.confAgree, s.confN), s.confN)
		fmt.Fprintf(w, "  Jev said ask or deny and you allowed: %d; Jev said allow and you denied: %d\n", s.flaggedAllowed, s.allowDenied)
		fmt.Fprintf(w, "  of the %d you denied, Jev said ask or deny on %d\n", s.denied, s.caught)
	}
	return s
}

func turnSection(w io.Writer, evs []Event, outs map[string]Event, to time.Time) {
	var check, done, checkN, doneN int
	for _, c := range evs {
		if c.Kind != Call || c.Feature != turnCheck || c.Err != "" || c.T.After(to.Add(-FollowUp)) {
			continue // a turn under FollowUp old may still get its prompt
		}
		o, ok := outs[c.ID]
		followed := ok && o.User == Prompted && o.WaitMs <= FollowUp.Milliseconds()
		switch c.Answer {
		case "check":
			checkN++
			if followed {
				check++
			}
		case "done":
			doneN++
			if followed {
				done++
			}
		}
	}
	if checkN+doneN == 0 {
		return
	}
	fmt.Fprintf(w, "\nTurn check: turns you prompted again within %d minutes\n", int(FollowUp.Minutes()))
	fmt.Fprintf(w, "  marked Check: %d of %d (%s); marked Done: %d of %d (%s)", check, checkN, pct(check, checkN), done, doneN, pct(done, doneN))
	if checkN > 0 && doneN > 0 {
		fmt.Fprintf(w, "; lift %+.0f points", 100*(float64(check)/float64(checkN)-float64(done)/float64(doneN)))
	}
	fmt.Fprintln(w)
}

func triageSection(w io.Writer, evs []Event, calls, outs map[string]Event) {
	waits := map[string][]float64{}
	unfocused, seen := 0, false
	for _, e := range evs {
		if e.Feature != triage {
			continue
		}
		switch {
		case e.Kind == Outcome && e.User == Focused:
			if c, ok := calls[e.ID]; ok && c.Err == "" {
				waits[c.Answer] = append(waits[c.Answer], float64(e.WaitMs)/1000)
				seen = true
			}
		case e.Kind == Call && e.Err == "":
			if _, ok := outs[e.ID]; !ok {
				unfocused++
				seen = true
			}
		}
	}
	if !seen {
		return
	}
	fmt.Fprintln(w, "\nTriage: time until you focused the pane")
	for _, u := range []string{"now", "soon", "later", "fyi"} {
		if xs := waits[u]; len(xs) > 0 {
			fmt.Fprintf(w, "  %-6s median %s (n=%d)\n", u, secs(quantile(xs, .5)), len(xs))
		}
	}
	if unfocused > 0 {
		fmt.Fprintf(w, "  not focused before the pane moved on: %d\n", unfocused)
	}
}

// verdict is one line on whether showing Jev's verdict changes how fast
// approvals get answered, how often Jev agrees and what it cost.
func verdict(s approvalStats, dollars float64, span time.Duration) string {
	if len(s.shown) < MinPerArm || len(s.held) < MinPerArm {
		v := fmt.Sprintf("Verdict: not enough data yet. It needs %d answered approvals in each arm; there are %d shown and %d held out", MinPerArm, len(s.shown), len(s.held))
		if len(s.held) == 0 && len(s.shown) >= MinPerArm {
			v += " (holdout under [decisions.approvals] is 0?)"
		}
		return v + "."
	}
	ms, mh := quantile(s.shown, .5), quantile(s.held, .5)
	lo, hi := medianDiffCI(s.shown, s.held)
	var speed string
	switch {
	case hi < 0:
		speed = "you answer faster with the suggestion shown"
	case lo > 0:
		speed = "you answer slower with the suggestion shown"
	default:
		speed = "no clear difference in answer time"
	}
	days := max(1, int(math.Round(span.Hours()/24)))
	return fmt.Sprintf("Verdict: %s (median %s shown vs %s held out; 95%% interval of the difference %+.1fs to %+.1fs). "+
		"Jev agreed with you on %s of approvals and said ask or deny on %d of the %d you denied. About %s over %d days.",
		speed, secs(ms), secs(mh), lo, hi, pct(s.agree, s.n), s.caught, s.denied, money(dollars), days)
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
	return fmt.Sprintf("median %s, p75 %s (n=%d)", secs(quantile(xs, .5)), secs(quantile(xs, .75)), len(xs))
}

func money(d float64) string {
	if d < 1 {
		return fmt.Sprintf("$%.4f", d)
	}
	return fmt.Sprintf("$%.2f", d)
}

func secs(s float64) string { return fmt.Sprintf("%.1fs", s) }

func pct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}
