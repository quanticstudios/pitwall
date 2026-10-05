package flow

import (
	"encoding/json"
	"time"
)

// pi reads a pi session: entries {type, timestamp, ...}; type "message"
// carries a message whose role is user, assistant or toolResult. An
// assistant's content has text and toolCall {id, name, arguments} blocks; a
// toolResult names its toolCallId. pi has no plan and no subagents.
type pi struct{}

type piEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
		ToolCallID string          `json:"toolCallId"`
		IsError    bool            `json:"isError"`
		Timestamp  int64           `json:"timestamp"`
	} `json:"message"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (pi) line(b *builder, line []byte) {
	var e piEntry
	if json.Unmarshal(line, &e) != nil || e.Type != "message" {
		return
	}
	m := e.Message
	ts := parseTime(e.Timestamp)
	if ts.IsZero() && m.Timestamp > 0 {
		ts = time.UnixMilli(m.Timestamp)
	}
	var blocks []piBlock
	var text string
	if json.Unmarshal(m.Content, &text) != nil && json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	switch m.Role {
	case "user":
		for _, bl := range blocks {
			if bl.Type == "text" && text == "" {
				text = bl.Text
			}
		}
		if text != "" {
			b.prompt(ts, text)
		}
	case "assistant":
		b.reply(ts, text)
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				b.reply(ts, bl.Text)
			case "toolCall":
				b.call(ts, bl.ID, bl.Name, arg(bl.Name, bl.Arguments))
			}
		}
		b.active(ts)
		switch m.StopReason {
		case "error", "aborted":
			b.end(ts, true)
		case "toolUse":
		default:
			if !b.pending() {
				b.end(ts, false)
			}
		}
	case "toolResult":
		b.active(ts)
		b.result(m.ToolCallID, m.IsError)
	}
}

func (pi) resolve(*builder) {}
