package acp

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

type stream struct {
	id, turn, prompt string
	images           bool
	cancelled        bool
	chunk            string
	serial           int
	tools            map[string]*session.ToolCall
	kinds            map[string]string
	permissions      map[string]permission
	unknown          map[string]bool
}

func newStream() *stream {
	return &stream{tools: map[string]*session.ToolCall{}, kinds: map[string]string{}, permissions: map[string]permission{}, unknown: map[string]bool{}}
}

type permission struct {
	ID        json.RawMessage
	SessionID string     `json:"sessionId"`
	ToolCall  toolUpdate `json:"toolCall"`
	Options   []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

type toolUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Content    *[]struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Path    string  `json:"path"`
		OldText *string `json:"oldText"`
		NewText string  `json:"newText"`
	} `json:"content"`
}

// line runs with d.mu held. JSON-RPC ids are kept as JSON, preserving both
// numeric and string ids when replying to agent requests.
func (d *driver) line(b []byte) []session.Event {
	var m rpcMessage
	if json.Unmarshal(b, &m) != nil || m.JSONRPC != "2.0" {
		d.s.unsupported("invalid JSON-RPC envelope")
		return nil
	}
	id := string(m.ID)
	if m.Method == "" {
		if ch := d.pending[id]; ch != nil {
			delete(d.pending, id)
			ch <- m
			return nil
		}
		if id != d.s.prompt || d.s.turn == "" {
			return nil
		}
		var r struct {
			StopReason string `json:"stopReason"`
		}
		state, detail := session.TurnCompleted, ""
		if m.Error != nil {
			state, detail = session.TurnFailed, m.Error.Message
		} else if json.Unmarshal(m.Result, &r) != nil {
			state, detail = session.TurnFailed, "Invalid ACP prompt response"
		} else {
			switch r.StopReason {
			case "cancelled":
				state = session.TurnInterrupted
			case "refusal":
				state, detail = session.TurnFailed, "ACP agent refused the prompt; it will not be included in the next turn"
			case "end_turn", "max_tokens", "max_turn_requests":
			default:
				state, detail = session.TurnFailed, "Unsupported ACP stop reason: "+r.StopReason
			}
		}
		d.cancelPermissions()
		e := session.Event{Type: session.EventTurnCompleted, TurnID: d.s.turn, State: state, ErrorMessage: detail}
		d.s.turn, d.s.prompt = "", ""
		if r.StopReason == "max_tokens" || r.StopReason == "max_turn_requests" {
			return []session.Event{{Type: session.EventContent, TurnID: e.TurnID, ItemID: e.TurnID + "-stop", Kind: session.KindSystem, Text: "ACP stopped: " + r.StopReason}, e}
		}
		return []session.Event{e}
	}
	if m.Method == "session/update" && len(m.ID) == 0 {
		var p struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if json.Unmarshal(m.Params, &p) != nil || p.SessionID != d.s.id || d.s.turn == "" {
			return nil
		}
		return d.s.update(p.Update)
	}
	if len(m.ID) > 0 {
		if m.Method == "session/request_permission" {
			var p permission
			if json.Unmarshal(m.Params, &p) != nil || p.SessionID != d.s.id || d.s.turn == "" || p.ToolCall.ToolCallID == "" {
				d.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": rpcError{Code: -32602, Message: "No active turn or invalid permission parameters"}})
				return nil
			}
			if d.s.cancelled {
				d.reply(m.ID, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
				return nil
			}
			p.ID = m.ID
			key := "acp-permission-" + id
			d.s.permissions[key] = p
			e := d.s.tool(p.ToolCall)
			t := e.Tool
			rt := session.RequestDynamicTool
			switch d.s.kinds[p.ToolCall.ToolCallID] {
			case "execute":
				rt = session.RequestCommand
			case "edit", "delete", "move":
				rt = session.RequestFileChange
			case "read":
				rt = session.RequestFileRead
			}
			return []session.Event{e, {Type: session.EventRequestOpened, TurnID: d.s.turn, RequestID: key, RequestType: rt, Detail: t.Progress, Request: &session.ToolRequest{ToolName: t.ToolName, Input: t.Input}}}
		}
		d.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": rpcError{Code: -32601, Message: "Unsupported client method: " + m.Method}})
	}
	d.s.unsupported(m.Method)
	return nil
}

func (s *stream) unsupported(kind string) {
	if !s.unknown[kind] {
		s.unknown[kind] = true
		log.Printf("ACP: unsupported message %q", kind)
	}
}

func (s *stream) update(raw json.RawMessage) []session.Event {
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		MessageID     string          `json:"messageId"`
		Content       json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &u) != nil {
		s.unsupported("invalid session/update")
		return nil
	}
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &content) != nil {
			s.unsupported("invalid content")
			return nil
		}
		if content.Type != "text" {
			s.unsupported("content/" + content.Type)
			return nil
		}
		if s.chunk != u.SessionUpdate {
			s.serial++
			s.chunk = u.SessionUpdate
		}
		kind := session.KindAssistantText
		if u.SessionUpdate == "agent_thought_chunk" {
			kind = session.KindReasoningText
		}
		item := fmt.Sprintf("%s-text-%d", s.turn, s.serial)
		if u.MessageID != "" {
			item = s.turn + "-" + u.SessionUpdate + "-" + u.MessageID
		}
		return []session.Event{{Type: session.EventContent, TurnID: s.turn, ItemID: item, Kind: kind, Text: content.Text}}
	case "tool_call", "tool_call_update":
		var t toolUpdate
		if json.Unmarshal(raw, &t) != nil || t.ToolCallID == "" {
			s.unsupported("invalid tool call")
			return nil
		}
		s.chunk = ""
		return []session.Event{s.tool(t)}
	case "user_message_chunk": // The chat loop already records the prompt.
		return nil
	case "plan":
		// Plans have content/priority/status, rather than session.Task's subject.
		var p struct {
			Entries []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil
		}
		tasks := make([]session.Task, 0, len(p.Entries))
		for _, e := range p.Entries {
			tasks = append(tasks, session.Task{Subject: e.Content, Status: e.Status})
		}
		return []session.Event{{Type: session.EventContent, TurnID: s.turn, ItemID: s.turn + "-plan", Kind: session.KindToolCall, Tool: &session.ToolCall{CallID: s.turn + "-plan", ToolName: "Plan", Status: "running", Tasks: tasks}}}
	default:
		s.unsupported("session/update/" + u.SessionUpdate)
		return nil
	}
}

