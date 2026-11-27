package codex

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// Rollout turns the lines of a Codex rollout, the session log the Codex
// TUI writes under CODEX_HOME/sessions, into chat events, for a chat that
// mirrors a Codex TUI running in a pane. Codex 0.152 and later log each
// finished item as an item_completed event; task events bound the turns.
// Codex logs an item only once it ends, so a running command shows when
// it finishes.
type Rollout struct {
	turn       string // the running turn's id; "" while idle
	asked      string // the open request's id, "" for none
	model      string
	effort     string
	total      tokenCounts // running totals from the latest token_count
	window     int64       // model_context_window from the latest token_count
	hasContext bool        // true while the snapshot holds a known ContextTokens
}

func NewRollout() *Rollout { return &Rollout{} }

// rolloutLine is the fields of a rollout line the reader uses.
type rolloutLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type      string          `json:"type"`
		TurnID    string          `json:"turn_id"`
		Reason    string          `json:"reason"`
		Item      json.RawMessage `json:"item"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		CallID    string          `json:"call_id"`
		// turn_context's
		Model  string `json:"model"`
		Effort string `json:"effort"`
		// thread_settings_applied's
		Settings *struct {
			Model  string `json:"model"`
			Effort string `json:"reasoning_effort"`
			Collab struct {
				Settings struct {
					Effort string `json:"reasoning_effort"`
				} `json:"settings"`
			} `json:"collaboration_mode"`
		} `json:"thread_settings"`
		// token_count's
		Info *struct {
			Total  tokenCounts `json:"total_token_usage"`
			Last   tokenCounts `json:"last_token_usage"`
			Window int64       `json:"model_context_window"`
		} `json:"info"`
		StartedMS   int64 `json:"started_at_ms"`
		CompletedMS int64 `json:"completed_at_ms"`
	} `json:"payload"`
}

type tokenCounts struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Output int64 `json:"output_tokens"`
	Total  int64 `json:"total_tokens"`
}

// rolloutItem is an item_completed event's item: the fields of each type
// the reader shows.
type rolloutItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Phase   string `json:"phase"`
	Status  string `json:"status"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Path string `json:"path"` // a local_image's
	} `json:"content"`
	Summary []string `json:"summary_text"`
	Raw     []string `json:"raw_content"`
	// CommandExecution's
	Command  []string               `json:"command"`
	Parsed   []struct{ Cmd string } `json:"parsed_cmd"`
	Output   string                 `json:"aggregated_output"`
	ExitCode *int                   `json:"exit_code"`
	// FileChange's
	Changes map[string]struct {
		Type    string `json:"type"`
		Diff    string `json:"unified_diff"`
		Content string `json:"content"`
	} `json:"changes"`
	Stdout, Stderr string
	// McpToolCall's
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	// Extension web.search's
	Query   string `json:"query"`
	Results []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"results"`
	// ImageView's
	Path string `json:"path"`
}

// Line is one rollout line's events, stamped with the line's time.
func (r *Rollout) Line(b []byte) []session.Event {
	var l rolloutLine
	if json.Unmarshal(b, &l) != nil {
		return nil
	}
	p := l.Payload
	var out []session.Event
	switch l.Type + "/" + p.Type {
	case "event_msg/task_started":
		out = r.End(session.TurnCompleted)
		r.turn = cmp.Or(p.TurnID, "turn:"+l.Timestamp)
		out = append(out, session.Event{Type: session.EventTurnStarted, TurnID: r.turn})
	case "event_msg/task_complete":
		out = r.End(session.TurnCompleted)
	case "event_msg/turn_aborted":
		state := session.TurnCancelled
		if p.Reason == "interrupted" {
			state = session.TurnInterrupted
		}
		out = r.End(state)
	case "event_msg/item_completed":
		out = r.item(p.Item, p.TurnID, p.StartedMS, p.CompletedMS)
	case "event_msg/token_count":
		if p.Info == nil {
			return nil
		}
		r.total = p.Info.Total
		if p.Info.Window > 0 {
			r.window = p.Info.Window
		}
		if p.Info.Last.Total == 0 {
			out = r.resetContext()
			break
		}
		r.hasContext = true
		ctx, cached := p.Info.Last.Total, p.Info.Total.Cached
		e := session.Event{Type: session.EventUsage, InputTokens: p.Info.Total.Input, OutputTokens: p.Info.Total.Output,
			CachedInputTokens: &cached, ContextTokens: &ctx}
		if r.window > 0 {
			w := r.window
			e.MaxTokens = &w
		}
		out = []session.Event{e}
	case "event_msg/context_compacted", "compacted/":
		out = r.resetContext()
	case "event_msg/thread_settings_applied":
		if s := p.Settings; s != nil {
			out = r.settings(s.Model, cmp.Or(s.Effort, s.Collab.Settings.Effort))
		}
	case "turn_context/":
		out = r.settings(p.Model, p.Effort)
	case "response_item/function_call":
		if p.Name == "update_plan" {
			out = r.plan(p.CallID, p.Arguments)
		}
	}
	at := stamp(l.Timestamp)
	for i := range out {
		if out[i].CreatedAt == "" {
			out[i].CreatedAt = at
		}
	}
	return out
}

// settings notes the model and effort a turn runs with: a new model is a
// settings event.
func (r *Rollout) settings(model, effort string) []session.Event {
	r.effort = cmp.Or(effort, r.effort)
	if model == "" || model == r.model {
		return nil
	}
	r.model = model
	return []session.Event{{Type: session.EventSettings, Model: model}}
}

// Settings are the model and effort the session last ran with, "" when
// the rollout has not said.
func (r *Rollout) Settings() (model, effort string) { return r.model, r.effort }

// item is a finished item's events. A prompt with no task running starts
// a turn of its own.
func (r *Rollout) item(raw json.RawMessage, turn string, startMS, endMS int64) []session.Event {
	var it rolloutItem
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return nil
	}
	// tool is c's events: when the item gave its times, c running at its
	// start and then done at its end, so the row says how long it took.
	tool := func(c session.ToolCall) []session.Event {
		if c.Status == "" {
			c.Status = "completed"
			if it.Status == "failed" || it.Status == "declined" {
				c.Status = "failed"
			}
		}
		done := session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: r.model, Tool: &c}
		if startMS <= 0 || endMS <= startMS {
			return []session.Event{done}
		}
		run := c
		run.Status, run.Output, run.ExitCode = "running", "", nil
		start := done
		start.Tool, start.CreatedAt = &run, session.Stamp(time.UnixMilli(startMS))
		done.CreatedAt = session.Stamp(time.UnixMilli(endMS))
		return []session.Event{start, done}
	}
	switch it.Type {
	case "UserMessage":
		text := strings.TrimSpace(it.text())
		var atts []session.Attachment
		for _, c := range it.Content {
			if c.Type != "local_image" {
				continue
			}
			if a, ok := session.ImageFile(c.Path, "", len(atts)+1); ok {
				atts = append(atts, a)
			}
		}
		if text == "" && len(atts) == 0 {
			return nil
		}
		var out []session.Event
		if r.turn == "" {
			r.turn = cmp.Or(turn, it.ID)
			out = append(out, session.Event{Type: session.EventTurnStarted, TurnID: r.turn})
		}
		return append(out, session.Event{Type: session.EventContent, ItemID: it.ID, Kind: session.KindUserPrompt, Text: text, Attachments: atts})
	case "AgentMessage":
		text := it.text()
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []session.Event{{Type: session.EventContent, ItemID: it.ID, Kind: session.KindAssistantText, Phase: it.Phase, Model: r.model, Text: text}}
	case "Reasoning":
		kind, parts := session.KindReasoningSummaryText, it.Summary
		if len(parts) == 0 {
			kind, parts = session.KindReasoningText, it.Raw
		}
		text := strings.TrimSpace(strings.Join(parts, "\n\n"))
		if text == "" {
			return nil
		}
		return []session.Event{{Type: session.EventContent, ItemID: it.ID, Kind: kind, Model: r.model, Text: text}}
	case "CommandExecution":
		cmd := it.command()
		in, _ := json.Marshal(map[string]string{"command": cmd})
		c := session.ToolCall{CallID: it.ID, ToolName: "Bash", Command: cmd, Input: in, Output: it.Output, ExitCode: it.ExitCode}
		if it.ExitCode != nil && *it.ExitCode != 0 {
			c.Status = "failed"
		}
		return tool(c)
	case "FileChange":
		var out []session.Event
		paths := slices.Sorted(maps.Keys(it.Changes))
		for i, path := range paths {
			ch := it.Changes[path]
			c := session.ToolCall{CallID: it.ID, ToolName: "Edit", Diff: ch.Diff,
				Files: []session.ToolFile{{Path: path, Status: ch.Type}}}
			in := map[string]string{"file_path": path}
			if ch.Type == "add" {
				c.ToolName, c.Diff, in["content"] = "Write", "", ch.Content
			}
			c.Input, _ = json.Marshal(in)
			if len(paths) > 1 {
				c.CallID += "#" + strconv.Itoa(i)
			}
			if it.Status == "failed" {
				c.Output = cmp.Or(it.Stderr, it.Stdout)
			}
			out = append(out, tool(c)...)
		}
		return out
	case "McpToolCall":
		c := session.ToolCall{CallID: it.ID, ToolName: "mcp__" + it.Server + "__" + it.Tool, Input: it.Arguments}
		if len(c.Input) == 0 {
			c.Input = json.RawMessage("{}")
		}
		if res := it.Result; res != nil {
			var texts []string
			for _, t := range res.Content {
				texts = append(texts, t.Text)
			}
			c.Output = strings.Join(texts, "\n")
			if res.IsError {
				c.Status = "failed"
			}
		}
		if it.Error != nil {
			c.Output, c.Status = it.Error.Message, "failed"
		}
		return tool(c)
	case "Extension":
		if it.Kind != "web.search" {
			return nil // clock.sleep, image generation: nothing a chat shows
		}
		in, _ := json.Marshal(map[string]string{"query": it.Query})
		c := session.ToolCall{CallID: it.ID, ToolName: "WebSearch", Input: in}
		if links, err := json.Marshal(it.Results); err == nil && len(it.Results) > 0 {
			c.Output = "Links: " + string(links) // the shape Claude's WebSearch output gives the sources in
		}
		return tool(c)
	case "ImageView":
		in, _ := json.Marshal(map[string]string{"file_path": strings.TrimPrefix(it.Path, "file://")})
		return tool(session.ToolCall{CallID: it.ID, ToolName: "Read", Input: in})
	case "ContextCompaction":
		out := []session.Event{{Type: session.EventContent, ItemID: it.ID, Kind: session.KindSystem, Phase: session.PhaseCompact, Text: "Conversation compacted"}}
		return append(out, r.resetContext()...)
	}
	return nil // review mode, subagents, hook prompts: shortcut: not shown yet
}

// resetContext clears the snapshot's ContextTokens after a compaction while
// keeping the running totals and known context window, so the meter drops to
// its neutral state until the next token_count.
func (r *Rollout) resetContext() []session.Event {
	if !r.hasContext {
		return nil
	}
	r.hasContext = false
	cached := r.total.Cached
	e := session.Event{Type: session.EventUsage, InputTokens: r.total.Input, OutputTokens: r.total.Output,
		CachedInputTokens: &cached}
	if r.window > 0 {
		w := r.window
		e.MaxTokens = &w
	}
	return []session.Event{e}
}

// text joins a message item's text parts.
func (it rolloutItem) text() string {
	var parts []string
	for _, c := range it.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// command is the shell command an exec item ran: the script after the
// shell's -c, else the commands Codex parsed out of it.
func (it rolloutItem) command() string {
	if n := len(it.Command); n >= 3 && (it.Command[n-2] == "-lc" || it.Command[n-2] == "-c") {
		return it.Command[n-1]
	}
	var cmds []string
	for _, p := range it.Parsed {
		cmds = append(cmds, p.Cmd)
	}
	if len(cmds) > 0 {
		return strings.Join(cmds, " && ")
	}
	return strings.Join(it.Command, " ")
}

// plan is an update_plan call as the turn's task list, one tool call
// updated in place, as the app-server driver shows turn/plan/updated.
// shortcut: code mode writes update_plan inside an exec call's JavaScript, which this does not read.
func (r *Rollout) plan(call string, args json.RawMessage) []session.Event {
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	var in struct {
		Plan []struct{ Step, Status string } `json:"plan"`
	}
	if json.Unmarshal(args, &in) != nil {
		return nil
	}
	id := "plan-" + cmp.Or(r.turn, call)
	c := &session.ToolCall{CallID: id, ToolName: "Plan", Status: "completed", Input: json.RawMessage("{}"), Tasks: []session.Task{}}
	for _, st := range in.Plan {
		c.Tasks = append(c.Tasks, session.Task{Subject: st.Step, Status: cmp.Or(planStatus[st.Status], planStatus[snake(st.Status)], "pending")})
	}
	return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindToolCall, Model: r.model, Tool: c}}
}

// snake is a rollout's in_progress as the app-server's inProgress.
func snake(s string) string {
	if s == "in_progress" {
		return "inProgress"
	}
	return s
}

// Running reports whether a turn runs.
func (r *Rollout) Running() bool { return r.turn != "" }

// End completes the running turn as state; nothing while idle. It
// withdraws an open request first.
func (r *Rollout) End(state string) []session.Event {
	out := r.Resolve()
	if r.turn == "" {
		return out
	}
	out = append(out, session.Event{Type: session.EventTurnCompleted, TurnID: r.turn, State: state, Point: r.turn})
	r.turn = ""
	return out
}

// Ask opens request id, an approval the Codex TUI shows. Codex logs the
// call it asks about only once it runs, so detail, the hook's tool and
// "\n" and its command or file, is all the request says.
func (r *Rollout) Ask(id, detail string) []session.Event {
	if id == r.asked {
		return nil
	}
	out := r.Resolve()
	r.asked = id
	e := session.Event{Type: session.EventRequestOpened, RequestID: id, RequestType: session.RequestUnknown, Detail: detail}
	if tool, target, ok := strings.Cut(detail, "\n"); ok && tool != "" {
		e.Detail = tool + ": " + target
		e.Request = &session.ToolRequest{ToolName: tool}
		switch tool {
		case "Bash", "shell", "exec_command":
			e.RequestType, e.Request.Command = session.RequestCommand, target
		case "apply_patch", "Edit", "Write":
			e.RequestType, e.Request.FilePath = session.RequestFileChange, target
		}
		key := "command"
		if e.Request.FilePath != "" {
			key = "file_path"
		}
		e.Request.Input, _ = json.Marshal(map[string]string{key: target})
	}
	return append(out, e)
}

// Resolve withdraws the open request, if any.
func (r *Rollout) Resolve() []session.Event {
	if r.asked == "" {
		return nil
	}
	id := r.asked
	r.asked = ""
	return []session.Event{{Type: session.EventRequestResolved, RequestID: id}}
}

// Subagents is nil: a Codex subagent's calls stay in its own rollout.
func (r *Rollout) Subagents() map[string]bool { return nil }

// Subagent shows nothing (Subagents).
func (r *Rollout) Subagent(string, []byte) []session.Event { return nil }

// MarkOf is rollout line b's mark for a reader paging through it: a turn
// starts at task_started.
func MarkOf(b []byte) claude.Mark {
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"payload"`
	}
	if json.Unmarshal(b, &l) != nil {
		return claude.MarkNone
	}
	switch l.Type + "/" + l.Payload.Type {
	case "event_msg/task_started":
		return claude.MarkPrompt
	case "event_msg/item_completed":
		return claude.MarkItem
	case "response_item/function_call":
		if l.Payload.Name == "update_plan" {
			return claude.MarkItem
		}
	}
	return claude.MarkNone
}

// stamp is a rollout timestamp as session.Stamp writes it, now when it
// has none.
func stamp(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		at = now()
	}
	return session.Stamp(at)
}
