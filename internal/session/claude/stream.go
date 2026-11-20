package claude

import (
	"cmp"
	"encoding/json"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
)

// stream is what the driver knows between stdout lines: the turn that
// runs, and what that turn printed so far.
type stream struct {
	resume  string // the session id the chat loop knows
	version string // claude_code_version, once system/init gave it
	turn    string // the running turn's id, "" while idle
	point   string // the uuid of the turn's last assistant line
	recap   bool   // the running turn is /recap, whose output Claude Code sends as its own reply

	streamed map[string]int              // text item id: bytes stream_event deltas gave it
	blocks   map[string]int              // message id: content blocks its assistant lines gave so far
	tools    map[string]session.ToolCall // tool_use id: the call so far
	bg       map[string]session.ToolCall // tool_use id: a call whose shell runs in the background
	tasks    map[string]string           // a background shell's task id: its call's tool_use id
	// agentsBg are the subagents that run in the background, by tool_use
	// id: they outlive the turn that started them.
	agentsBg map[string]session.ToolCall
	// agents are every subagent call of the session, by tool_use id, so
	// a pane chat that reads a subagent's own transcript late still finds
	// its card.
	agents map[string]session.ToolCall
	// todoList is the task list TaskCreate and TaskUpdate keep, which
	// lasts the session.
	todoList []session.Task
	compact  string // the compaction item whose summary comes next, "" for none
	// modelEchoes counts the set_model requests whose "Set model to"
	// output is still to come; the chat loop already showed each.
	modelEchoes int
	harnessed   map[string]bool // user lines read as harness text, by uuid: a compaction replays them

	contextModel  string
	contextTokens *int64
	usage         *session.Event // the last usage event, which a new context size updates

	commands []string   // the slash_commands system/init last listed
	info     statusInfo // what system/init said, for /status

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
		blocks: map[string]int{}, tools: map[string]session.ToolCall{}, pending: map[string]ask{},
		agentsBg: map[string]session.ToolCall{}, agents: map[string]session.ToolCall{}, harnessed: map[string]bool{}}
}

// line is one stdout line, the fields of every type the driver reads.
type line struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	UUID      string `json:"uuid"`
	SessionID string `json:"session_id"`
	// ParentToolUseID is the Task call a subagent's line belongs to, "" on the main thread.
	ParentToolUseID string `json:"parent_tool_use_id"`

	// system/init
	Model        string   `json:"model"`
	Version      string   `json:"claude_code_version"`
	Capabilities []string `json:"capabilities"`
	// SlashCommands are names without the slash; the terminal ones need the TUI.
	SlashCommands         []string `json:"slash_commands"`
	TerminalSlashCommands []string `json:"terminal_slash_commands"`
	Cwd                   string   `json:"cwd"`
	PermissionMode        string   `json:"permissionMode"`

	// system/permission_denied: a text; assistant and user: an apiMessage
	Message   json.RawMessage `json:"message"`
	ToolUseID string          `json:"tool_use_id"`
	// user: the CLI's own result beside a tool_result; the transcript file
	// spells it toolUseResult.
	ToolUseResult  json.RawMessage `json:"tool_use_result"`
	ToolUseResult2 json.RawMessage `json:"toolUseResult"`

	// system/task_notification, system/task_updated: a background task's state
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Patch  struct {
		Status string `json:"status"`
	} `json:"patch"`
	// system/task_progress, task_notification: what a subagent card reads
	Description     string     `json:"description"`
	Summary         string     `json:"summary"`
	SummaryIsReport bool       `json:"summary_is_report"`
	TaskUsage       *taskUsage `json:"usage"`
	// system/hook_response
	HookName string `json:"hook_name"`
	ExitCode *int   `json:"exit_code"`
	Outcome  string `json:"outcome"`
	Stderr   string `json:"stderr"`
	Stdout   string `json:"stdout"`
	// system/api_retry
	Attempt      int             `json:"attempt"`
	MaxRetries   int             `json:"max_retries"`
	RetryDelayMS int64           `json:"retry_delay_ms"`
	ErrorStatus  *int            `json:"error_status"`
	Error        json.RawMessage `json:"error"`

	// system/away_summary, informational: the text; informational's level
	Content json.RawMessage `json:"content"`
	Level   string          `json:"level"`

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

	// assistant: a local command's synthetic reply
	LocalSource string          `json:"local_command_source"`
	LocalRun    *localRun       `json:"local_command_run"`
	UsageReport json.RawMessage `json:"usage_report"`

	// control_request, control_cancel_request
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype     string            `json:"subtype"`
		ToolName    string            `json:"tool_name"`
		Input       json.RawMessage   `json:"input"`
		Suggestions []json.RawMessage `json:"permission_suggestions"`
	} `json:"request"`
}

