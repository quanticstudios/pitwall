// Package board sorts tabs into the board's columns by what each needs from
// the user, from the state a window already has. It draws nothing.
package board

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Column is one of the board's columns, in the order they show.
type Column int

const (
	NeedsYou Column = iota
	Failed
	Working
	PROpen
	Done
	Queued
	Shells
	Columns // how many there are
)

// Names are the columns' headings.
var Names = [Columns]string{"Needs you", "Failed", "Working", "PR open", "Done", "Queued", "Shells"}

// Kind is what a card says its tab is doing.
type Kind int

const (
	KindApproval Kind = iota
	KindInput
	KindPlan
	KindError
	KindLimit // stopped by a plan's usage limit
	KindWorking
	KindWatching // an orchestrator whose turn ended while its workers work
	KindRunning  // a command reporting its status (OSC 7501)
	KindPR
	KindDone
	KindIdle    // an agent at its prompt with no turn yet
	KindStopped // a worker tab with no agent running
	KindShell
	KindTask
)

// Words are each kind in a few words, for a held card's "Now ..." line.
var Words = map[Kind]string{
	KindApproval: "waiting for approval", KindInput: "waiting for an answer", KindPlan: "waiting on its plan",
	KindError: "failed", KindLimit: "stopped by a usage limit", KindWorking: "working", KindWatching: "watching its workers",
	KindRunning: "running", KindPR: "PR open", KindDone: "done", KindIdle: "idle", KindStopped: "stopped", KindShell: "a shell",
	KindTask: "queued",
}

// ColumnOf is the column a kind's card goes in.
func ColumnOf(k Kind) Column {
	switch k {
	case KindApproval, KindInput, KindPlan:
		return NeedsYou
	case KindError, KindLimit:
		return Failed
	case KindWorking, KindWatching, KindRunning:
		return Working
	case KindPR:
		return PROpen
	case KindShell:
		return Shells
	case KindTask:
		return Queued
	}
	return Done
}

// Card is one tab, or one queued task, on the board.
type Card struct {
	ID   string // the tab's id, or "task:" and the task's id
	Kind Kind
	Tab  model.Workspace
	// Act is the activity the card shows; its PaneID is "" when the tab
	// has none.
	Act  model.Activity
	Host string // the machine the tab runs on, "" for this one
	// Parent is the orchestrator tab a worker shown on its own works for,
	// "" for a tab that is no worker.
	Parent string
	// Workers are an orchestrator's worker tabs, folded or not, and Fleet
	// counts them by column.
	Workers []string
	Fleet   [Columns]int
	Task    model.Task
	// GuardDeny is [agents] guard_deny, which the guard denies on top of
	// its built-in list.
	GuardDeny []string
	// Held is a card kept in its place under the pointer or keyboard
	// focus; Now is the kind it has moved on to.
	Held bool
	Now  Kind
}

// Column is the card's column.
func (c Card) Column() Column { return ColumnOf(c.Kind) }

// Quiet reports a working agent whose screen has been still a while.
func (c Card) Quiet() bool { return c.Kind == KindWorking && !c.Act.QuietSince.IsZero() }

// MaxCommand is how long a command may be, in bytes, and still be allowed
// from a card.
const MaxCommand = 180

// Answer is what a card offers for its approval.
type Answer int

const (
	AnswerNone    Answer = iota // not an approval pitwall can answer
	AnswerAllow                 // Allow and Deny
	AnswerLong                  // too long to read on a card: open the tab
	AnswerGuard                 // on the guard's deny list: open the tab
	AnswerUnknown               // no command known to check: open the tab
	AnswerChat                  // a managed chat's, answered in its tab
)

// Answers is what card c offers for its approval: Allow and Deny only for
// a short, one-line command the guard does not deny. A prompt whose command
// pitwall does not know, from a tool other than a shell or an older
// daemon, gets none.
func (c Card) Answers() Answer {
	switch {
	case c.Kind != KindApproval:
		return AnswerNone
	case c.Tab.Chat != "":
		// why: a chat takes ChatApprove with a request id that only its transcript carries.
		return AnswerChat
	case !remote.Answerable(c.Act):
		return AnswerNone
	}
	cmd := c.Act.Command
	switch {
	case cmd == "":
		return AnswerUnknown
	case len(cmd) > MaxCommand || strings.ContainsAny(cmd, "\r\n"):
		return AnswerLong
	case Denied(cmd, c.GuardDeny):
		return AnswerGuard
	}
	return AnswerAllow
}

// Denied reports whether the guard's built-in deny list, or extra, names a
// part of cmd.
func Denied(cmd string, extra []string) bool {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return guard.Check(extra, in) != ""
}

