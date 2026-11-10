package flow

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Entry is one item of an agent session as the phone's chat shows it: a
// prompt, a reply, a tool call with its result, or an error. Thinking and
// the agent's own bookkeeping are left out.
type Entry struct {
	Seq    int64  `json:"seq"`
	Kind   string `json:"kind"` // user, assistant, tool, error
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Arg    string `json:"arg,omitempty"` // a short summary: the command, the file
	Out    string `json:"out,omitempty"` // the call's result, cut to maxOut bytes
	Done   bool   `json:"done,omitempty"`
	Failed bool   `json:"failed,omitempty"`
	rev    int64
}

// Entry kinds.
const (
	EntryUser      = "user"
	EntryAssistant = "assistant"
	EntryTool      = "tool"
	EntryError     = "error"
)

// Limits on what a Log keeps.
const (
	maxEntries = 2000
	maxText    = 32000 // runes of a prompt or a reply
	maxOut     = 4000  // bytes of a call's result
	maxArg     = 200   // runes of a call's summary
)

// Log is an agent session file read as chat entries, from the part of the
// file the first read takes (its last 8 MiB) on. It is not safe for
// concurrent use.
type Log struct {
	Provider model.Provider
	Path     string
	// Gen counts the times the file started over (replaced or truncated):
	// entries read before are void. Rev counts every change.
	Gen, Rev int64
	// Model is the model the session's latest turn named, "" when unknown.
	Model string

	t       tail
	entries []Entry
	next    int64
	calls   map[string]int64 // a call's id to its entry's seq
}

// NewLog reads nothing yet; Poll does.
func NewLog(provider model.Provider, path string) *Log {
	return &Log{Provider: provider, Path: path, t: tail{path: path}, next: 1, calls: map[string]int64{}}
}

// Poll reads what was appended to the file since the last Poll.
func (l *Log) Poll() {
	lines, reset := l.t.read()
	if reset {
		l.entries, l.calls, l.Model = nil, map[string]int64{}, ""
		l.Gen++
		l.Rev++
	}
	for _, line := range lines {
		l.parse(line)
	}
}

func (l *Log) parse(line []byte) {
	defer func() { _ = recover() }() // why: a line that trips a parser bug costs that line
	switch l.Provider {
	case model.ProviderClaude:
		l.claude(line)
	case model.ProviderCodex:
		l.codex(line)
	default:
		l.pi(line)
	}
}

// Since is the entries added or changed after rev, oldest first.
func (l *Log) Since(rev int64) []Entry {
	var out []Entry
	for _, e := range l.entries {
		if e.rev > rev {
			out = append(out, e)
		}
	}
	return out
}

// Last is the latest n entries.
func (l *Log) Last(n int) []Entry {
	return clone(l.entries[max(0, len(l.entries)-n):])
}

// Before is the n entries before seq.
func (l *Log) Before(seq int64, n int) []Entry {
	end := 0
	for end < len(l.entries) && l.entries[end].Seq < seq {
		end++
	}
	return clone(l.entries[max(0, end-n):end])
}

func clone(es []Entry) []Entry { return append([]Entry(nil), es...) }

func (l *Log) add(e Entry, id string) {
	l.Rev++
	e.Seq, e.rev = l.next, l.Rev
	l.next++
	l.entries = append(l.entries, e)
	if n := len(l.entries); n > maxEntries {
		l.entries = l.entries[n-maxEntries:]
	}
	if id != "" {
		l.calls[id] = e.Seq
	}
}

func (l *Log) text(kind, text string) {
	if text = strings.TrimSpace(text); text != "" {
		l.add(Entry{Kind: kind, Text: cut(text, maxText)}, "")
	}
}

func (l *Log) call(id, tool, arg string) {
	l.add(Entry{Kind: EntryTool, Tool: tool, Arg: cut(arg, maxArg)}, id)
}

// result ends the call id with its output.
func (l *Log) result(id, out string, failed bool) {
	seq, ok := l.calls[id]
	delete(l.calls, id)
	if !ok || len(l.entries) == 0 || seq < l.entries[0].Seq {
		return
	}
	e := &l.entries[seq-l.entries[0].Seq]
	l.Rev++
	e.Out, e.Done, e.Failed, e.rev = cutBytes(strings.TrimSpace(out), maxOut), true, failed, l.Rev
}

