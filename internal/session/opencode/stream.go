package opencode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

type stream struct {
	id, turn, point string
	interrupted     bool
	roles           map[string]string
	parts           map[string]part
	permissions     map[string]bool
	questions       map[string][]session.Question
	usage           map[string]tokens
}

func newStream(id string) *stream {
	s := &stream{id: id}
	s.begin("")
	return s
}

func (s *stream) begin(id string) {
	s.turn, s.point, s.interrupted = id, "", false
	s.roles = map[string]string{}
	s.parts = map[string]part{}
	s.permissions = map[string]bool{}
	s.questions = map[string][]session.Question{}
	s.usage = map[string]tokens{}
}

type tokens struct {
	Input, Output, Reasoning int64
	Cache                    struct{ Read, Write int64 }
}

type apiError struct {
	Name string
	Data struct{ Message string }
}

func (e apiError) message() string {
	if e.Data.Message != "" {
		return e.Data.Message
	}
	return e.Name
}

type part struct {
	ID, SessionID, MessageID, Type, Text, CallID, Tool string
	State                                              struct {
		Status               string
		Input                json.RawMessage
		Output, Error, Title string
		Metadata             struct {
			Exit *int `json:"exit"`
		}
	}
}

// line maps the public instance /event union from SDK v2. Heartbeats and
// events for other sessions on the same workspace stream are ignored.
func (s *stream) line(b []byte, post func(session.Event)) error {
	var e struct {
		Type       string
		Properties struct {
			SessionID, ID, RequestID, MessageID, PartID, Field, Delta, Permission string
			Patterns                                                              []string
			Metadata                                                              json.RawMessage
			Error                                                                 apiError
			Status                                                                struct{ Type, Message string }
			Info                                                                  struct {
				ID, SessionID, Role, ModelID, ProviderID string
				Time                                     struct{ Completed int64 }
				Tokens                                   tokens
				Error                                    apiError
			}
			Part      part
			Questions []struct {
				Header, Question string
				Options          []struct{ Label, Description string }
			}
		}
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return fmt.Errorf("decode OpenCode event: %w", err)
	}
	p := e.Properties
	id := p.SessionID
	if e.Type == "message.updated" {
		id = p.Info.SessionID
	}
	if e.Type == "message.part.updated" {
		id = p.Part.SessionID
	}
	if id != s.id || s.turn == "" {
		return nil
	}
	switch e.Type {
	case "message.updated":
		i := p.Info
		s.roles[i.ID] = i.Role
		if i.Role != "assistant" {
			return nil
		}
		s.point = i.ID
		if i.Error.Name != "" {
			state := session.TurnFailed
			if s.interrupted || i.Error.Name == "MessageAbortedError" {
				state = session.TurnInterrupted
			}
			s.complete(state, i.Error.message(), post)
			return nil
		}
		if i.Time.Completed != 0 {
			s.usage[i.ID] = i.Tokens
			var input, output, cached int64
			for _, t := range s.usage {
				input += t.Input
				output += t.Output
				cached += t.Cache.Read
			}
			post(session.Event{Type: session.EventUsage, InputTokens: input, OutputTokens: output, CachedInputTokens: &cached})
		}
	case "message.part.updated":
		p := p.Part
		if s.roles[p.MessageID] != "assistant" {
			return nil
		}
		old := s.parts[p.ID]
		s.parts[p.ID] = p
		switch p.Type {
		case "text", "reasoning":
			if !strings.HasPrefix(p.Text, old.Text) {
				return fmt.Errorf("OpenCode replaced streamed text for %s", p.ID)
			}
			if delta := strings.TrimPrefix(p.Text, old.Text); delta != "" {
				post(textEvent(p, delta))
			}
		case "tool":
			t := session.ToolCall{CallID: p.CallID, ToolName: p.Tool, Input: p.State.Input,
				Status: p.State.Status, Output: p.State.Output, Progress: p.State.Title, ExitCode: p.State.Metadata.Exit}
			if len(t.Input) == 0 {
				t.Input = json.RawMessage(`{}`)
			}
			if t.Status == "pending" {
				t.Status = "running"
			}
			if t.Status == "error" {
				t.Status, t.Output = "failed", p.State.Error
			}
			var input struct{ Command string }
			json.Unmarshal(t.Input, &input)
			t.Command = input.Command
			post(session.Event{Type: session.EventContent, ItemID: p.ID, Kind: session.KindToolCall, Tool: &t})
		}
	case "message.part.delta":
		p0, ok := s.parts[p.PartID]
		if !ok || p.Field != "text" || p0.MessageID != p.MessageID || (p0.Type != "text" && p0.Type != "reasoning") {
			return nil
		}
		p0.Text += p.Delta
		s.parts[p.PartID] = p0
		if p.Delta != "" {
			post(textEvent(p0, p.Delta))
		}
	case "permission.asked":
		if s.permissions[p.ID] {
			return nil
		}
		s.permissions[p.ID] = true
		kind := session.RequestDynamicTool
		switch p.Permission {
		case "bash":
			kind = session.RequestCommand
		case "edit":
			kind = session.RequestFileChange
		case "read":
			kind = session.RequestFileRead
		}
		var meta struct{ Command, FilePath, Diff string }
		json.Unmarshal(p.Metadata, &meta)
		post(session.Event{Type: session.EventRequestOpened, RequestID: p.ID, RequestType: kind,
			Detail: strings.Join(p.Patterns, ", "), Request: &session.ToolRequest{ToolName: p.Permission,
				Input: p.Metadata, Command: meta.Command, FilePath: meta.FilePath, Diff: meta.Diff}})
	case "permission.replied":
		if s.permissions[p.RequestID] {
			delete(s.permissions, p.RequestID)
			post(session.Event{Type: session.EventRequestResolved, RequestID: p.RequestID})
		}
	case "question.asked":
		if _, exists := s.questions[p.ID]; exists {
			return nil
		}
		questions := make([]session.Question, len(p.Questions))
		for i, q := range p.Questions {
			questions[i] = session.Question{ID: strconv.Itoa(i), Header: q.Header, Prompt: q.Question, Options: []session.PromptOption{}}
			for _, o := range q.Options {
				questions[i].Options = append(questions[i].Options, session.PromptOption{ID: o.Label, Label: o.Label, Description: o.Description})
			}
		}
		s.questions[p.ID] = questions
		post(session.Event{Type: session.EventInputRequested, RequestID: p.ID, Questions: questions})
	case "question.replied", "question.rejected":
		if _, exists := s.questions[p.RequestID]; exists {
			delete(s.questions, p.RequestID)
			post(session.Event{Type: session.EventInputResolved, RequestID: p.RequestID})
		}
	case "session.status":
		if p.Status.Type == "idle" {
			s.complete(session.TurnCompleted, "", post)
		}
	case "session.error":
		state := session.TurnFailed
		if s.interrupted || p.Error.Name == "MessageAbortedError" {
			state = session.TurnInterrupted
		}
		s.complete(state, p.Error.message(), post)
	}
	return nil
}

func textEvent(p part, delta string) session.Event {
	kind := session.KindAssistantText
	if p.Type == "reasoning" {
		kind = session.KindReasoningText
	}
	return session.Event{Type: session.EventContent, ItemID: p.ID, Kind: kind, Text: delta}
}

func (s *stream) complete(state, message string, post func(session.Event)) {
	if s.turn == "" {
		return
	}
	if s.interrupted {
		state = session.TurnInterrupted
	}
	// Resolve in a deterministic order so captured transcripts stay stable.
	ids := make([]string, 0, len(s.permissions))
	for id := range s.permissions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		post(session.Event{Type: session.EventRequestResolved, RequestID: id})
	}
	ids = ids[:0]
	for id := range s.questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		post(session.Event{Type: session.EventInputResolved, RequestID: id})
	}
	post(session.Event{Type: session.EventTurnCompleted, TurnID: s.turn, State: state, ErrorMessage: message, Point: s.point})
	s.begin("")
}
