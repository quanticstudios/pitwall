// Package decisionlog keeps decisions.jsonl in the state directory: one
// JSON line per decision-model call and per user outcome joined to it,
// for `pitwall jev report`. A line holds ids, times, latencies, answers,
// probabilities and token counts, never text: no prompt, command, tool
// input, file path, screen or error message.
package decisionlog

import (
	"encoding/json"
	"time"

	"github.com/quanticstudios/pitwall/internal/logs"
)

// Version is the schema version every line carries as "v".
const Version = 1

// MaxSize is the size at which the log moves to <name>.1, replacing the
// old one, so the two files hold at most about twice this.
const MaxSize = 10 << 20

// Kinds of line.
const (
	Call    = "call"    // the model answered, or failed
	Outcome = "outcome" // what the user did, joined to its call by ID
)

// User outcomes.
const (
	Allowed     = "allowed"     // approvals: the tool ran
	Denied      = "denied"      // approvals: the turn went on or ended without it
	Unknown     = "unknown"     // approvals: no event said which
	Focused     = "focused"     // triage: the pane was shown focused
	Prompted    = "prompted"    // turn check, check: the user sent the agent another prompt
	Corrected   = "corrected"   // turn check, check: that prompt reads as a correction (decide.Correction)
	Interrupted = "interrupted" // stuck: the user pressed Esc or Ctrl+C before the next ask or the turn's end
	Continued   = "continued"   // stuck: the agent went on, or its turn ended, without that
)

// Event is one line.
type Event struct {
	V       int       `json:"v"`
	T       time.Time `json:"t"`
	Kind    string    `json:"ev"`
	ID      string    `json:"id"` // the decision, random; joins a call and its outcome
	Feature string    `json:"feature"`
	Pane    string    `json:"pane,omitempty"`

	// A call: latency, the failure kind (decide.Meta.Err) or the answer.
	// Answer is the verdict, the urgency level, the screen state, or
	// "check"/"done" for a turn; P is its probability (for a turn, that it
	// needs review) and Conf the model's confidence.
	Ms        int64   `json:"ms,omitempty"`
	Err       string  `json:"err,omitempty"`
	Tokens    int     `json:"in_tokens,omitempty"`
	Estimated bool    `json:"tokens_estimated,omitempty"`
	Answer    string  `json:"answer,omitempty"`
	P         float64 `json:"p,omitempty"`
	Conf      float64 `json:"conf,omitempty"`
	// Probs is each yes/no question's probability of yes, for a call that
	// asked several.
	Probs map[string]float64 `json:"probs,omitempty"`
	// Base is the rule a call is measured against: for a turn, "check" or
	// "done" by decide.TurnRule; for an approval, "rule" when pitwall's
	// own risk flags answered and the model was not asked.
	Base string `json:"base,omitempty"`

	// Approvals and triage, on both kinds: the answer was hidden
	// (holdout); approvals: pitwall saw a risk in the call.
	Held bool `json:"held,omitempty"`
	Risk bool `json:"risk,omitempty"`

	// An outcome: what the user did and how long after the call's
	// prompt. Via is how an approval's answer was timed: "key", the last
	// key into the pane while it asked, or "hook", the event that showed
	// the answer.
	User   string `json:"user,omitempty"`
	WaitMs int64  `json:"wait_ms,omitempty"`
	Via    string `json:"via,omitempty"`
}

// Log appends events to the file without blocking the caller. A nil Log
// drops them.
type Log struct{ w *logs.Writer }

// Open opens path for appending, 0600 in a 0700 directory as pitwall's
// other logs.
func Open(path string) (*Log, error) { return open(path, MaxSize) }

func open(path string, max int64) (*Log, error) {
	r, err := logs.OpenRotating(path, max)
	if err != nil {
		return nil, err
	}
	return &Log{w: logs.NewWriter("decisionlog ", r, r)}, nil
}

// Add queues e with the schema version and its time cut to milliseconds.
func (l *Log) Add(e Event) {
	if l == nil {
		return
	}
	e.V, e.T = Version, e.T.UTC().Truncate(time.Millisecond)
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.w.Write(append(b, '\n'))
}

// Close writes what is queued, waiting at most timeout.
func (l *Log) Close(timeout time.Duration) {
	if l != nil {
		l.w.Close(timeout)
	}
}

// Read returns the events at or after since from path.1 and path, oldest
// first. Lines that are not events, such as the writer's note of dropped
// lines, are skipped; missing files are no events.
func Read(path string, since time.Time) ([]Event, error) {
	return logs.ReadJSONL(path, func(e Event) bool { return e.V != 0 && !e.T.Before(since) })
}