type taskUsage struct {
	ToolUses int `json:"tool_uses"`
}

type streamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
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
	Signature string          `json:"signature"` // thinking: says when the block is a summary, not reasoning
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // tool_result: a string or text blocks
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"` // image: a base64 picture
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
		s.info = statusInfo{version: l.Version, session: l.SessionID, cwd: l.Cwd, mode: l.PermissionMode}
		if l.SessionID != "" && (l.SessionID != s.resume || s.version == "") {
			if s.version == "" {
				log.Printf("claude: Claude Code %s", l.Version)
			}
			s.resume, s.version = l.SessionID, l.Version
			out = append(out, session.Event{Type: session.EventResumeID, ResumeID: l.SessionID,
				Version: l.Version, Capabilities: l.Capabilities})
		}
		if l.Model != "" && l.Model != s.contextModel {
			out = append(out, session.Event{Type: session.EventSettings, Model: l.Model}) // the model the CLI runs, picked or its default
			s.contextModel = l.Model
		}
		if l.SlashCommands != nil && !slices.Equal(l.SlashCommands, s.commands) {
			s.commands = l.SlashCommands
			cmds := make([]session.Command, 0, len(l.SlashCommands))
			for _, name := range l.SlashCommands {
				cmds = append(cmds, session.Command{Name: name,
					Terminal: session.TerminalCommand(name) || slices.Contains(l.TerminalSlashCommands, name)})
			}
			out = append(out, session.Event{Type: session.EventCommands, Commands: cmds})
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
	case "task_progress":
		t, ok := s.call(l.ToolUseID)
		if !ok || !subagentTool(t.ToolName) || l.Description == "" || t.Progress == l.Description {
			return nil
		}
		t.Progress = l.Description
		return s.put(t)
	case "task_notification":
		// A background shell's call takes its state; a subagent's card
		// ends; a task started in the background gets its row.
		out := s.background(l.ToolUseID, l.Status)
		return append(out, s.taskDone(l.ToolUseID, l.Status, l.Summary, l.Summary, l.SummaryIsReport)...)
	case "task_updated":
		return s.background(s.tasks[l.TaskID], l.Patch.Status)
	case "compact_boundary":
		s.compact = cmp.Or(l.UUID, "compact:"+l.SessionID)
		return []session.Event{{Type: session.EventContent, ItemID: s.compact, Kind: session.KindSystem, Phase: session.PhaseCompact, Text: "Conversation compacted"}}
	case "hook_response":
		if l.Outcome == "success" || l.Outcome == "" && (l.ExitCode == nil || *l.ExitCode == 0) {
			return nil
		}
		return []session.Event{{Type: session.EventContent, ItemID: cmp.Or(l.UUID, "hook:"+l.HookName), Kind: session.KindSystem,
			Phase: session.PhaseHook, Text: hookLine(l.HookName, l.ExitCode, cmp.Or(l.Stderr, l.Stdout))}}
	case "api_retry":
		return []session.Event{{Type: session.EventContent, ItemID: cmp.Or(l.UUID, "retry:"+strconv.Itoa(l.Attempt)), Kind: session.KindSystem,
			Phase: session.PhaseWarning, Text: retryLine(l.ErrorStatus, errText(l.Error), l.RetryDelayMS, l.Attempt, l.MaxRetries)}}
	case "informational": // a hook's systemMessage, a notice
		var text string
		json.Unmarshal(l.Content, &text)
		return informational(cmp.Or(l.UUID, "info:"+text), text, l.Level)
	case "away_summary":
		var text string
		json.Unmarshal(l.Content, &text)
		return recap(cmp.Or(l.UUID, "recap:"+text), text)
	case "status", "task_started", "thinking_tokens", "hook_started", "hook_progress", "background_tasks_changed":
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
	if l.Event == nil || l.ParentToolUseID != "" {
		return nil
	}
	id := l.APIMessageID + ":" + strconv.Itoa(l.Event.Index)
	if l.Event.Type == "content_block_start" {
		// A thinking row starts timing when the model starts to think;
		// its text may never come, as Claude hides it.
		if t := l.Event.ContentBlock.Type; t == "thinking" || t == "redacted_thinking" {
			return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindReasoningText, Model: s.contextModel}}
		}
		return nil
	}
	if l.Event.Type != "content_block_delta" {
		return nil
	}
	// A thinking block's text waits for its finished line: only that
	// line's signature says whether the text is a summary, and a summary
	// must never show as thinking, not even while it streams.
	if d := l.Event.Delta; d.Type == "text_delta" && d.Text != "" {
		s.streamed[id] += len(d.Text)
		return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindAssistantText, Model: s.contextModel, Text: d.Text}}
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
	if l.ParentToolUseID != "" {
		return s.subagent(l.ParentToolUseID, blocks)
	}
	if s.recap && m.Model == "<synthetic>" {
		s.recap = false
		var text []string
		for _, b := range blocks {
			if b.Type == "text" {
				text = append(text, b.Text)
			}
		}
		return recap(cmp.Or(m.ID, "recap:"+s.turn), strings.Join(text, "\n"))
	}
	if l.LocalSource != "" {
		return s.localOutput(l)
	}
	if m.Model != "" {
		s.contextModel = m.Model
	}
	var out []session.Event
	if u := m.Usage; u != nil {
		t := u.Input + u.Output + u.CacheRead + u.CacheCreation
		// The composer's context meter moves with each API response, not
		// only when the turn ends; the counts stay the last result's.
		if s.usage != nil && (s.contextTokens == nil || *s.contextTokens != t) {
			e := *s.usage
			e.ContextTokens = &t
			s.usage = &e
			out = append(out, e)
		}
		s.contextTokens = &t
	}
	if l.UUID != "" {
		s.point = l.UUID
	}
	for _, b := range blocks {
		id := m.ID + ":" + strconv.Itoa(s.blocks[m.ID])
		s.blocks[m.ID]++
		kind, text, phase := session.KindAssistantText, b.Text, ""
		switch b.Type {
		case "thinking", "redacted_thinking":
			kind, text = session.KindReasoningText, b.Thinking
			if narration(b.Signature) {
				phase = session.PhaseSummary
			}
		case "tool_use":
			t := session.ToolCall{CallID: b.ID, ToolName: b.Name, Input: b.Input, Status: "running"}
			if tasks, ok := s.taskList(b.Name, b.Input); ok {
				t.Tasks = tasks
			}
			s.tools[b.ID] = t
			if subagentTool(b.Name) {
				s.agents[b.ID] = t
			}
			out = append(out, session.Event{Type: session.EventContent, ItemID: b.ID, Kind: session.KindToolCall, Model: m.Model, Tool: &t})
			continue
		case "text":
		default:
			continue
		}
		// Only what streaming did not give; an interrupted turn's line can
		// hold less than was streamed. A thinking block whose text Claude
		// hides still reports, so its row exists and ends now.
		if n := s.streamed[id]; n < len(text) || kind == session.KindReasoningText && text == "" || phase != "" {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: kind, Phase: phase, Model: m.Model, Text: text[min(n, len(text)):]})
		}
	}
	return out
}

