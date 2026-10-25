// Package timelog keeps timing.jsonl in the state directory: one JSON line
// per finished agent turn, with its time split into the model, each kind
// of tool call and waiting on the user, and one per milestone of a tab,
// such as its pull request opening or merging, so Settings can show where
// agents' time goes. A line holds no prompt or output; the slowest
// commands of a turn are kept, redacted, to name the time sinks.
package timelog

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/logs"
)

// Version is the schema version every line carries as "v".
const Version = 1

// MaxSize is the size at which the log moves to <name>.1.
const MaxSize = 10 << 20

// Kinds of line: a turn, and a tab's milestones.
const (
	Turn    = "turn"
	Prompt  = "prompt" // the tab's first prompt the daemon saw
	PR      = "pr"     // its pull request opened
	Checks  = "ci"     // its checks finished, CI says pass or fail
	Merged  = "merged"
	Closed  = "closed" // the tab closed
	maxSlow = 5        // commands a turn keeps
	slowMin = 1000     // ms a command must run to be kept
	cmdLen  = 200      // runes a kept command keeps
)

// How a turn ended.
const (
	Completed   = "completed"
	Failed      = "error"
	Interrupted = "interrupted" // stopped by the user, or the agent exited
	PaneClosed  = "closed"
)

// Event is one line.
type Event struct {
	V        int       `json:"v"`
	T        time.Time `json:"t"` // when the turn ended, or the milestone happened
	Kind     string    `json:"kind"`
	Pane     string    `json:"pane,omitempty"`
	Tab      string    `json:"tab,omitempty"`
	Project  string    `json:"project,omitempty"`
	Name     string    `json:"name,omitempty"` // the tab's name
	Branch   string    `json:"branch,omitempty"`
	Provider string    `json:"provider,omitempty"`

	// A turn. The parts, in ms, add up to Ms.
	Start time.Time        `json:"start,omitzero"`
	End   string           `json:"end,omitempty"`
	Ms    int64            `json:"ms,omitempty"`
	Model int64            `json:"model,omitempty"`
	Wait  int64            `json:"wait,omitempty"`  // waiting on the user: approvals, questions, plans
	Tools map[string]int64 `json:"tools,omitempty"` // by kind
	Reply int64            `json:"reply,omitempty"` // from the pane's previous turn's end to this start
	Slow  []Cmd            `json:"slow,omitempty"`

	// A milestone.
	PR int    `json:"pr,omitempty"`
	CI string `json:"ci,omitempty"` // pass or fail
}

// Cmd is one of a turn's slowest commands.
type Cmd struct {
	Command string `json:"command"`
	Kind    string `json:"kind"`
	Ms      int64  `json:"ms"`
}

// Agent is the turn's time the agent worked, waiting on the user left out.
func (e Event) Agent() int64 { return e.Ms - e.Wait }

// Log appends events to the file without blocking the caller. A nil Log
// drops them.
type Log struct{ w *logs.Writer }

// Open opens path for appending, 0600 in a 0700 directory.
func Open(path string) (*Log, error) {
	r, err := logs.OpenRotating(path, MaxSize)
	if err != nil {
		return nil, err
	}
	return &Log{w: logs.NewWriter("timelog ", r, r)}, nil
}

// Add queues e with its commands redacted, secrets and each of known,
// and cut to cmdLen runes.
func (l *Log) Add(e Event, known ...string) {
	if l == nil {
		return
	}
	e.V, e.T = Version, e.T.UTC().Truncate(time.Millisecond)
	if !e.Start.IsZero() {
		e.Start = e.Start.UTC().Truncate(time.Millisecond)
	}
	for i := range e.Slow {
		e.Slow[i].Command = redact(e.Slow[i].Command, known)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.w.Write(append(b, '\n'))
}

// redact is the start of a command with its secrets redacted. The daemon
// holds its lock, so a long one is cut first, at a space, where a secret
// cut in two would no longer match its pattern.
func redact(s string, known []string) string {
	if r := []rune(s); len(r) > 2*cmdLen {
		c := string(r[:2*cmdLen])
		s = c[:max(0, strings.LastIndexFunc(c, unicode.IsSpace))]
	}
	s = strings.Join(strings.Fields(decide.Redact(s, known...)), " ")
	if utf8.RuneCountInString(s) > cmdLen {
		s = string([]rune(s)[:cmdLen-1]) + "…"
	}
	return s
}

// Close writes what is queued, waiting at most timeout.
func (l *Log) Close(timeout time.Duration) {
	if l != nil {
		l.w.Close(timeout)
	}
}

// Read returns the events at or after since from path.1 and path, oldest
// first.
func Read(path string, since time.Time) ([]Event, error) {
	return logs.ReadJSONL(path, func(e Event) bool { return e.V != 0 && !e.T.Before(since) })
}
