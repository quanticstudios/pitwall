// Package errlog keeps errors.jsonl in the state directory: one JSON line
// per command, tool call or turn of an agent that failed, so Settings can
// show which failures repeat. Unlike decisions.jsonl a line holds text, the
// command and the tail of its output, with secrets redacted first.
package errlog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/logs"
)

// Version is the schema version every line carries as "v".
const Version = 1

// MaxSize is the size at which the log moves to <name>.1.
const MaxSize = 10 << 20

// Kinds of error.
const (
	Command = "command" // a shell command failed
	Tool    = "tool"    // another tool call failed, such as Edit or Read
	API     = "api"     // the API ended the turn
)

// How much text a line keeps: the start of the command, and the end of
// the output, where errors are.
const (
	commandLen = 300
	outputLen  = 600
)

// Event is one line.
type Event struct {
	V        int       `json:"v"`
	T        time.Time `json:"t"`
	Pane     string    `json:"pane,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Kind     string    `json:"kind"`
	Tool     string    `json:"tool,omitempty"`
	Command  string    `json:"command,omitempty"`
	Output   string    `json:"output,omitempty"`
	Exit     int       `json:"exit,omitempty"`
	Cwd      string    `json:"cwd,omitempty"`
}

// Log appends events to the file without blocking the caller. A nil Log
// drops them.
type Log struct{ w *logs.Writer }

// Open opens path for appending, 0600 in a 0700 directory.
func Open(path string) (*Log, error) {
	r, err := logs.OpenRotating(path, MaxSize)
	if err != nil {
		return nil, err
	}
	return &Log{w: logs.NewWriter("errlog ", r, r)}, nil
}

// Add queues e with its command and output redacted, secrets and each of
// known, then cut to commandLen and the last outputLen runes.
func (l *Log) Add(e Event, known ...string) {
	if l == nil {
		return
	}
	e.V, e.T = Version, e.T.UTC().Truncate(time.Millisecond)
	if r := []rune(strings.TrimSpace(decide.Redact(e.Command, known...))); len(r) > commandLen {
		e.Command = string(r[:commandLen-1]) + "…"
	} else {
		e.Command = string(r)
	}
	if r := []rune(strings.TrimSpace(decide.Redact(e.Output, known...))); len(r) > outputLen {
		e.Output = "…" + string(r[len(r)-outputLen+1:])
	} else {
		e.Output = string(r)
	}
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
// first.
func Read(path string, since time.Time) ([]Event, error) {
	return logs.ReadJSONL(path, func(e Event) bool { return e.V != 0 && !e.T.Before(since) })
}

// Group is the events of one signature.
type Group struct {
	Sig       string
	Count     int
	Providers []string // sorted
	Last      time.Time
	Example   Event // the most recent
}

// Groups puts evs together by Signature, the most frequent first.
func Groups(evs []Event) []Group {
	at := map[string]int{}
	var out []Group
	for _, e := range evs {
		sig := Signature(e)
		i, ok := at[sig]
		if !ok {
			i = len(out)
			at[sig] = i
			out = append(out, Group{Sig: sig})
		}
		g := &out[i]
		g.Count++
		if e.Provider != "" && !slices.Contains(g.Providers, e.Provider) {
			g.Providers = append(g.Providers, e.Provider)
			slices.Sort(g.Providers)
		}
		if !e.T.Before(g.Last) {
			g.Last, g.Example = e.T, e
		}
	}
	slices.SortStableFunc(out, func(a, b Group) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return b.Last.Compare(a.Last)
	})
	return out
}

// errorWords mark the line of an output that says what went wrong.
var errorWords = []string{"error", "not found", "no such", "no matches", "permission denied", "unknown", "invalid", "unrecognized", "cannot", "failed", "fatal", "zsh:", "bash:"}

// Placeholders, applied in order: quoted text first, as it may hold the
// rest; a glob before a path, as /x/* is both. A path starts the line or
// follows a space, =, : or bracket; its first character is kept.
var (
	quoted = regexp.MustCompile(`"[^"]*"|'[^']*'|` + "`[^`]*`")
	glob   = regexp.MustCompile(`\S*\*\S*`)
	path   = regexp.MustCompile(`(^|[\s=:(\[])~?/[^\s:,;)\]]+`)
	hex    = regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{7,}\b`)
	digits = regexp.MustCompile(`\d+`)
)

// sigLen caps a signature.
const sigLen = 160

// Signature is what makes two errors the same: the first line of the
// output with an error word in it, or else its last line, with quoted
// text, globs, paths, hex ids and numbers made placeholders.
func Signature(e Event) string {
	line := ""
	for l := range strings.Lines(e.Output) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if slices.ContainsFunc(errorWords, func(w string) bool { return strings.Contains(low, w) }) {
			line = l
			break
		}
		line = l
	}
	line = strings.TrimPrefix(line, "…")
	line = quoted.ReplaceAllString(line, "<str>")
	line = glob.ReplaceAllString(line, "<glob>")
	line = path.ReplaceAllString(line, "$1<path>")
	line = hex.ReplaceAllStringFunc(line, func(m string) string {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(strings.ToLower(m), "abcdef") {
			return "<hex>"
		}
		return m // a word such as "defaced", or a number
	})
	line = digits.ReplaceAllString(line, "N")
	line = strings.Join(strings.Fields(line), " ")
	if r := []rune(line); len(r) > sigLen {
		line = string(r[:sigLen-1]) + "…"
	}
	if line != "" {
		return line
	}
	switch {
	case e.Kind == API:
		return "API error"
	case e.Tool != "":
		return e.Tool + " failed"
	}
	return "failed with no output"
}

// Report is the first n groups as Markdown, to paste into an agent and
// ask it to fix the causes. span names the window, such as "the last 7
// days".
func Report(gs []Group, n int, span string) string {
	total := 0
	for _, g := range gs {
		total += g.Count
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent errors, %s\n\n", span)
	fmt.Fprintf(&b, "My coding agents hit these %d errors, grouped by cause, most frequent first. Find why each happens and fix it in my setup: CLAUDE.md or AGENTS.md, shell config, or a missing tool.\n", total)
	for i, g := range gs[:min(n, len(gs))] {
		e := g.Example
		fmt.Fprintf(&b, "\n## %d. %s\n\n×%d, %s, last %s\n", i+1, g.Sig, g.Count, strings.Join(g.Providers, ", "), g.Last.Local().Format("2006-01-02 15:04"))
		if e.Cwd != "" {
			fmt.Fprintf(&b, "cwd: %s\n", e.Cwd)
		}
		if e.Command != "" {
			label := "command"
			if e.Kind == Tool {
				label = e.Tool
			}
			fmt.Fprintf(&b, "\n%s:\n```\n%s\n```\n", label, e.Command)
		}
		if e.Output != "" {
			fmt.Fprintf(&b, "\noutput:\n```\n%s\n```\n", e.Output)
		}
	}
	return b.String()
}
