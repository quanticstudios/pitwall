package claude

import (
	"encoding/json"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// stream is what the driver knows between stdout lines: the turn that
// runs, and what that turn printed so far.
type stream struct {
	resume  string // the session id the chat loop knows
	version string // claude_code_version, once system/init gave it
	turn    string // the running turn's id, "" while idle
	point   string // the uuid of the turn's last assistant line

	streamed map[string]int              // text item id: bytes stream_event deltas gave it
	blocks   map[string]int              // message id: content blocks its assistant lines gave so far
	tools    map[string]session.ToolCall // tool_use id: the call so far

	contextModel  string
	contextTokens *int64

	pending map[string]ask // can_use_tool request id: what Claude asks, until answered or withdrawn

	logged map[string]bool // types and subtypes skipped, logged once per process
	reply  any             // a control_response line line wants written
}

// ask is an open can_use_tool request, what its answer needs.
type ask struct {
	tool        string
	input       json.RawMessage
	suggestions []json.RawMessage // permission_suggestions
}

func newStream(resume string) *stream {
	return &stream{resume: resume, logged: map[string]bool{}, streamed: map[string]int{},
		blocks: map[string]int{}, tools: map[string]session.ToolCall{}, pending: map[string]ask{}}
}

// line is one stdout line, the fields of every type the driver reads.
type line struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	UUID      string `json:"uuid"`
	SessionID string `json:"session_id"`

	// system/init
	Model        string   `json:"model"`
	Version      string   `json:"claude_code_version"`
	Capabilities []string `json:"capabilities"`

	// system/permission_denied: a text; assistant and user: an apiMessage
	Message   json.RawMessage `json:"message"`
	ToolUseID string          `json:"tool_use_id"`

	// stream_event
	Event        *streamEvent `json:"event"`
	APIMessageID string       `json:"api_message_id"`

	// result
	IsError        bool                  `json:"is_error"`
	Result         string                `json:"result"`
	Errors         []string              `json:"errors"`
	TerminalReason string                `json:"terminal_reason"`
	TotalCostUSD   *float64              `json:"total_cost_usd"`
	ModelUsage     map[string]modelUsage `json:"modelUsage"`

	RateLimitInfo *rateLimitInfo `json:"rate_limit_info"`

	// control_request, control_cancel_request
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype     string            `json:"subtype"`
		ToolName    string            `json:"tool_name"`
		Input       json.RawMessage   `json:"input"`
		Suggestions []json.RawMessage `json:"permission_suggestions"`
	} `json:"request"`
}

type streamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

type apiMessage struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // a string in user lines the CLI echoes
	Usage   *struct {
		Input         int64 `json:"input_tokens"`
		Output        int64 `json:"output_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result: a string or text blocks
	IsError   bool            `json:"is_error"`
}

type modelUsage struct {
	InputTokens              int64    `json:"inputTokens"`
	OutputTokens             int64    `json:"outputTokens"`
	CacheReadInputTokens     int64    `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64    `json:"cacheCreationInputTokens"`
	CostUSD                  *float64 `json:"costUSD"`
	ContextWindow            int64    `json:"contextWindow"`
}

