package claude

import (
	"cmp"
	"encoding/json"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Transcript turns the lines of Claude Code's own transcript file into chat
// events, for a chat that mirrors a Claude Code TUI running in a pane. The
// assistant and tool lines have stream-json's shapes, so the stream reads
// them; Transcript adds what only stream-json's result and control lines
// say there: where turns start and end, and what Claude asks.
type Transcript struct {
	s     *stream
	turn  string // the running turn's id, its prompt line's uuid; "" while idle
	model string
	last  string // the latest tool call's id
	asked string // the open request's id, "" for none
}

func NewTranscript() *Transcript { return &Transcript{s: newStream("")} }

// transcriptLine is the fields of a transcript line Transcript reads on
// top of the stream's.
type transcriptLine struct {
	Type             string `json:"type"`
	Subtype          string `json:"subtype"`
	UUID             string `json:"uuid"`
	Timestamp        string `json:"timestamp"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Origin           *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Line is one transcript line's events, stamped with the line's time.
func (t *Transcript) Line(b []byte) []session.Event {
	var l transcriptLine
	if json.Unmarshal(b, &l) != nil || l.IsSidechain {
		return nil
	}
	var out []session.Event
	switch l.Type {
	case "user":
		if text, ok := promptText(l); ok {
			out = t.prompt(l, text)
			break
		}
		out = t.s.line(b)
	case "assistant":
		if m := l.Message.Model; m != "" && m != "<synthetic>" && m != t.model {
			t.model = m
			out = append(out, session.Event{Type: session.EventSettings, Model: m})
		}
		out = append(out, t.s.line(b)...)
	case "system":
		if l.Subtype != "turn_duration" {
			return nil
		}
		out = t.End(session.TurnCompleted) // Claude Code's mark at the end of each turn
	default:
		return nil // summaries, snapshots, attachments, system notes: nothing a chat shows
	}
	for i := range out {
		if e := out[i]; e.Type == session.EventContent && e.Tool != nil && e.Tool.Status == "running" {
			t.last = e.ItemID
		}
		out[i].CreatedAt = stamp(l.Timestamp)
	}
	return out
}

// prompt is a prompt line: the end of the running turn, then the prompt and
// the turn it starts. An interrupt's marker ends the turn alone.
func (t *Transcript) prompt(l transcriptLine, text string) []session.Event {
	if strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user") {
		return t.End(session.TurnInterrupted)
	}
	out := t.End(session.TurnCompleted)
	clear(t.s.blocks)
	clear(t.s.tools)
	t.turn = cmp.Or(l.UUID, "prompt:"+l.Timestamp)
	return append(out,
		session.Event{Type: session.EventContent, ItemID: t.turn, Kind: session.KindUserPrompt, Text: text},
		session.Event{Type: session.EventTurnStarted, TurnID: t.turn})
}

// promptText is the text of a user line that is something the user said,
// false for a tool's result, a slash command's echo, a ! command's output,
// a harness note or a compaction summary, as flow reads them.
func promptText(l transcriptLine) (string, bool) {
	if l.IsMeta || l.IsCompactSummary || l.Origin != nil && l.Origin.Kind != "human" {
		return "", false
	}
	var text string
	if json.Unmarshal(l.Message.Content, &text) != nil {
		var blocks []block
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return "", false
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<") {
		return "", false
	}
	return text, true
}

// Running reports whether a turn runs.
func (t *Transcript) Running() bool { return t.turn != "" }

// End completes the running turn as state, a session turn state; nothing
// while idle. It withdraws an open request first.
func (t *Transcript) End(state string) []session.Event {
	out := t.Resolve()
	if t.turn == "" {
		return out
	}
	out = append(out, session.Event{Type: session.EventTurnCompleted, TurnID: t.turn, State: state})
	t.turn = ""
	return out
}

// Ask opens request id: Claude Code waits on a permission prompt, a plan
// or a question for its latest tool call that has not finished. Without
// such a call it opens a permission request that says only detail.
func (t *Transcript) Ask(id, detail string) []session.Event {
	if id == t.asked {
		return nil
	}
	out := t.Resolve()
	t.asked = id
	call, ok := t.s.tools[t.last]
	if !ok || call.Status != "running" {
		return append(out, session.Event{Type: session.EventRequestOpened, RequestID: id, RequestType: session.RequestUnknown, Detail: detail})
	}
	var l line
	l.RequestID, l.Request.Subtype, l.Request.ToolName, l.Request.Input = id, "can_use_tool", call.ToolName, call.Input
	return append(out, t.s.canUseTool(l)...)
}

// Asked is the open request's id, "" for none.
func (t *Transcript) Asked() string { return t.asked }

// Resolve withdraws the open request, if any.
func (t *Transcript) Resolve() []session.Event {
	if t.asked == "" {
		return nil
	}
	id := t.asked
	t.asked = ""
	if out := t.s.withdraw(id); out != nil {
		return out
	}
	return []session.Event{{Type: session.EventRequestResolved, RequestID: id}}
}

// stamp is a transcript timestamp as session.Stamp writes it, now when
// it has none.
func stamp(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		at = time.Now()
	}
	return session.Stamp(at)
}

// Mark is what a transcript line is to a reader paging through it.
type Mark uint8

const (
	MarkNone    Mark = iota // a line no chat shows
	MarkItem                // a line a chat shows as an item or part of one
	MarkPrompt              // a prompt line, where a turn starts
	MarkCompact             // a compaction's boundary
)

// MarkOf is line b's Mark.
func MarkOf(b []byte) Mark {
	var l transcriptLine
	if json.Unmarshal(b, &l) != nil || l.IsSidechain || l.IsCompactSummary {
		return MarkNone
	}
	switch l.Type {
	case "user":
		if text, ok := promptText(l); ok && !strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user") {
			return MarkPrompt
		}
		return MarkItem
	case "assistant":
		return MarkItem
	case "system":
		if l.Subtype == "compact_boundary" {
			return MarkCompact
		}
	}
	return MarkNone
}