// subagent adds a subagent's tool calls to its Task call, as aide does,
// and their inputs to its SubagentCalls, which keeps the newest
// (session.KeepSubagentCalls); its text stays out of the transcript.
func (s *stream) subagent(parent string, blocks []block) []session.Event {
	t, ok := s.call(parent)
	n := len(t.SubagentTools)
	// why: an emitted event still holds the old slices.
	t.SubagentTools, t.SubagentCalls = slices.Clone(t.SubagentTools), slices.Clone(t.SubagentCalls)
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			t.SubagentTools = append(t.SubagentTools, b.Name)
			t.SubagentCalls = append(t.SubagentCalls, session.ToolCall{CallID: b.ID, ToolName: b.Name, Input: b.Input, Status: "running"})
		}
	}
	if !ok || len(t.SubagentTools) == n {
		return nil
	}
	t.SubagentCalls = session.KeepSubagentCalls(t.SubagentCalls)
	return s.put(t)
}

// subagentDone ends the subagent calls that blocks give results for.
func (s *stream) subagentDone(parent string, blocks []block) []session.Event {
	t, ok := s.call(parent)
	if !ok {
		return nil
	}
	t.SubagentCalls = slices.Clone(t.SubagentCalls)
	done := false
	for _, b := range blocks {
		i := slices.IndexFunc(t.SubagentCalls, func(c session.ToolCall) bool { return c.CallID == b.ToolUseID })
		if b.Type != "tool_result" || i < 0 {
			continue
		}
		t.SubagentCalls[i].Status = "completed"
		// Enough of its output for its row's result: a count of matches,
		// of lines read.
		if out := resultText(b.Content); len(out) <= 8<<10 {
			t.SubagentCalls[i].Output = out
		}
		if b.IsError {
			t.SubagentCalls[i].Status = "failed"
		}
		done = true
	}
	if !done {
		return nil
	}
	return s.put(t)
}

