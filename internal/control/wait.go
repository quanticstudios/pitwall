package control

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Why a wait ended, Outcome.Reason.
const (
	Reached = "reached" // the pane got to the state waited for
	Blocked = "blocked" // its agent waits on the user
	Exited  = "exited"  // its process ended
	Closed  = "closed"  // it went without an exit code: closed on purpose
)

// Wait is one wait on a pane until it gets to a state: "done", "idle",
// "blocked" or "exit". It remembers what it saw across states.
type Wait struct {
	until    string
	saw      bool // a turn ran during this wait
	hadAgent bool // the pane ran an agent
}

// NewWait starts a wait until a state.
func NewWait(until string) *Wait { return &Wait{until: until} }

// Outcome is where a wait got to. Reason is "" while it goes on.
type Outcome struct {
	Reason   string
	State    string // the pane's state, for Reached
	Question string // the agent's question, for Blocked
	Exit     *Exit  // for Exited
	// Awaited says the exit is what the wait was for: until exit, or until
	// done on a pane that never ran an agent. The program's own code is
	// then the answer.
	Awaited bool
}

// Exit is how a pane's process ended.
type Exit struct {
	Code    int
	Unknown bool // the daemon restarted while it ran: the code was lost
	Signal  bool // a signal killed it; Code is negative
}

// Step takes the next state. exit looks up pane's exit code once the pane
// is gone from state or has exited, and says whether one is known; Step
// calls it only then.
func (w *Wait) Step(state model.State, pane string, exit func() (int, bool)) Outcome {
	i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == pane })
	var agent, st, question string
	if i >= 0 {
		agent, st, question = PaneState(state, state.Panes[i])
	}
	w.hadAgent = w.hadAgent || agent != ""
	if i < 0 || st == "exited" {
		if i >= 0 && state.Panes[i].ExitUnknown {
			// why: the daemon restarted while the command ran, and did not run it again.
			return Outcome{Reason: Exited, Exit: &Exit{Unknown: true}}
		}
		code, ok := exit()
		if !ok {
			return Outcome{Reason: Closed}
		}
		return Outcome{Reason: Exited, Exit: &Exit{Code: code, Signal: code < 0}, Awaited: w.until == "exit" || w.until == "done" && !w.hadAgent}
	}
	w.saw = w.saw || st == "working" || st == "blocked"
	switch {
	case st == w.until, w.until == "idle" && (st == "done" || st == "pr-open"),
		(w.until == "done" || w.until == "idle") && st == "parked", // its turn ended before it parked
		w.until == "done" && st == "idle" && w.saw:                 // an interrupted turn ends idle
		return Outcome{Reason: Reached, State: st}
	case st == "blocked":
		return Outcome{Reason: Blocked, Question: question}
	}
	return Outcome{}
}
