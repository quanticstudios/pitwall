package timelog

import (
	"cmp"
	"iter"
	"maps"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Where is the pane a turn runs in, and its tab, for the line.
type Where struct {
	Pane, Tab, Project, Name, Branch, Provider string
}

// Tracker assembles turns from each pane's activity state and its agent's
// tool calls. It is not safe for concurrent use; the daemon calls it under
// its lock.
type Tracker struct {
	turns map[string]*turn
	ended map[string]time.Time // pane: when its last turn ended
	tabs  map[string]bool      // tabs whose first prompt is logged
}

type turn struct {
	w        Where
	start    time.Time
	waitFrom time.Time // zero while not waiting
	waits    []span
	open     []call
	stale    []call // calls with an id whose end never came, set aside
	calls    []call
	lastEnd  time.Time // when the latest call ended
}

type span struct{ from, to time.Time }

type call struct {
	span
	id, tool, cmd, kind string
}

// waiting reports a state in which the agent waits on the user.
func waiting(s model.AgentState) bool {
	return s == model.StatePendingApproval || s == model.StateAwaitingInput || s == model.StatePlanReady
}

// Running reports a state inside a turn: working or waiting on the user.
func Running(s model.AgentState) bool { return s == model.StateWorking || waiting(s) }

// Has reports whether a turn runs in pane.
func (tr *Tracker) Has(pane string) bool { return tr.turns[pane] != nil }

// Panes are the panes with a turn running.
func (tr *Tracker) Panes() iter.Seq[string] { return maps.Keys(tr.turns) }

// State moves w's pane to activity state s at now: working or waiting on
// the user starts a turn, completed, error or no activity ends one. It
// returns the lines to log: the finished turn, and a tab's first prompt.
func (tr *Tracker) State(w Where, s model.AgentState, now time.Time) []Event {
	t := tr.turns[w.Pane]
	switch {
	case t == nil && Running(s):
		if tr.turns == nil {
			tr.turns, tr.ended, tr.tabs = map[string]*turn{}, map[string]time.Time{}, map[string]bool{}
		}
		t = &turn{w: w, start: now}
		tr.turns[w.Pane] = t
		if waiting(s) {
			t.waitFrom = now
		}
		if w.Tab != "" && !tr.tabs[w.Tab] {
			tr.tabs[w.Tab] = true
			return []Event{t.line(Prompt, now)}
		}
	case t != nil && Running(s):
		t.w = w
		switch w := waiting(s); {
		case w && t.waitFrom.IsZero():
			t.waitFrom = now
		case !w && !t.waitFrom.IsZero():
			t.waits = append(t.waits, span{t.waitFrom, now})
			t.waitFrom = time.Time{}
		}
	case t != nil:
		end := Interrupted
		switch s {
		case model.StateCompleted:
			end = Completed
		case model.StateError:
			end = Failed
		}
		return []Event{tr.finish(w.Pane, now, end)}
	}
	return nil
}

// Close ends the turn in pane, which closed.
func (tr *Tracker) Close(pane string, now time.Time) (Event, bool) {
	if tr.turns[pane] == nil {
		return Event{}, false
	}
	e := tr.finish(pane, now, PaneClosed)
	delete(tr.ended, pane)
	return e, true
}

// Forget drops what tr knows of tab, which closed.
func (tr *Tracker) Forget(tab string) { delete(tr.tabs, tab) }

// Tool notes the start or end of a tool call in pane's turn, if one runs.
// An end matches its start by id, or, when the agent sends none, the
// oldest start without one. Such an agent runs one call at a time, so a
// start without an id also ends the calls before it, and a call it never
// reports the end of runs until the next one, or the turn's end. An end
// with no start, as Cursor sends, ran for c.Ms before it.
//
// Claude Code and Codex send no end for a call that another hook blocked,
// that the user denied or that was cancelled, and Codex none for a
// command still running when it handed back its output. A start that
// comes after an end means the model ran in between, so a call with an id
// that started before that end is over: it is set aside and counts for
// nothing, its time going to the model or the calls around it. Its end,
// if it still comes, as for a call running beside a subagent's, counts it
// again.
// ponytail: a lone call with no end, such as a denied one, keeps running
// through the agent's next calls until one of them ends; matching the
// denial on screen would end it sooner.
func (tr *Tracker) Tool(pane string, c agent.Call, now time.Time) {
	t := tr.turns[pane]
	if t == nil {
		return
	}
	if !c.End {
		if c.ID == "" {
			t.closeOpen(now, func(o call) bool { return o.id == "" })
		}
		t.open = slices.DeleteFunc(t.open, func(o call) bool {
			if o.id != "" && o.from.Before(t.lastEnd) {
				t.stale = append(t.stale, o)
				return true
			}
			return false
		})
		t.open = append(t.open, call{span: span{from: now}, id: c.ID, tool: c.Tool, cmd: c.Command})
		return
	}
	t.lastEnd = now
	i := slices.IndexFunc(t.open, func(o call) bool { return o.id == c.ID })
	if i < 0 && c.ID != "" {
		if j := slices.IndexFunc(t.stale, func(o call) bool { return o.id == c.ID }); j >= 0 {
			t.open = append(t.open, t.stale[j])
			t.stale = slices.Delete(t.stale, j, j+1)
			i = len(t.open) - 1
		} else {
			i = slices.IndexFunc(t.open, func(o call) bool { return o.id == "" })
		}
	}
	if i >= 0 {
		o := t.open[i]
		t.open = slices.Delete(t.open, i, i+1)
		o.to, o.cmd, o.tool = now, cmp.Or(o.cmd, c.Command), cmp.Or(o.tool, c.Tool)
		t.calls = append(t.calls, o)
		return
	}
	if c.Ms > 0 {
		t.calls = append(t.calls, call{span: span{now.Add(-time.Duration(c.Ms) * time.Millisecond), now}, tool: c.Tool, cmd: c.Command})
	}
}

// closeOpen ends the open calls that match at now.
func (t *turn) closeOpen(now time.Time, match func(call) bool) {
	t.open = slices.DeleteFunc(t.open, func(o call) bool {
		if match(o) {
			o.to = now
			t.calls = append(t.calls, o)
			return true
		}
		return false
	})
}

func (t *turn) line(kind string, at time.Time) Event {
	return Event{T: at, Kind: kind, Pane: t.w.Pane, Tab: t.w.Tab, Project: t.w.Project, Name: t.w.Name, Branch: t.w.Branch, Provider: t.w.Provider}
}

// finish ends pane's turn at now.
func (tr *Tracker) finish(pane string, now time.Time, end string) Event {
	t := tr.turns[pane]
	delete(tr.turns, pane)
	if !t.waitFrom.IsZero() {
		t.waits = append(t.waits, span{t.waitFrom, now})
	}
	if end == Completed || end == Failed {
		// The agent finished its reply, so its calls are over: one with an
		// id that sent no end is set aside, as in Tool.
		t.open = slices.DeleteFunc(t.open, func(o call) bool { return o.id != "" })
	}
	t.closeOpen(now, func(call) bool { return true })
	e := t.line(Turn, now)
	e.Start, e.End = t.start, end
	if prev, ok := tr.ended[pane]; ok && prev.Before(t.start) {
		e.Reply = ms(t.start.Sub(prev))
	}
	tr.ended[pane] = now
	for i := range t.calls {
		t.calls[i].kind = Classify(t.calls[i].tool, t.calls[i].cmd)
	}
	e.Ms, e.Model, e.Wait, e.Tools = split(t.start, now, t.waits, t.calls)
	var slow []Cmd
	for _, c := range t.calls {
		if c.cmd == "" {
			continue
		}
		if d := ms(c.to.Sub(c.from)) - overlap(c.span, t.waits); d >= slowMin {
			slow = append(slow, Cmd{Command: c.cmd, Kind: c.kind, Ms: d})
		}
	}
	slices.SortStableFunc(slow, func(a, b Cmd) int { return cmp.Compare(b.Ms, a.Ms) })
	e.Slow = slow[:min(maxSlow, len(slow))]
	return e
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// overlap is the ms of s inside any of waits, which do not overlap.
func overlap(s span, waits []span) int64 {
	var n int64
	for _, w := range waits {
		if from, to := later(s.from, w.from), earlier(s.to, w.to); to.After(from) {
			n += ms(to.Sub(from))
		}
	}
	return n
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// split divides the turn from start to end into its parts, in ms: the
// time waiting on the user, each kind's tool time and the model's, which
// is the rest. Parallel calls count once. Waiting wins over a call that
// runs under it, such as one waiting for approval, and time two kinds of
// call share is split evenly between them, so the parts add up to the
// wall time.
func split(start, end time.Time, waits []span, calls []call) (wall, mdl, wait int64, tools map[string]int64) {
	wall = ms(end.Sub(start))
	type edge struct {
		at   int64
		kind string // "" for a wait
		d    int
	}
	var edges []edge
	add := func(s span, kind string) {
		from, to := max(0, ms(s.from.Sub(start))), min(wall, ms(s.to.Sub(start)))
		if to > from {
			edges = append(edges, edge{from, kind, 1}, edge{to, kind, -1})
		}
	}
	for _, w := range waits {
		add(w, "")
	}
	for _, c := range calls {
		add(c.span, c.kind)
	}
	slices.SortFunc(edges, func(a, b edge) int { return cmp.Compare(a.at, b.at) })
	on := map[string]int{}
	share := map[string]float64{}
	var prev int64
	flush := func(to int64) {
		d := to - prev
		if d <= 0 {
			return
		}
		if on[""] > 0 {
			wait += d
			return
		}
		n := 0
		for k, c := range on {
			if k != "" && c > 0 {
				n++
			}
		}
		for k, c := range on {
			if k != "" && c > 0 {
				share[k] += float64(d) / float64(n)
			}
		}
	}
	for _, e := range edges {
		flush(e.at)
		prev = max(prev, e.at)
		on[e.kind] += e.d
	}
	flush(wall)
	mdl = wall - wait
	for k, v := range share {
		if n := int64(v + 0.5); n > 0 {
			if tools == nil {
				tools = map[string]int64{}
			}
			tools[k] = n
			mdl -= n
		}
	}
	return wall, max(0, mdl), wait, tools
}