// ScopeKind is what a scope narrows the board to.
type ScopeKind int

const (
	ScopeAll       ScopeKind = iota
	ScopeProject             // one group, by its id
	ScopeUngrouped           // tabs in no group on this machine
	ScopeHost                // one machine, by its name
)

// Scope is the part of the window's tabs the board shows.
type Scope struct {
	Kind ScopeKind
	ID   string // the group's id, or the host's name
}

// Options are what Derive needs besides the state.
type Options struct {
	Scope  Scope
	Shells bool // show plain shells in their own column
	// Hosts is each session's machine by session id, "" or missing for
	// this one, and in a shared session each tab's by tab id, which wins.
	Hosts map[string]string
	// GuardDeny is [agents] guard_deny, for each card's Answers.
	GuardDeny []string
	Now       time.Time
}

// Board is the cards of each column, in order, and what it left out.
type Board struct {
	Cols [Columns][]Card
	// All is every tab's card, folded workers included, by id.
	All map[string]Card
	// Snoozed, Settled and HiddenShells count the tabs in scope the
	// board leaves out: snoozed or settled in the inbox, or plain shells
	// while Shells is off.
	Snoozed, Settled, HiddenShells int
}

// Count is how many cards column c shows.
func (b *Board) Count(c Column) int { return len(b.Cols[c]) }

// Derive sorts the state's tabs into columns. Workers fold into their
// orchestrator's card unless they need the user or failed.
func Derive(st *model.State, o Options) Board {
	b := Board{All: map[string]Card{}}
	acts := map[string][]model.Activity{}
	for _, a := range st.Activities {
		acts[a.WorkspaceID] = append(acts[a.WorkspaceID], a)
	}
	watching := st.Watching()
	var shown []Card
	for _, w := range st.Workspaces {
		host := cmp.Or(o.Hosts[w.ID], o.Hosts[w.SessionID])
		if w.Detached || !inScope(w, host, o.Scope) {
			continue
		}
		switch {
		case w.SnoozedUntil.After(o.Now):
			b.Snoozed++
			continue
		case !w.Settled.IsZero():
			b.Settled++
			continue
		}
		c := cardOf(st, w, acts[w.ID], watching[w.ID], o.Now)
		c.Host, c.GuardDeny = host, o.GuardDeny
		if c.Kind == KindShell && !o.Shells {
			b.HiddenShells++
			continue
		}
		b.All[c.ID] = c
		shown = append(shown, c)
	}
	// Workers count toward their orchestrator, and fold into it while they
	// work or are done.
	for i := range shown {
		c := &shown[i]
		root := st.Root(c.Tab)
		if root == "" {
			continue
		}
		j := slices.IndexFunc(shown, func(p Card) bool { return p.ID == root })
		if j < 0 {
			continue
		}
		c.Parent = root
		shown[j].Workers = append(shown[j].Workers, c.ID)
		shown[j].Fleet[c.Column()]++
	}
	for _, c := range shown {
		b.All[c.ID] = c
		if c.Parent != "" && folds(c.Column()) {
			continue
		}
		b.Cols[c.Column()] = append(b.Cols[c.Column()], c)
	}
	for _, t := range st.Tasks {
		if taskInScope(st, t, o) {
			c := Card{ID: "task:" + t.ID, Kind: KindTask, Task: t, Host: o.Hosts[t.SessionID]}
			b.Cols[Queued] = append(b.Cols[Queued], c)
		}
	}
	for col := range b.Cols {
		sortColumn(Column(col), b.Cols[col])
	}
	return b
}

// folds reports whether a worker in column c folds into its orchestrator.
func folds(c Column) bool { return c == Working || c == PROpen || c == Done }

func inScope(w model.Workspace, host string, s Scope) bool {
	switch s.Kind {
	case ScopeProject:
		return w.ProjectID == s.ID
	case ScopeUngrouped:
		return w.ProjectID == "" && host == ""
	case ScopeHost:
		return host == s.ID
	}
	return true
}

func taskInScope(st *model.State, t model.Task, o Options) bool {
	host := o.Hosts[t.SessionID]
	switch o.Scope.Kind {
	case ScopeProject:
		return t.GroupID == o.Scope.ID
	case ScopeUngrouped:
		return t.GroupID == "" && host == ""
	case ScopeHost:
		return host == o.Scope.ID
	}
	return true
}

