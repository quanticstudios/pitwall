// Package errlog keeps errors.jsonl in the state directory: one JSON line
// per command, tool call or turn of an agent that failed, so Settings can
// show which failures repeat. Unlike decisions.jsonl a line holds text, the
// command and the tail of its output, with secrets redacted first.
package errlog

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
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
	// Quiet marks a command that exited 1 with no output, as grep finding
	// nothing does; Interrupted one the user stopped. Both are kept, in
	// groups of their own.
	Quiet       bool `json:"quiet,omitempty"`
	Interrupted bool `json:"interrupted,omitempty"`
	// Size is the runes of the command and the whole output before the
	// cut: about what the failure cost the agent in tokens, see Tokens.
	Size int   `json:"size,omitempty"`
	Ms   int64 `json:"ms,omitempty"` // how long the call ran, when the agent says
}

// Tokens is about how many tokens e's command and output took.
// ponytail: four runes a token, an average for English and code; a
// tokenizer per model would be exact.
func (e Event) Tokens() int { return e.Size / 4 }

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
	if e.Size == 0 {
		e.Size = utf8.RuneCountInString(e.Command) + utf8.RuneCountInString(e.Output)
	}
	// why: the caller holds the daemon's lock, and a build's output can run
	// to megabytes, so Redact sees only a few times what the line keeps.
	// A secret cut at that edge would no longer match its pattern, so the
	// cut text loses its partial line, or partial word when it is one line.
	cmd, out := []rune(e.Command), []rune(e.Output)
	if len(cmd) > 2*commandLen {
		c := string(cmd[:2*commandLen])
		i := strings.LastIndexByte(c, '\n')
		if i < 0 {
			i = max(0, strings.LastIndexFunc(c, unicode.IsSpace)) // none: nothing is left
		}
		cmd = []rune(c[:i])
	}
	if len(out) > 8*outputLen {
		o := string(out[len(out)-8*outputLen:])
		i := strings.IndexByte(o, '\n')
		if i < 0 {
			i = strings.IndexFunc(o, unicode.IsSpace)
		}
		if i < 0 {
			i = len(o)
		}
		// A key block cut after its BEGIN line goes through its END line.
		if end, begin := strings.Index(o, "-----END"), strings.Index(o, "-----BEGIN"); end >= 0 && (begin < 0 || end < begin) {
			n := strings.IndexByte(o[end:], '\n')
			if n < 0 {
				n = len(o) - end
			}
			i = max(i, end+n)
		}
		out = []rune(o[i:])
	}
	cmd, out = []rune(strings.TrimSpace(decide.Redact(string(cmd), known...))), []rune(strings.TrimSpace(decide.Redact(string(out), known...)))
	if len(cmd) > commandLen {
		e.Command = string(cmd[:commandLen-1]) + "…"
	} else {
		e.Command = string(cmd)
	}
	if len(out) > outputLen {
		e.Output = "…" + string(out[len(out)-outputLen+1:])
	} else {
		e.Output = string(out)
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
	Tokens    int      // the sum of the events' Tokens
	Providers []string // sorted
	Last      time.Time
	Example   Event // the most recent
}

// Groups puts evs together by Signature, the most frequent first; see
// ByTokens for the costliest.
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
		g.Tokens += e.Tokens()
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

// ByTokens is gs, a copy, the most tokens first.
func ByTokens(gs []Group) []Group {
	out := slices.Clone(gs)
	slices.SortStableFunc(out, func(a, b Group) int { return cmp.Or(b.Tokens-a.Tokens, b.Count-a.Count) })
	return out
}

// Top is gs's first n groups, and the rest as one group with no Sig.
func Top(gs []Group, n int) ([]Group, Group) {
	n = min(n, len(gs))
	var rest Group
	for _, g := range gs[n:] {
		rest.Count += g.Count
		rest.Tokens += g.Tokens
	}
	return gs[:n], rest
}

// Day is the errors on Date, local time: how many of each kind and their
// tokens.
type Day struct {
	Date   time.Time
	Kinds  map[string]int
	Count  int
	Tokens int
}

