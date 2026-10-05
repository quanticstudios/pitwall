// Package flow reads a coding agent's own session file and turns it into what
// the side panel shows: the turns, their tool calls, the plan and the
// subagents.
//
// The files are Claude Code's transcript (and its subagents/ directory),
// Codex's rollout and pi's session, all JSONL. Their text stays in memory:
// nothing here logs or saves a prompt, a reply or a tool argument.
package flow

import (
	"context"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Feed is one agent session as the panel shows it.
type Feed struct {
	Provider model.Provider
	// Turns are oldest first. The last one is the current turn while End is
	// zero, else the latest finished one. Turns before the part of the file
	// Watch read are missing.
	Turns []Turn
	// Plan is the agent's latest todo list: Claude's TodoWrite or Task
	// tools, Codex's update_plan. nil when the session never wrote one.
	Plan []Step
	// Subagents are in the order they were spawned.
	Subagents []Subagent
}

// Turn is one user prompt and everything the agent did for it.
type Turn struct {
	Prompt     string    // first line of the prompt, cut to 200 runes
	Start, End time.Time // End is zero while the turn runs
	Calls      []Call
	Reply      string // start of the agent's last text reply, cut to 600 runes
	Subagents  []int  // indexes into Feed.Subagents spawned during the turn
}

// Errors is how many of the turn's calls failed.
func (t Turn) Errors() int {
	n := 0
	for _, c := range t.Calls {
		if c.Failed {
			n++
		}
	}
	return n
}

// Call is one tool call.
type Call struct {
	Time time.Time
	Tool string // the tool's name as the agent calls it: "Bash", "exec", "read"
	// Arg is a short label for display only: a command's first line, a
	// file's base name, a search query. Cut to 120 runes.
	Arg     string
	Running bool // no result yet
	Failed  bool
}

// StepState is where a plan step is.
type StepState int

const (
	StepPending StepState = iota
	StepActive
	StepDone
)

// Step is one item of the plan.
type Step struct {
	Text  string
	State StepState
}

// Subagent is one agent the session spawned.
type Subagent struct {
	ID     string
	Name   string // the spawn's short description
	Type   string // Claude's subagent_type, "" when unknown
	Prompt string // what it was asked, cut to 2000 runes
	Start  time.Time
	End    time.Time // zero while it runs
	Failed bool
	// Latest is its last text: a progress message while it runs, its
	// result once it is done. Cut to 2000 runes.
	Latest string
	Calls  []Call
}

// Running reports whether the subagent has not finished.
func (s Subagent) Running() bool { return s.End.IsZero() }

// Watch reads the session file at path, written by provider, and calls
// changed with a fresh Feed after the first read and after every change,
// until ctx is done. It reads at most the file's last 8 MiB and then only
// what is appended, from its own goroutine; changed must not block. A
// missing or unreadable file yields an empty Feed and is retried.
func Watch(ctx context.Context, provider model.Provider, path string, changed func(Feed)) {
	go watch(ctx, provider, path, changed)
}