// rank orders the kinds a tab's panes may have: the card shows its most
// pressing one.
var rank = map[Kind]int{
	KindApproval: 10, KindInput: 9, KindPlan: 8, KindError: 7, KindLimit: 6,
	KindWorking: 5, KindWatching: 5, KindRunning: 4, KindPR: 3, KindDone: 2,
}

// cardOf is tab w's card: its most pressing activity, or what its panes
// are when it has none.
func cardOf(st *model.State, w model.Workspace, acts []model.Activity, watching bool, now time.Time) Card {
	c := Card{ID: w.ID, Tab: w, Kind: -1}
	agentTab := len(st.AgentPanes(w)) > 0
	for _, a := range acts {
		if !agentTab && a.State == model.StateTerminalRunning {
			continue // a plain shell's command, unless it reports its status
		}
		k := kindOf(a, now)
		if watching && k == KindDone {
			k = KindWatching
		}
		if c.Kind < 0 || rank[k] > rank[c.Kind] || rank[k] == rank[c.Kind] && a.UpdatedAt.After(c.Act.UpdatedAt) {
			c.Kind, c.Act = k, a
		}
	}
	if c.Kind >= 0 {
		return c
	}
	if p := program(st, w); p != nil {
		if k, ok := programKind(*p); ok {
			c.Kind = k
			c.Act = model.Activity{WorkspaceID: w.ID, Detail: p.Message, UpdatedAt: p.UpdatedAt}
			return c
		}
	}
	switch {
	case agentTab:
		c.Kind = KindIdle
	case w.Parent != "":
		c.Kind = KindStopped
	default:
		c.Kind = KindShell
	}
	return c
}

// kindOf is the kind of one activity.
func kindOf(a model.Activity, now time.Time) Kind {
	if !a.Resumes.IsZero() {
		return KindLimit
	}
	switch a.State {
	case model.StatePendingApproval:
		return KindApproval
	case model.StateAwaitingInput:
		return KindInput
	case model.StatePlanReady:
		return KindPlan
	case model.StateError:
		if _, _, ok := agent.UsageLimit(a.Detail, now); ok {
			return KindLimit
		}
		return KindError
	case model.StateWorking, model.StateConnecting:
		return KindWorking
	case model.StateWatching:
		return KindWatching
	case model.StateTerminalRunning:
		return KindRunning
	case model.StatePROpen:
		return KindPR
	}
	return KindDone
}

// program is the most recent status report of w's panes, nil for none.
func program(st *model.State, w model.Workspace) *model.Program {
	var out *model.Program
	for i := range st.Panes {
		p := st.Panes[i].Program
		if st.Panes[i].WorkspaceID == w.ID && p != nil && (out == nil || p.UpdatedAt.After(out.UpdatedAt)) {
			out = p
		}
	}
	return out
}

// programKind is the kind a watched command's report shows as. A blocked
// command waits in Needs you, where its card opens the tab.
func programKind(p model.Program) (Kind, bool) {
	switch p.Activity() {
	case model.StateWorking:
		return KindRunning, true
	case model.StatePendingApproval, model.StateAwaitingInput:
		return KindInput, true
	case model.StateCompleted:
		return KindDone, true
	case model.StateError:
		return KindError, true
	}
	return 0, false
}

// sortColumn orders a column: Needs you by approvals, questions, then
// plans, each oldest first; Working with quiet agents on top; Done with
// unseen turns on top; pinned tabs first everywhere else.
func sortColumn(col Column, cs []Card) {
	pinned := func(c Card) int {
		if c.Tab.Pinned {
			return 0
		}
		return 1
	}
	flag := func(b bool) int {
		if b {
			return 0
		}
		return 1
	}
	newest := func(a, b Card) int { return b.Act.UpdatedAt.Compare(a.Act.UpdatedAt) }
	slices.SortStableFunc(cs, func(a, b Card) int {
		switch col {
		case NeedsYou:
			return cmp.Or(cmp.Compare(a.Kind, b.Kind), a.Act.UpdatedAt.Compare(b.Act.UpdatedAt))
		case Working:
			return cmp.Or(pinned(a)-pinned(b), flag(a.Quiet())-flag(b.Quiet()), newest(a, b))
		case Done:
			return cmp.Or(pinned(a)-pinned(b), flag(a.Act.Unseen)-flag(b.Act.Unseen), newest(a, b))
		case Queued:
			return 0
		}
		return cmp.Or(pinned(a)-pinned(b), newest(a, b))
	})
}

// NeedCount is how many cards wait on the user anywhere: the board
// button's badge.
func NeedCount(st *model.State, hosts map[string]string, now time.Time) int {
	b := Derive(st, Options{Hosts: hosts, Now: now})
	return b.Count(NeedsYou)
}
