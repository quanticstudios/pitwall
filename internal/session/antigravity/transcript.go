// Package antigravity reads the transcript Google's Antigravity CLI (agy)
// writes for a conversation, for a chat that mirrors agy's TUI in a pane.
package antigravity

import (
	"cmp"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// Transcript turns the lines of agy's
// brain/<conversation>/.system_generated/logs/transcript_full.jsonl into
// chat events. Each line is one step, written once it is done: the user's
// input, a model response with its text and tool calls, then one GENERIC
// step per call with what the call returned, in the order of the calls.
// A response with no tool call ends the turn. A call shows once its result
// is in, as agy writes nothing while it runs.
type Transcript struct {
	turn    string     // the running turn's id; "" while idle
	asked   string     // the open request's id, "" for none
	model   string     // the model of the last response
	pending []*pending // the calls whose results have not come, oldest first
}

// pending is a call a response made, waiting for its GENERIC step.
type pending struct {
	call session.ToolCall
	at   string
}

func NewTranscript() *Transcript { return &Transcript{} }

// step is the fields of a transcript line the reader uses.
type step struct {
	Index     int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Model     string `json:"model"`
	Content   string `json:"content"`
	Thinking  string `json:"thinking"`
	Error     string `json:"error"`
	ToolCalls []struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

var (
	// request is the user's own text in a USER_INPUT step, which agy wraps
	// with metadata for the model.
	request = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)
	// stamps are the lines agy starts each tool result with.
	stamps = regexp.MustCompile(`(?m)^(?:Created|Completed) At: .*\n?`)
	// exited is how a command's result gives its exit code.
	exited = regexp.MustCompile(`^Command exited with code (-?\d+)\.\s*(?:Command output:\n)?`)
)

// Line is one transcript line's events, stamped with the step's time.
func (t *Transcript) Line(b []byte) []session.Event {
	var s step
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	id := "step-" + strconv.Itoa(s.Index)
	var out []session.Event
	switch s.Type {
	case "USER_INPUT":
		text := s.Content
		if m := request.FindStringSubmatch(text); m != nil {
			text = m[1]
		}
		if text = strings.TrimSpace(text); text == "" {
			return nil
		}
		out = t.End(session.TurnCompleted)
		t.turn = id
		out = append(out, session.Event{Type: session.EventTurnStarted, TurnID: id},
			session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindUserPrompt, Text: text})
	case "PLANNER_RESPONSE":
		if s.Model != "" && s.Model != t.model {
			t.model = s.Model
			out = append(out, session.Event{Type: session.EventSettings, Model: s.Model})
		}
		if th := strings.TrimSpace(s.Thinking); th != "" {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id + "-thinking", Kind: session.KindReasoningText, Model: t.model, Text: th})
		}
		if strings.TrimSpace(s.Content) != "" {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindAssistantText, Model: t.model, Text: s.Content})
		}
		for i, c := range s.ToolCalls {
			t.pending = append(t.pending, &pending{call: toolCall(id+"#"+strconv.Itoa(i), c.Name, c.Args)})
		}
		if len(s.ToolCalls) == 0 && s.Status == "DONE" {
			out = append(out, t.End(session.TurnCompleted)...)
		}
	case "GENERIC":
		if len(t.pending) == 0 {
			return nil
		}
		p := t.pending[0]
		t.pending = t.pending[1:]
		c := p.call
		c.Status = "completed"
		text := strings.TrimSpace(stamps.ReplaceAllString(s.Content, ""))
		if m := exited.FindStringSubmatch(text); m != nil {
			code, _ := strconv.Atoi(m[1])
			c.ExitCode, text = &code, strings.TrimSpace(text[len(m[0]):])
			if code != 0 {
				c.Status = "failed"
			}
		}
		if s.Status == "ERROR" || s.Error != "" {
			c.Status, text = "failed", cmp.Or(strings.TrimSpace(s.Error), text)
		}
		c.Output = strings.ReplaceAll(text, "\r\n", "\n")
		out = append(out, session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, Tool: &c})
	case "CHECKPOINT":
		out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindSystem, Phase: session.PhaseCompact, Text: "Conversation compacted"})
	}
	at := stamp(s.CreatedAt)
	for i := range out {
		if out[i].CreatedAt == "" {
			out[i].CreatedAt = at
		}
	}
	return out
}