// Days counts evs by the day they happened in loc, one Day for every day
// from from to to, empty ones too.
func Days(evs []Event, from, to time.Time, loc *time.Location) []Day {
	f := from.In(loc)
	day0 := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
	var out []Day
	at := map[time.Time]int{}
	for d := day0; !d.After(to); d = d.AddDate(0, 0, 1) {
		at[d] = len(out)
		out = append(out, Day{Date: d, Kinds: map[string]int{}})
	}
	for _, e := range evs {
		t := e.T.In(loc)
		i, ok := at[time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)]
		if !ok || e.T.Before(from) || e.T.After(to) {
			continue
		}
		out[i].Kinds[e.Kind]++
		out[i].Count++
		out[i].Tokens += e.Tokens()
	}
	return out
}

// errorWords mark the line of an output that says what went wrong.
var errorWords = []string{"error", "not found", "no such", "no matches", "permission denied", "unknown", "invalid", "unrecognized", "cannot", "failed", "fatal", "zsh:", "bash:"}

// Placeholders, applied in order: quoted text first, as it may hold the
// rest; a glob before a path, as /x/* is both. A path, absolute or with a
// slash in it, starts the line or follows a space, =, : or bracket; that
// character is kept. A number next to a letter, as in TS2304, is part of a
// code and stays. A shell's line number goes: zsh:1: is zsh:.
var (
	quoted = regexp.MustCompile(`"[^"]*"|'[^']*'|` + "`[^`]*`")
	glob   = regexp.MustCompile(`\S*\*\S*`)
	path   = regexp.MustCompile(`(^|[\s=:(\[])(?:~?/[^\s:,;()\]]+|[\w.@+-]+(?:/[\w.@+-]+)+)`)
	hex    = regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{7,}\b`)
	digits = regexp.MustCompile(`(^|[^A-Za-z\d])\d+`)
	shell  = regexp.MustCompile(`^(\w+):N:`)
)

// sigLen caps a signature.
const sigLen = 160

// Signature is what makes two errors the same: the first line of the
// output with an error word in it, or else its last line, with quoted
// text, globs, paths, hex ids and numbers made placeholders.
func Signature(e Event) string {
	switch {
	case e.Interrupted:
		return "Interrupted by you: " + head(e)
	case e.Quiet:
		return "Exit 1 with no output: " + head(e)
	}
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
	line = digits.ReplaceAllString(line, "${1}N")
	line = shell.ReplaceAllString(line, "$1:")
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

// head is the program e ran, such as "grep", or its tool.
func head(e Event) string {
	if f := strings.Fields(e.Command); e.Kind == Command && len(f) > 0 {
		return filepath.Base(f[0])
	}
	return cmp.Or(e.Tool, "a call")
}

// Report is the first n groups as Markdown, to paste into an agent and
// ask it to fix the causes. span names the window, such as "the last 7
// days".
func Report(gs []Group, n int, span string) string {
	total, toks := 0, 0
	for _, g := range gs {
		total, toks = total+g.Count, toks+g.Tokens
	}
	gs = gs[:min(n, len(gs))]
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent errors, %s\n\n", span)
	fmt.Fprintf(&b, "My coding agents hit these %d errors, about %s tokens of commands and output, grouped by cause, most frequent first. "+
		"Each one is a wasted step. Find why each happens and fix it in my setup: CLAUDE.md or AGENTS.md, shell config, or a missing tool. "+
		"\"Exit 1 with no output\" and \"Interrupted by you\" groups may not be bugs; check whether the agent should run something else.\n", total, short(toks))
	if len(gs) > 1 {
		var top []string
		for _, g := range ByTokens(gs)[:min(3, len(gs))] {
			top = append(top, fmt.Sprintf("%d (%s tokens)", slices.IndexFunc(gs, func(x Group) bool { return x.Sig == g.Sig })+1, short(g.Tokens)))
		}
		fmt.Fprintf(&b, "\nCostliest: %s.\n", strings.Join(top, ", "))
	}
	for i, g := range gs {
		e := g.Example
		fmt.Fprintf(&b, "\n## %d. %s\n\n×%d, about %s tokens, %s, last %s\n", i+1, g.Sig, g.Count, short(g.Tokens), strings.Join(g.Providers, ", "), g.Last.Local().Format("2006-01-02 15:04"))
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

// short is n tokens for a person: 840, 3.1k, 2.4M.
func short(n int) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
