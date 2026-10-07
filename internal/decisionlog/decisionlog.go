// Package decisionlog keeps decisions.jsonl in the state directory: one
// JSON line per decision-model call and per user outcome joined to it,
// for `pitwall jev report`. A line holds ids, times, latencies, answers,
// probabilities and token counts, never text: no prompt, command, tool
// input, file path, screen or error message.
package decisionlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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
	Allowed  = "allowed"  // approvals: the tool ran
	Denied   = "denied"   // approvals: the turn went on or ended without it
	Unknown  = "unknown"  // approvals: no event said which
	Focused  = "focused"  // triage: the pane was shown focused
	Prompted = "prompted" // turn check: the user sent the agent another prompt
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

	// Approvals, on both kinds: the verdict was hidden (holdout), and
	// pitwall saw a risk in the call.
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
	r := &rotating{path: path, max: max}
	if err := r.reopen(); err != nil {
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

// rotating is the file, moved to .1 when a line would take it past max.
// Only the Writer's goroutine uses it.
type rotating struct {
	path string
	max  int64
	f    *os.File
	size int64
}

func (r *rotating) reopen() error {
	f, err := logs.OpenFile(r.path, r.max)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *rotating) Write(p []byte) (int, error) {
	if r.f != nil && r.size > 0 && r.size+int64(len(p)) > r.max {
		r.f.Close()
		r.f = nil
		os.Rename(r.path, r.path+".1")
	}
	if r.f == nil {
		if err := r.reopen(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotating) Close() error {
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

// Read returns the events at or after since from path.1 and path, oldest
// first. Lines that are not events, such as the writer's note of dropped
// lines, are skipped; missing files are no events.
func Read(path string, since time.Time) ([]Event, error) {
	var out []Event
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.V == 0 || e.T.Before(since) {
				continue
			}
			out = append(out, e)
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
