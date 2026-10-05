package flow

import (
	"encoding/json"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
)

// builder collects one session file's Feed, line by line. The provider
// parsers drive it; it knows nothing of their formats.
type builder struct {
	provider model.Provider
	turns    []Turn
	plan     []Step
	planIDs  []string // Claude's task ids beside plan; nil unless the Task tools own it
	subs     []*sub
	calls    map[string]ref // a kept call's id to where it is
	dropped  int            // turns dropped from the front, for maxTurns
	aborted  bool           // the last turn was aborted; it does not reopen
	last     string         // the latest text reply, cut to 2000 runes
	handback time.Time      // Claude: when a subagent's SubagentHandback got its result
}

// maxTurns is how many of the latest turns a Feed keeps.
const maxTurns = 50

// ref is a call's place: its turn counted from the session's first, then
// its index in the turn.
type ref struct{ turn, call int }

// sub is a Subagent as the parent's file tells it, plus the child session
// whose own file adds its calls, latest text and end.
type sub struct {
	Subagent
	key    string // what the provider's later events name it by
	thread string // Codex: the child's thread id, which names its rollout
	file   string // the child's own session file, "" until known
	final  bool   // the parent recorded its result: End, Failed and Latest stay
	kid    *session
	done   bool      // kid was read after the subagent ended, or the look ended
	ended  time.Time // when a poll first saw the subagent ended, for lookFor
}

func newBuilder(provider model.Provider) *builder {
	return &builder{provider: provider, calls: map[string]ref{}}
}

// prompt starts a turn. A turn still open ends where the next one starts.
func (b *builder) prompt(ts time.Time, text string) {
	if n := len(b.turns); n > 0 && b.turns[n-1].End.IsZero() {
		b.turns[n-1].End = ts
	}
	b.add(Turn{Prompt: cut(firstLine(text), 200), Start: ts})
}

// add appends t, then drops the oldest turns past maxTurns, with the ids
// of their calls.
func (b *builder) add(t Turn) {
	b.turns, b.aborted = append(b.turns, t), false
	n := len(b.turns) - maxTurns
	if n <= 0 {
		return
	}
	b.turns = slices.Clone(b.turns[n:])
	b.dropped += n
	for id, r := range b.calls {
		if r.turn < b.dropped {
			delete(b.calls, id)
		}
	}
}

// callByID is the kept call with that id, or nil.
func (b *builder) callByID(id string) *Call {
	r, ok := b.calls[id]
	if !ok {
		return nil
	}
	return &b.turns[r.turn-b.dropped].Calls[r.call]
}

// active is the turn the agent works on at ts: the last one, open again if
// it had ended, or a new turn without a prompt when the read began mid-turn
// or the last turn was aborted. Only the agent's own activity calls it,
// never a call's result.
func (b *builder) active(ts time.Time) *Turn {
	if len(b.turns) == 0 || b.aborted {
		b.add(Turn{Start: ts})
	}
	t := &b.turns[len(b.turns)-1]
	t.End = time.Time{}
	return t
}

func (b *builder) call(ts time.Time, id, tool, arg string) {
	t := b.active(ts)
	t.Calls = append(t.Calls, Call{Time: ts, Tool: tool, Arg: cut(arg, 120), Running: true})
	if id != "" {
		b.calls[id] = ref{b.dropped + len(b.turns) - 1, len(t.Calls) - 1}
	}
}

// result records a call's result, in its turn even if that ended; it
// reports false for an unknown call.
func (b *builder) result(id string, failed bool) bool {
	c := b.callByID(id)
	if c == nil {
		return false
	}
	c.Running, c.Failed = false, c.Failed || failed
	return true
}

func (b *builder) reply(ts time.Time, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.active(ts).Reply = cut(text, 600)
	b.last = cut(text, 2000)
}

// pending reports whether the last turn has a call without a result.
func (b *builder) pending() bool {
	if len(b.turns) == 0 {
		return false
	}
	return slices.ContainsFunc(b.turns[len(b.turns)-1].Calls, func(c Call) bool { return c.Running })
}

// end ends the last turn at ts unless it already ended. An aborted turn's
// calls never get their results, so they count as failed.
func (b *builder) end(ts time.Time, aborted bool) {
	if len(b.turns) == 0 {
		return
	}
	t := &b.turns[len(b.turns)-1]
	if t.End.IsZero() {
		t.End = ts
	}
	if aborted {
		b.aborted = true
		for i := range t.Calls {
			if t.Calls[i].Running {
				t.Calls[i].Running, t.Calls[i].Failed = false, true
			}
		}
	}
}

