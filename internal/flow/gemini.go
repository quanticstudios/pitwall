package flow

import (
	"encoding/json"
	"strings"
)

// gemini reads a Gemini CLI chat, chats/session-*.jsonl under its project's
// tmp directory: a first line of session metadata, then one line per
// message. A message that changes, as its tool calls run and its tokens
// come in, is appended again whole under the same id, so each id's latest
// copy is what it says. Lines keyed "$set", "$patch" and "$rewindTo" edit
// the metadata or rewind; they are skipped. A user message's content is a
// list of parts; a gemini message's is its text, beside toolCalls {id,
// name, args, status} and tokens. Its write_todos tool sets the plan.
// Gemini runs no subagents pitwall follows.
type gemini struct {
	seen   map[string]bool // message ids already read: prompts and tokens count once
	called map[string]bool // tool call ids already added
}

type geminiMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	ToolCalls []struct {
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Args   json.RawMessage `json:"args"`
		Status string          `json:"status"`
	} `json:"toolCalls"`
	Tokens *geminiTokens `json:"tokens"`
}

// geminiTokens are a call's tokens as Gemini counts them: input includes
// the cached part, and thoughts bill as output.
type geminiTokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
}

func (t geminiTokens) tokens() Tokens {
	return Tokens{Input: max(0, t.Input-t.Cached) + t.Tool, Output: t.Output + t.Thoughts, CacheRead: t.Cached}
}

func (g *gemini) line(b *builder, line []byte) {
	var m geminiMessage
	if json.Unmarshal(line, &m) != nil || m.ID == "" {
		return
	}
	if g.seen == nil {
		g.seen, g.called = map[string]bool{}, map[string]bool{}
	}
	ts := parseTime(m.Timestamp)
	first := !g.seen[m.ID]
	switch m.Type {
	case "user":
		var parts []struct {
			Text string `json:"text"`
		}
		var text string
		if json.Unmarshal(m.Content, &text) != nil && json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				text += p.Text
			}
		}
		// why: a slash command is no prompt; it stays in the turn it was typed in.
		if first && text != "" && !strings.HasPrefix(strings.TrimSpace(text), "/") {
			b.prompt(ts, text)
		}
	case "gemini":
		if t := m.Tokens; t != nil && m.Model != "" && !g.seen[m.ID+"\x00tokens"] {
			g.seen[m.ID+"\x00tokens"] = true
			b.use(ts, m.Model, t.tokens(), t.Input+t.Tool, 0)
		}
		var text string
		_ = json.Unmarshal(m.Content, &text)
		b.reply(ts, text)
		for _, c := range m.ToolCalls {
			if !g.called[c.ID] {
				g.called[c.ID] = true
				b.call(ts, c.ID, c.Name, arg(c.Name, c.Args))
				if c.Name == "write_todos" {
					b.setPlan(geminiTodos(c.Args))
				}
			}
			switch c.Status {
			case "success":
				b.result(c.ID, false)
			case "error", "cancelled":
				b.result(c.ID, true)
			}
		}
		b.active(ts)
		if len(m.ToolCalls) == 0 && !b.pending() {
			b.end(ts, false)
		}
	case "error":
		b.end(ts, true)
	}
	g.seen[m.ID] = true
}

// geminiTodos is the plan a write_todos call sets: {todos: [{description,
// status}]}, status pending, in_progress, completed, cancelled or blocked.
func geminiTodos(args json.RawMessage) []Step {
	var in struct {
		Todos []struct {
			Description string `json:"description"`
			Status      string `json:"status"`
		} `json:"todos"`
	}
	_ = json.Unmarshal(args, &in)
	var steps []Step
	for _, t := range in.Todos {
		s := stepState(t.Status)
		if t.Status == "cancelled" {
			s = StepDone
		}
		steps = append(steps, Step{Text: cut(t.Description, 200), State: s})
	}
	return steps
}

func (*gemini) resolve(*builder) {}