// call is tool call id of this turn or a background subagent.
func (s *stream) call(id string) (session.ToolCall, bool) {
	if t, ok := s.agentsBg[id]; ok {
		return t, true
	}
	if t, ok := s.tools[id]; ok {
		return t, true
	}
	t, ok := s.agents[id]
	return t, ok
}

// put stores t where call finds it and reports it.
func (s *stream) put(t session.ToolCall) []session.Event {
	if _, ok := s.agentsBg[t.CallID]; ok {
		s.agentsBg[t.CallID] = t
	} else {
		s.tools[t.CallID] = t
	}
	if subagentTool(t.ToolName) {
		s.agents[t.CallID] = t
	}
	return []session.Event{{Type: session.EventContent, ItemID: t.CallID, Kind: session.KindToolCall, Tool: &t}}
}

// taskDone is a task's notification for a background subagent: its card
// ends with its report. A shell's is background's: its own row says it
// ended, and nothing else does.
func (s *stream) taskDone(callID, status, summary, result string, report bool) []session.Event {
	if t, ok := s.call(callID); ok && subagentTool(t.ToolName) {
		delete(s.agentsBg, callID)
		t.Status, t.Progress = "completed", ""
		if status == "failed" || status == "killed" || status == "stopped" {
			t.Status = "failed"
		}
		if report || result != "" {
			t.Output = cmp.Or(result, summary)
		}
		s.tools[callID], s.agents[callID] = t, t
		return []session.Event{{Type: session.EventContent, ItemID: callID, Kind: session.KindToolCall, Tool: &t}}
	}
	return nil
}