// spawn adds a subagent started by call id during the active turn.
func (b *builder) spawn(ts time.Time, id, name, typ, prompt string) *sub {
	s := &sub{Subagent: Subagent{ID: id, Name: cut(firstLine(name), 200), Type: typ, Prompt: cut(prompt, 2000), Start: ts}}
	b.subs = append(b.subs, s)
	t := b.active(ts)
	t.Subagents = append(t.Subagents, len(b.subs)-1)
	return s
}

// subBy returns the subagent whose call id or key is id, or nil.
func (b *builder) subBy(id string) *sub {
	if id == "" {
		return nil
	}
	for _, s := range b.subs {
		if s.ID == id || s.key == id {
			return s
		}
	}
	return nil
}

func (b *builder) setPlan(steps []Step) {
	b.plan, b.planIDs = steps, nil
	if b.plan == nil {
		b.plan = []Step{}
	}
}

func stepState(status string) StepState {
	switch status {
	case "in_progress":
		return StepActive
	case "completed":
		return StepDone
	}
	return StepPending
}

// feed is a copy of what b holds that shares nothing with it.
func (b *builder) feed() Feed {
	f := Feed{Provider: b.provider, Plan: slices.Clone(b.plan)}
	for _, t := range b.turns {
		t.Calls, t.Subagents = slices.Clone(t.Calls), slices.Clone(t.Subagents)
		f.Turns = append(f.Turns, t)
	}
	for _, s := range b.subs {
		f.Subagents = append(f.Subagents, s.view())
	}
	return f
}

// view is the subagent with what its own file adds: the calls of its turns
// that started at or after its spawn (a forked Codex child repeats its
// parent's history first), its latest text, and its end (its handback, else
// the end of the last such turn), unless the parent already recorded its
// result.
func (s *sub) view() Subagent {
	v := s.Subagent
	v.Calls = slices.Clone(v.Calls)
	if s.kid == nil {
		return v
	}
	kb := s.kid.b
	v.Calls = nil
	var last *Turn
	for i, t := range kb.turns {
		if !t.Start.Before(v.Start) {
			v.Calls = append(v.Calls, t.Calls...)
			last = &kb.turns[i]
		}
	}
	if !s.final {
		if kb.last != "" {
			v.Latest = kb.last
		}
		switch {
		case !v.End.IsZero():
		case !kb.handback.IsZero():
			v.End = kb.handback
		case last != nil:
			v.End = last.End
		}
	}
	return v
}

// cut trims s and keeps its first n runes.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// arg is Call.Arg for a tool's input: a command's first line, a file's base
// name, a pattern or query, a fetched URL's host, else "".
func arg(tool string, input json.RawMessage) string {
	var in map[string]json.RawMessage
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			var s string
			if json.Unmarshal(in[k], &s) == nil && s != "" {
				return s
			}
		}
		return ""
	}
	switch tool {
	case "Bash", "bash", "exec_command", "shell_command", "shell":
		var argv []string
		if json.Unmarshal(in["command"], &argv) == nil {
			return command(argv)
		}
		return firstLine(str("command", "cmd"))
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "read", "edit", "write":
		if p := str("file_path", "path", "notebook_path"); p != "" {
			return path.Base(strings.ReplaceAll(p, `\`, "/"))
		}
	case "apply_patch":
		return patchFile(str("input", "patch"))
	case "Grep", "Glob", "grep", "find":
		return firstLine(str("pattern"))
	case "WebSearch", "web_search":
		return firstLine(str("query"))
	case "WebFetch":
		if u, err := url.Parse(str("url")); err == nil {
			return u.Host
		}
	}
	return ""
}

// command is the line an argv runs: the script of `bash -lc script`, else
// the words joined.
func command(argv []string) string {
	if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return firstLine(argv[2])
	}
	return firstLine(strings.Join(argv, " "))
}

// patchFile is the base name of the first file an apply_patch patch touches.
func patchFile(patch string) string {
	for line := range strings.Lines(patch) {
		for _, p := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if f, ok := strings.CutPrefix(line, p); ok {
				return path.Base(strings.TrimSpace(f))
			}
		}
	}
	return ""
}