// tool merges ACP's partial updates and emits a fresh complete value. Values
// already passed to emit must never change when a later update arrives.
func (s *stream) tool(u toolUpdate) session.Event {
	s.chunk = ""
	if u.Kind != "" {
		s.kinds[u.ToolCallID] = u.Kind
	}
	t := session.ToolCall{CallID: u.ToolCallID, ToolName: "Tool", Status: "running"}
	if old := s.tools[u.ToolCallID]; old != nil {
		t = *old
	}
	if u.Title != "" {
		t.Progress = u.Title
		if t.ToolName == "Tool" {
			t.ToolName = u.Title
		}
	}
	if u.Name != "" {
		t.ToolName = u.Name
	}
	if len(u.RawInput) > 0 {
		t.Input = u.RawInput
	}
	switch u.Status {
	case "pending", "in_progress":
		t.Status = "running"
	case "completed", "failed":
		t.Status = u.Status
	}
	if len(u.RawOutput) > 0 {
		t.Output = string(u.RawOutput)
	}
	if u.Content != nil {
		var output, diffs []string
		t.Files = nil
		for _, c := range *u.Content {
			switch c.Type {
			case "content":
				if c.Content.Type == "text" {
					output = append(output, c.Content.Text)
				} else {
					s.unsupported("tool content/" + c.Content.Type)
				}
			case "diff":
				old := ""
				if c.OldText != nil {
					old = *c.OldText
				}
				diffs = append(diffs, fmt.Sprintf("%s\nOld text:\n%s\nNew text:\n%s", c.Path, old, c.NewText))
				t.Files = append(t.Files, session.ToolFile{Path: c.Path, Status: "modified"})
			default:
				s.unsupported("tool content/" + c.Type)
			}
		}
		t.Output = strings.Join(output, "\n")
		t.Diff = strings.Join(diffs, "\n")
	}
	s.tools[u.ToolCallID] = &t
	return session.Event{Type: session.EventContent, TurnID: s.turn, ItemID: "acp-tool-" + u.ToolCallID, Kind: session.KindToolCall, Tool: &t}
}