// cutBytes keeps s's first n bytes, on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (l *Log) claude(line []byte) {
	var e claudeEntry
	if json.Unmarshal(line, &e) != nil || e.IsSidechain {
		return
	}
	switch e.Type {
	case "assistant":
		if m := e.Message.Model; m != "" && m != "<synthetic>" {
			l.Model = m
		}
		var blocks []claudeBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return
		}
		for _, bl := range blocks {
			switch {
			case bl.Type == "text" && e.APIError:
				l.text(EntryError, bl.Text)
			case bl.Type == "text":
				l.text(EntryAssistant, bl.Text)
			case bl.Type == "tool_use":
				l.call(bl.ID, bl.Name, arg(bl.Name, bl.Input))
			}
		}
	case "user":
		var text string
		if json.Unmarshal(e.Message.Content, &text) != nil {
			var blocks []claudeBlock
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				return
			}
			for _, bl := range blocks {
				switch bl.Type {
				case "tool_result":
					l.result(bl.ToolUseID, claudeResult(bl.Content), bl.IsError)
				case "text":
					if text == "" {
						text = bl.Text
					}
				}
			}
			if slicesHasResult(blocks) {
				return
			}
		}
		t := strings.TrimSpace(text)
		switch {
		case e.IsMeta || e.IsCompactSummary || e.Origin != nil && e.Origin.Kind != "human":
		case strings.HasPrefix(t, "[Request interrupted by user"):
			l.text(EntryError, "Interrupted")
		case strings.HasPrefix(t, "<"):
			// A slash command's echo, a ! command's output, a task notification.
		default:
			l.text(EntryUser, text)
		}
	}
}

func slicesHasResult(blocks []claudeBlock) bool {
	for _, bl := range blocks {
		if bl.Type == "tool_result" {
			return true
		}
	}
	return false
}

// claudeResult is a tool_result's content: a string or text blocks.
func claudeResult(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocksText(blocks)
}

func (l *Log) codex(line []byte) {
	var e codexEntry
	if json.Unmarshal(line, &e) != nil {
		return
	}
	p := e.Payload
	switch e.Type + "/" + p.Type {
	case "turn_context/":
		if p.Model != "" {
			l.Model = p.Model
		}
	case "response_item/message":
		switch text := codexText(p.Content); p.Role {
		case "assistant":
			l.text(EntryAssistant, text)
		case "user":
			if !injected(text) {
				l.text(EntryUser, text)
			}
		}
	case "response_item/function_call":
		args := p.Arguments
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		l.call(p.CallID, p.Name, arg(p.Name, args))
	case "response_item/custom_tool_call":
		a := firstLine(p.Input)
		if p.Name == "apply_patch" {
			a = patchFile(p.Input)
		}
		l.call(p.CallID, p.Name, a)
	case "response_item/local_shell_call":
		l.call(p.CallID, "shell", command(p.Action.Command))
	case "response_item/function_call_output", "response_item/custom_tool_call_output":
		l.result(p.CallID, codexOutput(p.Output), codexFailed(p.Output))
	case "event_msg/error":
		l.text(EntryError, p.Message)
	case "event_msg/turn_aborted":
		l.text(EntryError, "Interrupted")
	}
}

// codexOutput is a call's output as text: a string, the output field of a
// JSON string or object, or its content's text.
func codexOutput(raw json.RawMessage) string {
	var o struct {
		Output string `json:"output"`
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Unmarshal([]byte(s), &o) == nil && o.Output != "" {
			return o.Output
		}
		return s
	}
	if json.Unmarshal(raw, &o) == nil && o.Output != "" {
		return o.Output
	}
	var c struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &c) == nil && c.Content != nil {
		return codexText(c.Content)
	}
	return codexText(raw)
}

func (l *Log) pi(line []byte) {
	var e piEntry
	if json.Unmarshal(line, &e) != nil || e.Type != "message" {
		return
	}
	m := e.Message
	var text string
	var blocks []piBlock
	if json.Unmarshal(m.Content, &text) != nil {
		_ = json.Unmarshal(m.Content, &blocks)
	}
	switch m.Role {
	case "user":
		for _, bl := range blocks {
			if bl.Type == "text" && text == "" {
				text = bl.Text
			}
		}
		l.text(EntryUser, text)
	case "assistant":
		if m.Model != "" {
			l.Model = m.Model
		}
		l.text(EntryAssistant, text)
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				l.text(EntryAssistant, bl.Text)
			case "toolCall":
				l.call(bl.ID, bl.Name, arg(bl.Name, bl.Arguments))
			}
		}
		switch m.StopReason {
		case "error":
			l.text(EntryError, firstNonEmpty(m.ErrorMessage, "The turn failed"))
		case "aborted":
			l.text(EntryError, "Interrupted")
		}
	case "toolResult":
		var out []string
		for _, bl := range blocks {
			if bl.Type == "text" {
				out = append(out, bl.Text)
			}
		}
		l.result(m.ToolCallID, firstNonEmpty(text, strings.Join(out, "\n")), m.IsError)
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