// toolCall is a call as the chat's tool rows read it: agy's shell, file
// read, edit and write tools under Claude Code's names, so they draw as a
// command, a file and a diff; any other tool under its own name with its
// arguments.
func toolCall(id, name string, args map[string]json.RawMessage) session.ToolCall {
	arg := func(k string) string {
		var s string
		_ = json.Unmarshal(args[k], &s)
		return s
	}
	c := session.ToolCall{CallID: id, ToolName: name}
	var in any = args
	switch name {
	case "run_command":
		c.ToolName, c.Command = "Bash", arg("CommandLine")
		in = map[string]string{"command": c.Command, "description": arg("toolSummary")}
	case "read_file", "view_file":
		c.ToolName = "Read"
		in = map[string]string{"file_path": cmp.Or(arg("Path"), arg("AbsolutePath"))}
	case "edit_file":
		c.ToolName = "Edit"
		in = map[string]string{"file_path": arg("Path"), "old_string": arg("Target"), "new_string": arg("Replacement")}
	case "write_file":
		c.ToolName = "Write"
		in = map[string]string{"file_path": arg("Path"), "content": arg("Content")}
	}
	c.Input, _ = json.Marshal(in)
	if string(c.Input) == "null" {
		c.Input = json.RawMessage("{}")
	}
	return c
}

// Running reports whether a turn runs.
func (t *Transcript) Running() bool { return t.turn != "" }

// Settings are the model the session last ran with and no effort, which
// the transcript does not record.
func (t *Transcript) Settings() (model, effort string) { return t.model, "" }

// End completes the running turn as state, with the calls still waiting
// for a result shown as they were asked; nothing while idle. It withdraws
// an open request first.
func (t *Transcript) End(state string) []session.Event {
	out := t.Resolve()
	if t.turn == "" {
		return out
	}
	for _, p := range t.pending {
		c := p.call
		c.Status = "failed"
		out = append(out, session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, Tool: &c})
	}
	t.pending = nil
	out = append(out, session.Event{Type: session.EventTurnCompleted, TurnID: t.turn, State: state, Point: t.turn})
	t.turn = ""
	return out
}

// Ask opens request id, a permission prompt agy's TUI shows. detail is
// what agy's terminal report says, as "Approve Bash: rm -rf build"; a
// Bash one is a command approval.
func (t *Transcript) Ask(id, detail string) []session.Event {
	if id == t.asked {
		return nil
	}
	out := t.Resolve()
	t.asked = id
	e := session.Event{Type: session.EventRequestOpened, RequestID: id, RequestType: session.RequestUnknown, Detail: detail}
	if tool, target, ok := strings.Cut(strings.TrimPrefix(detail, "Approve "), ": "); ok && tool == "Bash" {
		e.RequestType = session.RequestCommand
		e.Request = &session.ToolRequest{ToolName: tool, Command: target}
		e.Request.Input, _ = json.Marshal(map[string]string{"command": target})
	}
	return append(out, e)
}

// Resolve withdraws the open request, if any.
func (t *Transcript) Resolve() []session.Event {
	if t.asked == "" {
		return nil
	}
	id := t.asked
	t.asked = ""
	return []session.Event{{Type: session.EventRequestResolved, RequestID: id}}
}

// Subagents is nil: agy's subagents keep transcripts of their own.
func (t *Transcript) Subagents() map[string]bool { return nil }

// Subagent shows nothing (Subagents).
func (t *Transcript) Subagent(string, []byte) []session.Event { return nil }

// MarkOf is transcript line b's mark for a reader paging through it: a
// turn starts at the user's input.
func MarkOf(b []byte) claude.Mark {
	var s struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &s) != nil {
		return claude.MarkNone
	}
	switch s.Type {
	case "USER_INPUT":
		return claude.MarkPrompt
	case "CHECKPOINT":
		return claude.MarkCompact
	case "PLANNER_RESPONSE", "GENERIC":
		return claude.MarkItem
	}
	return claude.MarkNone
}

// now is the clock stamp uses for a line without a time; tests set it.
var now = time.Now

// stamp is a step's created_at as session.Stamp writes it, now when it
// has none.
func stamp(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		at = now()
	}
	return session.Stamp(at)
}