// taskList is the task list after a TodoWrite, TaskCreate or TaskUpdate
// call with input, false for any other call. TodoWrite's input is the
// whole list; the others change the session's list one task at a time.
func (s *stream) taskList(name string, input json.RawMessage) ([]session.Task, bool) {
	var in struct {
		Todos []struct {
			Content, Status string
			ActiveForm      string `json:"activeForm"`
		} `json:"todos"`
		TaskID     string `json:"taskId"`
		Subject    string `json:"subject"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm"`
	}
	json.Unmarshal(input, &in)
	switch name {
	case "TodoWrite":
		tasks := []session.Task{}
		for _, t := range in.Todos {
			tasks = append(tasks, session.Task{Status: t.Status, Subject: t.Content, ActiveForm: t.ActiveForm})
		}
		return tasks, true
	case "TaskCreate":
		n := 0
		for _, t := range s.todoList {
			n = max(n, atoi(t.ID))
		}
		s.todoList = append(slices.Clip(s.todoList), session.Task{ID: strconv.Itoa(n + 1), Status: "pending", Subject: in.Subject, ActiveForm: in.ActiveForm})
	case "TaskUpdate":
		s.todoList = slices.Clone(s.todoList)
		i := slices.IndexFunc(s.todoList, func(t session.Task) bool { return t.ID == in.TaskID })
		switch {
		case i < 0:
		case in.Status == "deleted":
			s.todoList = slices.Delete(s.todoList, i, i+1)
		default:
			t := &s.todoList[i]
			t.Status, t.Subject, t.ActiveForm = cmp.Or(in.Status, t.Status), cmp.Or(in.Subject, t.Subject), cmp.Or(in.ActiveForm, t.ActiveForm)
		}
	default:
		return nil, false
	}
	return append([]session.Task{}, s.todoList...), true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// user is a tool's result ending its call. Prompts the CLI echoes and its
// "[Request interrupted by user]" lines give nothing.
func (s *stream) user(l line) []session.Event {
	var m apiMessage
	var blocks []block
	if json.Unmarshal(l.Message, &m) != nil {
		return nil
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			return nil
		}
		if strings.Contains(text, "<task-notification>") {
			return s.notice(text) // a background shell's call takes its state
		}
		return s.harness(l.UUID, text)
	}
	if l.ParentToolUseID != "" {
		return s.subagentDone(l.ParentToolUseID, blocks)
	}
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(l.ToolUseResult, &result) != nil {
		json.Unmarshal(l.ToolUseResult2, &result)
	}
	var out []session.Event
	for _, b := range blocks {
		t, ok := s.call(b.ToolUseID)
		if b.Type != "tool_result" || !ok {
			continue
		}
		t.Status, t.Output = "completed", resultText(b.Content)
		switch {
		case b.IsError:
			t.Status = "failed"
		case subagentTool(t.ToolName) && result.Status == "async_launched":
			// It runs on; its notification ends it, maybe turns later.
			t.Status, t.Output, t.Progress = "running", "", "Running in the background"
			s.agentsBg[b.ToolUseID] = t
		case t.ToolName == "TaskCreate":
			s.renumber(t.Output)
		}
		res := l.ToolUseResult
		if res == nil {
			res = l.ToolUseResult2
		}
		s.finish(&t, b, res)
		out = append(out, s.put(t)...)
	}
	return out
}

var taskCreated = regexp.MustCompile(`^Task #(\d+) created`)

// renumber gives the task TaskCreate just added the number Claude Code
// says it has.
func (s *stream) renumber(output string) {
	m := taskCreated.FindStringSubmatch(output)
	if n := len(s.todoList); m != nil && n > 0 {
		s.todoList = slices.Clone(s.todoList)
		s.todoList[n-1].ID = m[1]
	}
}

