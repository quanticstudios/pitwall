package panel

import (
	"fmt"
	"hash/fnv"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// Input is everything one frame of the panel shows, for the focused pane.
type Input struct {
	Pane     *model.Pane     // the focused pane, nil when there is none
	Activity *model.Activity // the pane's activity, nil when idle
	Feed     *flow.Feed      // nil until the agent's session file has been read
	Branch   model.BranchStats
	// Base and Files are gitstat.Files for the pane's live directory; Git
	// is false until a call succeeded there.
	Base   string
	Files  []gitstat.FileStat
	Git    bool
	Decide string // model.State.Decide.Provider
	Now    time.Time
}

// View is one of the panel's tabs.
type View string

const (
	ViewFlow      View = "flow"
	ViewSubagents View = "subagents"
	ViewPlan      View = "plan"
	ViewChanges   View = "changes"
	ViewTimeline  View = "timeline"
)

// agent is the pane's coding agent, "" for a shell or no pane.
func (in *Input) agent() model.Provider {
	var p model.Provider
	switch {
	case in.Pane != nil && in.Pane.Provider != "":
		p = in.Pane.Provider
	case in.Activity != nil:
		p = in.Activity.Provider
	case in.Feed != nil:
		p = in.Feed.Provider
	}
	switch p {
	case model.ProviderClaude, model.ProviderCodex, model.ProviderPi:
		return p
	}
	return ""
}

// turn is the feed's current or latest turn, nil without one.
func (in *Input) turn() *flow.Turn {
	if in.Feed == nil || len(in.Feed.Turns) == 0 {
		return nil
	}
	return &in.Feed.Turns[len(in.Feed.Turns)-1]
}

type tab struct {
	view  View
	label string
	count string
	live  bool // a pulsing dot before the count
}

// tabsFor lists the tabs in order. Subagents and Plan show when the feed
// has them, and always for Claude, which can make both; Timeline needs an
// agent. Flow and Changes always show.
func tabsFor(in *Input) []tab {
	ag := in.agent()
	out := []tab{{view: ViewFlow, label: "Flow"}}
	var subs []flow.Subagent
	var plan []flow.Step
	if in.Feed != nil {
		subs, plan = in.Feed.Subagents, in.Feed.Plan
	}
	if ag == model.ProviderClaude || len(subs) > 0 {
		t := tab{view: ViewSubagents, label: "Subagents"}
		if len(subs) > 0 {
			t.count = fmt.Sprint(len(subs))
		}
		t.live = slices.ContainsFunc(subs, flow.Subagent.Running)
		out = append(out, t)
	}
	if ag == model.ProviderClaude || plan != nil {
		t := tab{view: ViewPlan, label: "Plan"}
		if len(plan) > 0 {
			done, n := planCounts(plan)
			t.count = fmt.Sprintf("%d/%d", done, n)
		}
		out = append(out, t)
	}
	ch := tab{view: ViewChanges, label: "Changes"}
	if in.Git {
		ch.count = fmt.Sprint(len(in.Files))
	}
	out = append(out, ch)
	if ag != "" {
		t := tab{view: ViewTimeline, label: "Timeline"}
		if n := len(timeline(in)); n > 0 {
			t.count = fmt.Sprint(n)
		}
		out = append(out, t)
	}
	return out
}

// planCounts is how many steps are done, of how many.
func planCounts(plan []flow.Step) (done, total int) {
	for _, s := range plan {
		if s.State == flow.StepDone {
			done++
		}
	}
	return done, len(plan)
}

// event is one line of the timeline.
type event struct {
	at   time.Time
	text string
	note string // a decision model's suggestion, drawn under text
	bad  bool   // a failed call
}

// maxEvents keeps the timeline of a long session to its newest lines.
const maxEvents = 400

// timeline merges the feed's prompts, calls, subagent starts and ends,
// finished turns, and a pending approval's suggestion, oldest first.
// Calls in a row to the same tool fold into one line.
func timeline(in *Input) []event {
	var out []event
	if f := in.Feed; f != nil {
		for _, t := range f.Turns {
			out = append(out, event{at: t.Start, text: "You: " + t.Prompt})
			out = append(out, foldCalls(t.Calls)...)
			if !t.End.IsZero() {
				out = append(out, event{at: t.End, text: "Finished in " + duration(t.End.Sub(t.Start))})
			}
		}
		for _, s := range f.Subagents {
			who := s.Type
			if who == "" {
				who = "subagent"
			}
			out = append(out, event{at: s.Start, text: "Spawned " + who + ": " + s.Name})
			switch {
			case s.Running():
			case s.Failed:
				out = append(out, event{at: s.End, text: s.Name + " failed after " + duration(s.End.Sub(s.Start)), bad: true})
			default:
				out = append(out, event{at: s.End, text: s.Name + " finished in " + duration(s.End.Sub(s.Start))})
			}
		}
	}
	if a := in.Activity; a != nil && a.State == model.StatePendingApproval {
		e := event{at: a.UpdatedAt, text: "Approval for " + firstLine(a.Detail)}
		if s := sidebar.AdviceText(*a, in.Decide); s != "" {
			e.note = s
			if a.Urgency != "" && a.Urgency != model.UrgencyPending {
				e.note += ", triage " + a.Urgency
			}
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b event) int { return a.at.Compare(b.at) })
	if len(out) > maxEvents {
		out = out[len(out)-maxEvents:]
	}
	return out
}

// foldCalls turns calls into timeline lines: a run of calls to one tool
// that all succeeded is "Read a.go, b.go and 4 more"; a failed or running
// call stays on its own line.
func foldCalls(calls []flow.Call) []event {
	var out []event
	for i := 0; i < len(calls); {
		c := calls[i]
		j := i + 1
		for j < len(calls) && calls[j].Tool == c.Tool && !calls[j].Failed && !c.Failed && !calls[j].Running && !c.Running {
			j++
		}
		e := event{at: c.Time, bad: c.Failed}
		switch n := j - i; {
		case n == 1:
			e.text = strings.TrimSpace(c.Tool + " " + c.Arg)
			if c.Failed {
				e.text += " failed"
			}
		case n == 2:
			e.text = c.Tool + " " + short(c.Arg) + ", " + short(calls[i+1].Arg)
		default:
			e.text = fmt.Sprintf("%s %s, %s and %d more", c.Tool, short(c.Arg), short(calls[i+1].Arg), n-2)
		}
		out = append(out, e)
		i = j
	}
	return out
}

// short is a path argument's base name, any other argument as it is.
func short(arg string) string {
	if strings.Contains(arg, "/") && !strings.Contains(arg, " ") {
		return path.Base(arg)
	}
	return arg
}

// banner is the "What is happening" box at the top of Flow.
type banner struct {
	kind    string // "" working, "wait", "error" or "done"
	eyebrow string
	what    string
	why     string
}

func bannerFor(in *Input) banner {
	name := sidebar.AgentName(in.agent())
	a, t := in.Activity, in.turn()
	if a == nil {
		if t != nil && !t.End.IsZero() {
			return banner{kind: "done", eyebrow: "Finished " + sidebar.RelTime(in.Now, t.End), what: "Latest turn completed", why: firstLine(t.Reply)}
		}
		return banner{kind: "done", eyebrow: "Idle", what: name + " is waiting for a prompt"}
	}
	switch a.State {
	case model.StatePendingApproval:
		why := firstLine(a.Detail)
		if a.Advice != "" {
			why = strings.TrimSuffix(why, ".") + fmt.Sprintf(". %s suggests %s (%.0f%%)", sidebar.DecideName(in.Decide), a.Advice, a.AdviceP*100)
			if a.AdviceRule != "" {
				why += ": " + a.AdviceRule
			}
			why += "."
		} else if a.AdviceRule != "" {
			why = strings.TrimSuffix(why, ".") + ". Risk: " + a.AdviceRule + "."
		}
		return banner{kind: "wait", eyebrow: "Needs you", what: name + " wants approval", why: why}
	case model.StateAwaitingInput:
		return banner{kind: "wait", eyebrow: "Needs you", what: name + " asks you something", why: firstLine(a.Detail)}
	case model.StatePlanReady:
		return banner{kind: "wait", eyebrow: "Needs you", what: "The plan is ready for review", why: firstLine(a.Detail)}
	case model.StateError:
		return banner{kind: "error", eyebrow: "Stopped on an error", what: name + " hit an error", why: firstLine(a.Detail)}
	case model.StateCompleted:
		b := banner{kind: "done", eyebrow: "Finished " + sidebar.RelTime(in.Now, a.UpdatedAt), what: "Latest turn completed"}
		if a.Review {
			b.what = "Turn finished; the turn check says look at it"
		}
		if t != nil {
			b.why = firstLine(t.Reply)
		}
		if b.why == "" {
			b.why = firstLine(a.Detail)
		}
		return b
	case model.StateConnecting:
		return banner{eyebrow: "What is happening", what: name + " is starting"}
	}
	b := banner{eyebrow: "What is happening", what: "Thinking"}
	if t == nil {
		return b
	}
	var why []string
	if c := lastCall(t); c != nil {
		b.what = callTitle(c.Tool)
		s := strings.TrimSpace(c.Tool + " " + c.Arg)
		if c.Running {
			s += " for " + duration(in.Now.Sub(c.Time))
		}
		why = append(why, s+".")
	} else {
		why = append(why, "Turn running for "+duration(in.Now.Sub(t.Start))+".")
	}
	if n := runningSubagents(in.Feed); n > 0 {
		why = append(why, plural(n, "subagent")+" still working.")
	}
	b.why = strings.Join(why, " ")
	return b
}

func lastCall(t *flow.Turn) *flow.Call {
	if len(t.Calls) == 0 {
		return nil
	}
	return &t.Calls[len(t.Calls)-1]
}

// callTitle says what a tool call does, for the banner.
func callTitle(tool string) string {
	switch strings.ToLower(tool) {
	case "bash", "exec", "exec_command", "shell", "local_shell", "bashoutput":
		return "Running a command"
	case "read", "view_image", "notebookread":
		return "Reading files"
	case "edit", "multiedit", "write", "apply_patch", "notebookedit":
		return "Editing files"
	case "grep", "glob", "find", "ls":
		return "Searching the code"
	case "webfetch", "websearch", "web_search":
		return "Reading the web"
	case "task", "agent", "spawn_agent":
		return "Starting a subagent"
	case "todowrite", "update_plan":
		return "Updating the plan"
	}
	return "Using " + tool
}

func runningSubagents(f *flow.Feed) int {
	if f == nil {
		return 0
	}
	n := 0
	for _, s := range f.Subagents {
		if s.Running() {
			n++
		}
	}
	return n
}

// nodeState is how a graph node is drawn.
type nodeState int

const (
	nodeCompleted nodeState = iota
	nodeRunning
	nodeWaiting
	nodePending
)

// node is one box of the Flow graph; edge labels the line into it.
type node struct {
	kind, title, detail string
	state               nodeState
	edge                string
	subagents           bool // a click opens the Subagents tab
}

// graphFor is the current turn as a column of nodes: Prompt, Agent,
// Subagents when the turn spawned any, the decision gate while an
// approval waits, and Outcome.
func graphFor(in *Input) []node {
	name := sidebar.AgentName(in.agent())
	a, t := in.Activity, in.turn()
	state := model.AgentState("")
	if a != nil {
		state = a.State
	}
	ended := t != nil && !t.End.IsZero() || state == model.StateCompleted

	prompt := node{kind: "Prompt", title: "No prompt yet", state: nodeCompleted}
	if t != nil {
		prompt.title, prompt.detail = t.Prompt, "you · "+sidebar.RelTime(in.Now, t.Start)
	} else if in.Pane != nil && in.Pane.Prompt != "" {
		prompt.title, prompt.detail = in.Pane.Prompt, "you"
	}
	out := []node{prompt}

	ag := node{kind: "Agent", title: name + " owns the turn", edge: "prompt", state: nodeCompleted}
	calls := 0
	if t != nil {
		calls = len(t.Calls)
		if c := lastCall(t); c != nil {
			ag.title = strings.TrimSpace(c.Tool + " " + c.Arg)
		}
	}
	ag.detail = strings.ToLower(name) + " · " + plural(calls, "tool call")
	if in.Pane != nil && in.Pane.AgentMode != "" {
		ag.detail += " · " + in.Pane.AgentMode
	}
	switch {
	case ended:
	case state == model.StateWorking || state == model.StateConnecting:
		ag.state = nodeRunning
	case state == model.StateAwaitingInput || state == model.StatePlanReady:
		ag.state = nodeWaiting
	}
	out = append(out, ag)

	if t != nil && len(t.Subagents) > 0 {
		n := node{kind: "Subagents", edge: "spawn", state: nodeCompleted, subagents: true}
		var run []flow.Subagent
		var last *flow.Subagent
		for _, i := range t.Subagents {
			if i < 0 || i >= len(in.Feed.Subagents) {
				continue
			}
			s := in.Feed.Subagents[i]
			if s.Running() {
				run = append(run, s)
			} else if last == nil || s.End.After(last.End) {
				last = &s
			}
		}
		if len(run) > 0 {
			n.state = nodeRunning
			n.title = fmt.Sprintf("%d spawned, %d working", len(t.Subagents), len(run))
			n.detail = run[0].Name + " · " + duration(in.Now.Sub(run[0].Start))
		} else {
			n.title = fmt.Sprintf("%d spawned, all done", len(t.Subagents))
			if last != nil {
				n.detail = last.Name + " finished"
			}
		}
		out = append(out, n)
	}

	gate := state == model.StatePendingApproval
	if gate {
		n := node{kind: sidebar.DecideName(in.Decide) + " gate", title: "Approval: " + firstLine(a.Detail), state: nodeWaiting, edge: "asks", detail: "waiting for you"}
		if in.Decide == "" {
			n.kind = "Approval"
		}
		if out[len(out)-1].subagents {
			n.edge = "return"
		}
		if a.Advice != "" {
			n.detail = fmt.Sprintf("%s %.0f%%", a.Advice, a.AdviceP*100)
			if a.AdviceRule != "" {
				n.detail += " · " + a.AdviceRule
			}
		} else if a.AdviceRule != "" {
			n.detail = "risk: " + a.AdviceRule
		}
		out = append(out, n)
	}

	o := node{kind: "Outcome", title: "Turn result", detail: "not reached yet", state: nodePending, edge: "result"}
	switch {
	case gate:
		o.edge, o.detail = "then", "waiting on the approval"
	case out[len(out)-1].subagents:
		o.edge = "return"
	}
	switch {
	case state == model.StateError:
		o.state, o.title, o.detail = nodeWaiting, "Error", firstLine(a.Detail)
	case ended:
		o.state, o.title = nodeCompleted, "Done"
		if a != nil && a.Review {
			o.title = "Check"
		}
		o.detail = plural(calls, "tool call")
		if t != nil && !t.End.IsZero() {
			o.detail += " · " + duration(t.End.Sub(t.Start))
		}
		if t != nil && t.Reply != "" {
			o.detail = firstLine(t.Reply)
		}
	}
	return append(out, o)
}

// stat is one cell of the Flow stats grid.
type stat struct {
	key, value string
	add, del   int // Changes draws these in green and red before value
	changes    bool
}

func statsFor(in *Input) []stat {
	var calls, errs int
	took := "-"
	if t := in.turn(); t != nil {
		calls, errs = len(t.Calls), t.Errors()
		end := t.End
		if end.IsZero() {
			end = in.Now
		}
		took = duration(end.Sub(t.Start))
	}
	ch := stat{key: "Changes", changes: true, add: in.Branch.Additions, del: in.Branch.Deletions}
	if in.Git {
		ch.value = plural(len(in.Files), "file")
	}
	return []stat{
		{key: "Tool calls", value: fmt.Sprint(calls)},
		{key: "Errors", value: fmt.Sprint(errs)},
		{key: "Turn time", value: took},
		ch,
	}
}

// duration is "12s", "3m 13s" or "1h 04m".
func duration(d time.Duration) string {
	s := max(0, int(d/time.Second))
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s/60%60)
}

// clock is a call's time from its subagent's start, "1:30".
func clock(d time.Duration) string {
	s := max(0, int(d/time.Second))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// hue is a subagent's avatar hue, 0-359, from its id.
func hue(id string) float64 {
	h := fnv.New32a()
	h.Write([]byte(id))
	return float64(h.Sum32() % 360)
}

// splitPath is a file's directory with its slash, and its name.
func splitPath(p string) (dir, name string) {
	i := strings.LastIndexByte(p, '/')
	return p[:i+1], p[i+1:]
}