// rateLimitInfo is SDKRateLimitInfo from the Agent SDK's sdk.d.ts.
type rateLimitInfo struct {
	Status         string `json:"status"`
	ResetsAt       int64  `json:"resetsAt"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

// line maps one stdout line to events, without CreatedAt. A line it
// cannot decode, or of a type it does not read, gives none.
func (s *stream) line(b []byte) []session.Event {
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		s.skip("undecodable line")
		return nil
	}
	switch l.Type {
	case "system":
		return s.system(l)
	case "stream_event":
		return s.streamEvent(l)
	case "assistant":
		return s.assistant(l)
	case "user":
		return s.user(l)
	case "result":
		return s.result(l)
	case "rate_limit_event":
		return s.limit(l)
	case "control_request":
		if l.Request.Subtype == "can_use_tool" {
			return s.canUseTool(l)
		}
		s.reply = map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": l.RequestID, "error": "pitwall does not handle " + l.Request.Subtype}}
		return nil
	case "control_cancel_request":
		return s.withdraw(l.RequestID)
	case "control_response":
		// shortcut: the driver's own requests are not matched to their
		// answers, so a refused set_model goes unreported; match them by
		// request_id if a caller needs ProviderError.
		return nil
	}
	s.skip(l.Type)
	return nil
}

// skip logs a line type the driver does not read, once per process.
func (s *stream) skip(what string) {
	if !s.logged[what] {
		s.logged[what] = true
		log.Printf("claude: skipped %s", what)
	}
}

func (s *stream) system(l line) []session.Event {
	switch l.Subtype {
	case "init":
		var out []session.Event
		if l.SessionID != "" && (l.SessionID != s.resume || s.version == "") {
			if s.version == "" {
				log.Printf("claude: Claude Code %s", l.Version)
			}
			s.resume, s.version = l.SessionID, l.Version
			out = append(out, session.Event{Type: session.EventResumeID, ResumeID: l.SessionID,
				Version: l.Version, Capabilities: l.Capabilities})
		}
		if l.Model != "" {
			s.contextModel = l.Model
		}
		return out
	case "permission_denied":
		var text string
		json.Unmarshal(l.Message, &text)
		id := l.UUID
		if id == "" {
			id = l.ToolUseID + ":denied"
		}
		return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindSystem, Text: text}}
	case "status", "task_started", "task_progress", "task_notification", "thinking_tokens",
		"hook_started", "hook_progress", "hook_response":
		// status's permissionMode echoes a set_permission_mode, which the
		// chat loop patched when it called Set.
		return nil
	}
	s.skip("system/" + l.Subtype)
	return nil
}

// streamEvent is a partial message's text or thinking as it streams. Its
// item id is the one the finished assistant line's block gets.
func (s *stream) streamEvent(l line) []session.Event {
	if l.Event == nil || l.Event.Type != "content_block_delta" {
		return nil
	}
	id := l.APIMessageID + ":" + strconv.Itoa(l.Event.Index)
	d := l.Event.Delta
	switch {
	case d.Type == "text_delta" && d.Text != "":
		s.streamed[id] += len(d.Text)
		return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindAssistantText, Model: s.contextModel, Text: d.Text}}
	case d.Type == "thinking_delta" && d.Thinking != "":
		s.streamed[id] += len(d.Thinking)
		return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindReasoningText, Model: s.contextModel, Text: d.Thinking}}
	}
	return nil
}

// assistant is one finished content block: the text the stream did not
// give, or a tool call starting. The CLI prints one line per block, so
// the block's index is how many lines its message had before.
func (s *stream) assistant(l line) []session.Event {
	var m apiMessage
	var blocks []block
	if json.Unmarshal(l.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		s.skip("undecodable assistant line")
		return nil
	}
	if m.Model != "" {
		s.contextModel = m.Model
	}
	if u := m.Usage; u != nil {
		t := u.Input + u.Output + u.CacheRead + u.CacheCreation
		s.contextTokens = &t
	}
	if l.UUID != "" {
		s.point = l.UUID
	}
	var out []session.Event
	for _, b := range blocks {
		id := m.ID + ":" + strconv.Itoa(s.blocks[m.ID])
		s.blocks[m.ID]++
		kind, text := session.KindAssistantText, b.Text
		switch b.Type {
		case "thinking":
			kind, text = session.KindReasoningText, b.Thinking
		case "tool_use":
			t := session.ToolCall{CallID: b.ID, ToolName: b.Name, Input: b.Input, Status: "running"}
			if b.Name == "TodoWrite" {
				t.Tasks = todos(b.Input)
			}
			s.tools[b.ID] = t
			out = append(out, session.Event{Type: session.EventContent, ItemID: b.ID, Kind: session.KindToolCall, Model: m.Model, Tool: &t})
			continue
		case "text":
		default:
			continue
		}
		// Only what streaming did not give; an interrupted turn's line can
		// hold less than was streamed.
		if n := s.streamed[id]; n < len(text) {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: kind, Model: m.Model, Text: text[n:]})
		}
	}
	return out
}

// todos is TodoWrite's input as the call's tasks.
func todos(input json.RawMessage) []session.Task {
	var in struct {
		Todos []struct{ Content, Status string } `json:"todos"`
	}
	json.Unmarshal(input, &in)
	tasks := []session.Task{}
	for _, t := range in.Todos {
		tasks = append(tasks, session.Task{Status: t.Status, Subject: t.Content})
	}
	return tasks
}

// user is a tool's result ending its call. Prompts the CLI echoes and its
// "[Request interrupted by user]" lines give nothing.
func (s *stream) user(l line) []session.Event {
	var m apiMessage
	var blocks []block
	if json.Unmarshal(l.Message, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []session.Event
	for _, b := range blocks {
		t, ok := s.tools[b.ToolUseID]
		if b.Type != "tool_result" || !ok {
			continue
		}
		t.Status, t.Output = "completed", resultText(b.Content)
		if b.IsError {
			t.Status = "failed"
		}
		s.tools[b.ToolUseID] = t
		out = append(out, session.Event{Type: session.EventContent, ItemID: b.ToolUseID, Kind: session.KindToolCall, Tool: &t})
	}
	return out
}

// resultText is a tool_result's content as text: the string, or its text
// blocks one per line.
func resultText(c json.RawMessage) string {
	var text string
	if json.Unmarshal(c, &text) == nil {
		return text
	}
	var blocks []block
	json.Unmarshal(c, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// result ends the turn, then reports its usage as aide does.
func (s *stream) result(l line) []session.Event {
	var out []session.Event
	turn := s.turn
	if turn != "" {
		e := session.Event{Type: session.EventTurnCompleted, TurnID: turn, State: session.TurnCompleted, Point: s.point}
		switch {
		case !l.IsError:
		case l.TerminalReason == "aborted_streaming" || l.TerminalReason == "aborted_tools":
			e.State = session.TurnInterrupted
		default:
			e.State, e.ErrorMessage = session.TurnFailed, l.Result
			if e.ErrorMessage == "" {
				e.ErrorMessage = strings.Join(l.Errors, "\n")
			}
		}
		out = append(out, e)
	}

	u := session.Event{Type: session.EventUsage, CachedInputTokens: new(int64), ContextTokens: s.contextTokens}
	r := &session.UsageReport{ID: l.UUID, ScopeID: l.SessionID, Provider: "claude", Models: []session.ModelUsage{},
		EstimatedCostUSD: l.TotalCostUSD}
	if turn != "" {
		r.TurnID = &turn
	}
	names := make([]string, 0, len(l.ModelUsage))
	for name := range l.ModelUsage {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		m := l.ModelUsage[name]
		u.InputTokens += m.InputTokens
		u.OutputTokens += m.OutputTokens
		*u.CachedInputTokens += m.CacheReadInputTokens
		r.Models = append(r.Models, session.ModelUsage{Model: &name, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
			CachedInputTokens: &m.CacheReadInputTokens, CacheCreationInputTokens: &m.CacheCreationInputTokens, EstimatedCostUSD: m.CostUSD})
	}
	if w := l.ModelUsage[s.contextModel].ContextWindow; w > 0 {
		u.MaxTokens = &w
	}
	u.Report = r
	out = append(out, u)

	s.turn, s.point = "", ""
	clear(s.streamed)
	clear(s.blocks)
	clear(s.tools)
	return out
}

// limit is a rate_limit_event's plan limit.
func (s *stream) limit(l line) []session.Event {
	info := l.RateLimitInfo
	if info == nil {
		return nil
	}
	lim := &session.Limit{Rejected: info.Status == "rejected", Windows: []session.LimitWindow{}}
	if info.ResetsAt > 0 {
		lim.Resets = time.Unix(info.ResetsAt, 0).UTC()
	}
	for _, k := range []struct {
		name    string
		minutes int64
	}{{"five_hour", 5 * 60}, {"seven_day", 7 * 24 * 60}} {
		w, ok := info.UnifiedWindows[k.name]
		if !ok {
			continue
		}
		lw := session.LimitWindow{Minutes: k.minutes, Used: w.Utilization * 100}
		if w.ResetsAt > 0 {
			lw.Resets = time.Unix(w.ResetsAt, 0).UTC()
		}
		lim.Windows = append(lim.Windows, lw)
	}
	return []session.Event{{Type: session.EventLimit, Limit: lim}}
}

// canUseTool is Claude asking before a tool runs: AskUserQuestion asks
// the user questions, ExitPlanMode asks to leave plan mode with a plan,
// any other tool asks for permission.
func (s *stream) canUseTool(l line) []session.Event {
	r := l.Request
	s.pending[l.RequestID] = ask{tool: r.ToolName, input: r.Input, suggestions: r.Suggestions}
	var in struct {
		Plan      string `json:"plan"`
		Command   string `json:"command"`
		Cwd       string `json:"cwd"`
		FilePath  string `json:"file_path"`
		Questions []struct {
			Question, Header string
			Options          []struct{ Label, Description string }
		} `json:"questions"`
	}
	json.Unmarshal(r.Input, &in)
	switch r.ToolName {
	case "AskUserQuestion":
		e := session.Event{Type: session.EventInputRequested, RequestID: l.RequestID, Questions: []session.Question{}}
		for _, q := range in.Questions {
			// The question text is its id: Claude takes answers keyed by it.
			sq := session.Question{ID: q.Question, Prompt: q.Question, Header: q.Header, Options: []session.PromptOption{}}
			for _, o := range q.Options {
				sq.Options = append(sq.Options, session.PromptOption{ID: o.Label, Label: o.Label, Description: o.Description})
			}
			e.Questions = append(e.Questions, sq)
		}
		if len(e.Questions) > 0 {
			e.Detail = e.Questions[0].Prompt
		}
		return []session.Event{e}
	case "ExitPlanMode":
		return []session.Event{{Type: session.EventRequestOpened, RequestID: l.RequestID, RequestType: session.RequestPlan,
			Detail: in.Plan, Request: &session.ToolRequest{ToolName: r.ToolName, Input: r.Input}}}
	}
	detail := in.Command
	if detail == "" {
		detail = string(r.Input)
	}
	if r := []rune(detail); len(r) > 240 {
		detail = string(r[:240])
	}
	return []session.Event{{Type: session.EventRequestOpened, RequestID: l.RequestID, RequestType: requestType(r.ToolName),
		Detail:  r.ToolName + ": " + detail,
		Request: &session.ToolRequest{ToolName: r.ToolName, Input: r.Input, Command: in.Command, Cwd: in.Cwd, FilePath: in.FilePath}}}
}

// requestType is aide's classifyToolRequestType.
func requestType(tool string) session.RequestType {
	t := strings.ToLower(tool)
	switch {
	case strings.Contains(t, "read"):
		return session.RequestFileRead
	case strings.Contains(t, "edit"), strings.Contains(t, "write"), strings.Contains(t, "patch"), strings.Contains(t, "replace"):
		return session.RequestFileChange
	case strings.Contains(t, "bash"), strings.Contains(t, "command"), strings.Contains(t, "shell"):
		return session.RequestCommand
	}
	return session.RequestDynamicTool
}

// withdraw closes request id, which Claude no longer waits on: it was
// interrupted, or answered elsewhere.
func (s *stream) withdraw(id string) []session.Event {
	a, ok := s.pending[id]
	if !ok {
		return nil
	}
	delete(s.pending, id)
	if a.tool == "AskUserQuestion" {
		return []session.Event{{Type: session.EventInputResolved, RequestID: id}}
	}
	return []session.Event{{Type: session.EventRequestResolved, RequestID: id}}
}