// harness is a user line of text the user did not type: in stream-json
// only a local command's output, and the summary a compaction leaves.
// The CLI's system lines say the rest.
func (s *stream) harness(id, text string) []session.Event {
	if id != "" {
		if s.harnessed[id] {
			return nil
		}
		s.harnessed[id] = true
	}
	n := harness(text)
	switch {
	case n.phase == session.PhaseCompact && s.compact != "":
		c := s.compact
		s.compact = ""
		return []session.Event{{Type: session.EventContent, ItemID: c, Kind: session.KindSystem, Text: "\n\n" + n.text}}
	case n.phase == session.PhaseOutput && n.text != "" && !compacted(n.text):
		phase, text := n.phase, n.text
		if t, ok := settingNote(text); ok {
			if strings.HasPrefix(t, "Switched to ") && s.modelEchoes > 0 {
				s.modelEchoes--
				return nil
			}
			phase, text = session.PhaseSetting, t
		}
		return []session.Event{{Type: session.EventContent, ItemID: cmp.Or(id, "out:"+n.text), Kind: session.KindSystem, Phase: phase, Text: text}}
	}
	return nil
}

// localOutput is a local command's synthetic reply as its output: /usage
// and /cost with the session's tokens, a /status stream-json lacks as
// what system/init said.
func (s *stream) localOutput(l line) []session.Event {
	text, ok := tag(l.LocalSource, "local-command-stdout")
	if !ok {
		text, _ = tag(l.LocalSource, "local-command-stderr")
	}
	text = strings.TrimSpace(text)
	run := localRun{}
	if l.LocalRun != nil {
		run = *l.LocalRun
	}
	switch {
	case run.Command == "usage" || run.Command == "cost":
		text = joinParts(text, reportLines(l.UsageReport))
	case strings.HasPrefix(text, "/status isn't available") && s.info.version != "":
		st := s.info
		st.model = s.contextModel
		text = st.lines()
	}
	return s.harness(l.UUID, "<local-command-stdout>"+text+"</local-command-stdout>")
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
	s.recap = false
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
	live := u
	s.usage = &live
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
			MultiSelect      bool `json:"multiSelect"`
			Options          []struct{ Label, Description, Preview string }
		} `json:"questions"`
	}
	json.Unmarshal(r.Input, &in)
	switch r.ToolName {
	case "AskUserQuestion":
		e := session.Event{Type: session.EventInputRequested, RequestID: l.RequestID, Questions: []session.Question{}}
		for _, q := range in.Questions {
			// The question text is its id: Claude takes answers keyed by it.
			sq := session.Question{ID: q.Question, Prompt: q.Question, Header: q.Header, Multi: q.MultiSelect, Options: []session.PromptOption{}}
			for _, o := range q.Options {
				sq.Options = append(sq.Options, session.PromptOption{ID: o.Label, Label: o.Label, Description: o.Description, Preview: o.Preview})
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
	// The arg, as a tool row shows it: the command, or the file's name,
	// not the raw input the notice and the card would print as JSON.
	detail := flow.ToolArg(session.ToolCall{ToolName: r.ToolName, Input: r.Input, Command: in.Command})
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

// hookLine is a hook that blocked or failed, as its row says it.
func hookLine(name string, code *int, msg string) string {
	verb := "failed"
	if code != nil && *code == 2 {
		verb = "blocked"
	} else if code != nil {
		verb = "failed (exit " + strconv.Itoa(*code) + ")"
	}
	line := cmp.Or(name, "A") + " hook " + verb
	if msg = strings.TrimSpace(msg); msg != "" {
		line += ": " + msg
	}
	return line
}

// retryLine is an API retry as its row says it: API error 529:
// Overloaded · retrying in 4s (attempt 2 of 10).
func retryLine(status *int, msg string, delay int64, attempt, of int) string {
	line := "API error"
	if status != nil {
		line += " " + strconv.Itoa(*status)
	}
	if msg != "" {
		line += ": " + msg
	}
	line += " · retrying"
	if delay > 0 {
		line += " in " + strconv.FormatInt((delay+999)/1000, 10) + "s"
	}
	if attempt > 0 && of > 0 {
		line += " (attempt " + strconv.Itoa(attempt) + " of " + strconv.Itoa(of) + ")"
	}
	return line
}

// errText is an error field as text: a string, or an object's message.
func errText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(raw, &o)
	if o.Error != nil && o.Error.Message != "" {
		return o.Error.Message
	}
	return cmp.Or(o.Message, o.Type)
}
