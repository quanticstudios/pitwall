package claude

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
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
	// cmd is a /model or /effort command waiting for its output, which
	// turns the two into one divider; cmdID is its line's uuid.
	cmd, cmdID string
	compacted  bool              // a compaction since the last prompt: its /compact needs no chip
	notifiedOf map[string]bool   // task id and state of each notification shown: Claude Code may queue one, then add it again
	used       map[string]msgUse // the running turn's API messages' usage, by message id
	usedOrder  []string
}

// msgUse is one API message's usage, which each of its lines repeats.
type msgUse struct {
	model string
	t     flow.Tokens
}

func NewTranscript() *Transcript { return &Transcript{s: newStream(""), used: map[string]msgUse{}} }

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
	// queue-operation: what Claude Code queued, such as a task notification
	Operation string `json:"operation"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	IsAPIError bool  `json:"isApiErrorMessage"`
	ThinkingMS int64 `json:"thinkingDurationMs"`

	// system lines: local_command, compact_boundary, informational,
	// api_error, stop_hook_summary
	Content       json.RawMessage `json:"content"` // a string in system lines and queue operations
	Level         string          `json:"level"`
	Error         json.RawMessage `json:"error"`
	RetryInMS     float64         `json:"retryInMs"`
	RetryAttempt  int             `json:"retryAttempt"`
	MaxRetries    int             `json:"maxRetries"`
	HookErrors    []string        `json:"hookErrors"`
	PreventedStop bool            `json:"preventedContinuation"`
	StopReason    string          `json:"stopReason"`
	Attachment    *struct {
		Type     string          `json:"type"`
		Prompt   json.RawMessage `json:"prompt"`
		HookName string          `json:"hookName"`
		Message  string          `json:"message"`
		Stderr   string          `json:"stderr"`
		ExitCode *int            `json:"exitCode"`
		Blocking *struct {
			Error string `json:"blockingError"`
		} `json:"blockingError"`
	} `json:"attachment"`
}

// Line is one transcript line's events, stamped with the line's time.
func (t *Transcript) Line(b []byte) []session.Event {
	var l transcriptLine
	if json.Unmarshal(b, &l) != nil || l.IsSidechain {
		return nil
	}
	cmd := t.cmd
	var out []session.Event
	switch l.Type {
	case "user":
		if text, ok := promptText(l); ok {
			out = t.prompt(l, text)
			break
		}
		if text, ok := userText(l); ok {
			out = t.harness(l, text)
			break
		}
		out = t.s.line(b)
	case "assistant":
		if l.IsAPIError {
			out = []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: session.PhaseError,
				Text: apiError(blocksText(l.Message.Content), time.Now())}}
			break
		}
		if m := l.Message.Model; m != "" && m != "<synthetic>" && m != t.model {
			t.model = m
			out = append(out, session.Event{Type: session.EventSettings, Model: m})
		}
		t.use(l)
		out = append(out, t.s.line(b)...)
	case "system":
		out = t.system(l)
	case "attachment":
		out = t.attachment(l)
	case "queue-operation":
		var text string
		if l.Operation != "enqueue" || json.Unmarshal(l.Content, &text) != nil || !strings.Contains(text, "<task-notification>") {
			return nil
		}
		out = t.s.notice(text) // a background shell ended
	default:
		return nil // summaries, snapshots, queue operations: nothing a chat shows
	}
	// A command whose output did not follow it shows as itself; attachment
	// and system lines may come between the two.
	if cmd != "" && t.cmd == cmd && (l.Type == "user" || l.Type == "assistant") {
		t.cmd = ""
		out = append([]session.Event{t.command(t.cmdID, cmd)}, out...)
	}
	at := stamp(l.Timestamp)
	for i := 0; i < len(out); i++ {
		e := out[i]
		if e.Type == session.EventContent && e.Tool != nil && e.Tool.Status == "running" {
			t.last = e.ItemID
		}
		out[i].CreatedAt = at
		// A thinking row times the thinking: it starts that long before
		// the line that ends it.
		if e.Kind == session.KindReasoningText && l.ThinkingMS > 0 {
			start := e
			start.Text, start.CreatedAt = "", stampAt(l.Timestamp, -time.Duration(l.ThinkingMS)*time.Millisecond)
			out = slices.Insert(out, i, start)
			i++
		}
	}
	return out
}

// system is a system line: a turn's end, a local command, a compaction,
// a notice, an API error Claude Code retries, a Stop hook's verdict.
func (t *Transcript) system(l transcriptLine) []session.Event {
	switch l.Subtype {
	case "turn_duration":
		return t.End(session.TurnCompleted) // Claude Code's mark at the end of each turn
	case "local_command":
		return t.harness(l, str(l.Content))
	case "compact_boundary":
		t.s.compact, t.compacted = cmp.Or(l.UUID, "compact:"+l.Timestamp), true
		return []session.Event{{Type: session.EventContent, ItemID: t.s.compact, Kind: session.KindSystem, Phase: session.PhaseCompact, Text: "Conversation compacted"}}
	case "informational":
		text := str(l.Content)
		if text == "" {
			return nil
		}
		phase := session.PhaseNotice
		if l.Level == "warning" {
			phase = session.PhaseWarning
		}
		return []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: phase, Text: text}}
	case "api_error":
		return []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: session.PhaseWarning,
			Text: retryLine(nil, errText(l.Error), int64(l.RetryInMS), l.RetryAttempt, l.MaxRetries)}}
	case "stop_hook_summary":
		switch {
		case l.PreventedStop:
			return []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: session.PhaseHook,
				Text: strings.TrimSuffix("Stop hook kept Claude going: "+l.StopReason, ": ")}}
		case len(l.HookErrors) > 0:
			return []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: session.PhaseHook,
				Text: "Stop hook failed: " + l.HookErrors[0]}}
		}
	}
	return nil
}

// attachment is an attachment line: a notification Claude Code queued
// mid-turn, or a hook that blocked or failed. The rest (reminders, file
// and tool listings) speak to the model.
func (t *Transcript) attachment(l transcriptLine) []session.Event {
	a := l.Attachment
	if a == nil {
		return nil
	}
	switch a.Type {
	case "queued_command":
		var text string
		if json.Unmarshal(a.Prompt, &text) == nil && strings.Contains(text, "<task-notification>") {
			return t.notified(l, harness(text), text)
		}
	case "hook_blocking_error", "hook_non_blocking_error", "hook_stopped_continuation", "hook_error_during_execution":
		msg := cmp.Or(a.Message, a.Stderr)
		if a.Blocking != nil {
			msg = cmp.Or(a.Blocking.Error, msg)
		}
		code := a.ExitCode
		if a.Type == "hook_blocking_error" {
			code = new(int)
			*code = 2
		}
		return []session.Event{{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: session.PhaseHook,
			Text: hookLine(a.HookName, code, msg)}}
	}
	return nil
}

// harness is a user line, or a local command's system line, that the
// user did not type: a command and its output, a notification, a
// compaction's summary, a hook's feedback. Reminders and caveats show
// nothing.
func (t *Transcript) harness(l transcriptLine, text string) []session.Event {
	n := harness(text)
	if n.notified != "" {
		return t.notified(l, n, text)
	}
	item := func(phase, text string) session.Event {
		return session.Event{Type: session.EventContent, ItemID: l.UUID, Kind: session.KindSystem, Phase: phase, Text: text}
	}
	switch n.phase {
	case session.PhaseCommand:
		switch {
		case strings.HasPrefix(n.text, "/model") || strings.HasPrefix(n.text, "/effort"):
			t.cmd, t.cmdID = n.text, l.UUID
			return nil
		case strings.HasPrefix(n.text, "/compact") && t.compacted:
			return nil
		}
		return []session.Event{t.command(l.UUID, n.text)}
	case session.PhaseOutput:
		cmd := t.cmd
		t.cmd = ""
		if s, ok := settingNote(n.text); ok && cmd != "" {
			return []session.Event{item(session.PhaseSetting, s)}
		}
		var out []session.Event
		if cmd != "" {
			out = append(out, t.command(t.cmdID, cmd))
		}
		if n.text == "" || compacted(n.text) {
			return out
		}
		return append(out, item(session.PhaseOutput, n.text))
	case session.PhaseCompact:
		if c := t.s.compact; c != "" {
			t.s.compact = ""
			return []session.Event{{Type: session.EventContent, ItemID: c, Kind: session.KindSystem, Text: "\n\n" + n.text}}
		}
		return []session.Event{item(session.PhaseCompact, "Conversation compacted\n\n"+n.text)}
	case session.PhaseHook:
		return []session.Event{item(session.PhaseHook, n.text)}
	}
	return nil
}

// command is a slash or ! command's chip item.
func (t *Transcript) command(id, text string) session.Event {
	return session.Event{Type: session.EventContent, ItemID: cmp.Or(id, "cmd:"+text), Kind: session.KindSystem, Phase: session.PhaseCommand, Text: text}
}

// notified is a <task-notification>: a background subagent's card ends,
// another task gets its row. One that reaches an idle Claude starts a
// turn of its own, which Claude Code runs without a prompt.
func (t *Transcript) notified(l transcriptLine, n note, text string) []session.Event {
	out := t.s.notice(text) // a background shell's call takes its state
	if t.notifiedOf == nil {
		t.notifiedOf = map[string]bool{}
	}
	if key := n.task + "/" + n.status; !t.notifiedOf[key] {
		t.notifiedOf[key] = true
		t.s.bgTasks[n.task] = true // only a background task notifies
		out = append(out, t.s.taskDone(n.task, n.notified, n.status, n.summary, n.result, n.result != "", l.UUID)...)
	}
	if l.Origin != nil && l.Origin.Kind == "task-notification" {
		out = append(t.End(session.TurnCompleted), out...)
		clear(t.s.blocks)
		clear(t.s.tools)
		t.turn = cmp.Or(l.UUID, "notified:"+l.Timestamp)
		out = append(out, session.Event{Type: session.EventTurnStarted, TurnID: t.turn})
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
	t.compacted = false
	t.turn = cmp.Or(l.UUID, "prompt:"+l.Timestamp)
	return append(out,
		session.Event{Type: session.EventContent, ItemID: t.turn, Kind: session.KindUserPrompt, Text: text},
		session.Event{Type: session.EventTurnStarted, TurnID: t.turn})
}

// promptText is the text of a user line that is something the user said,
// false for a tool's result, a slash command's echo, a ! command's output,
// a harness note or a compaction summary, as flow reads them. A line
// Claude Code marks as the human's is a prompt even when it starts with
// markup, as pasted text does.
func promptText(l transcriptLine) (string, bool) {
	if l.IsMeta || l.IsCompactSummary || l.Origin != nil && l.Origin.Kind != "human" {
		return "", false
	}
	human := l.Origin != nil && l.Origin.Kind == "human"
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
	if strings.HasPrefix(t, "<") && !strings.HasPrefix(t, "<pasted_content") {
		// Claude Code marks a skill's or custom command's line as the
		// human's; a built-in's, such as /model, comes unmarked and draws as
		// a command chip or a divider.
		if !human {
			return "", false
		}
		if cmd, ok := slashPrompt(t); ok {
			return cmd, true
		}
	}
	return text, t != ""
}

// slashPrompt is a skill's or custom command's line as the user typed it,
// "/name args": Claude Code writes those with a <command-message> and runs
// a turn for them.
func slashPrompt(t string) (string, bool) {
	tag := func(name string) string {
		_, rest, ok := strings.Cut(t, "<"+name+">")
		in, _, closed := strings.Cut(rest, "</"+name+">")
		if !ok || !closed {
			return ""
		}
		return strings.TrimSpace(in)
	}
	cmd := tag("command-name")
	if !strings.HasPrefix(cmd, "/") || !strings.Contains(t, "<command-message>") {
		return "", false
	}
	if args := tag("command-args"); args != "" {
		cmd += " " + args
	}
	return cmd, true
}

// userText is a user line's text when it holds no tool result: a string,
// or text blocks one per line.
func userText(l transcriptLine) (string, bool) {
	var text string
	if json.Unmarshal(l.Message.Content, &text) == nil {
		return text, true
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return "", false
		}
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// blocksText is a message content's text: a string, or its text blocks.
func blocksText(c json.RawMessage) string {
	var text string
	if json.Unmarshal(c, &text) == nil {
		return text
	}
	return resultText(c)
}

// use counts an assistant line's API message once toward the turn's
// usage: each of its lines repeats the message's usage.
func (t *Transcript) use(l transcriptLine) {
	u, id := l.Message.Usage, l.Message.ID
	if u == nil || id == "" || t.turn == "" {
		return
	}
	if _, ok := t.used[id]; !ok {
		t.usedOrder = append(t.usedOrder, id)
	}
	t.used[id] = msgUse{l.Message.Model, flow.Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}}
}

// usage is the ending turn's usage, as the result line reports it in
// stream-json: its tokens and cost by model. The pane chat adds the
// context. ok is false when the turn made no call.
func (t *Transcript) usage() (session.Event, bool) {
	if len(t.usedOrder) == 0 {
		return session.Event{}, false
	}
	by := map[string]flow.Tokens{}
	for _, id := range t.usedOrder {
		m := t.used[id]
		k := by[m.model]
		k.Input, k.Output, k.CacheRead, k.CacheWrite = k.Input+m.t.Input, k.Output+m.t.Output, k.CacheRead+m.t.CacheRead, k.CacheWrite+m.t.CacheWrite
		by[m.model] = k
	}
	clear(t.used)
	t.usedOrder = t.usedOrder[:0]
	turn := t.turn
	e := session.Event{Type: session.EventUsage, CachedInputTokens: new(int64)}
	r := &session.UsageReport{ID: "turn:" + turn, ScopeID: "SESSION", Provider: "claude", TurnID: &turn, Models: []session.ModelUsage{}}
	var total float64
	priced := true
	for _, name := range slices.Sorted(maps.Keys(by)) {
		k := by[name]
		e.InputTokens += k.Input
		e.OutputTokens += k.Output
		*e.CachedInputTokens += k.CacheRead
		mu := session.ModelUsage{Model: &name, InputTokens: k.Input, OutputTokens: k.Output, CachedInputTokens: &k.CacheRead, CacheCreationInputTokens: &k.CacheWrite}
		if c, ok := (flow.Usage{Models: map[string]flow.Tokens{name: k}}).Cost(); ok {
			mu.EstimatedCostUSD = &c
			total += c
		} else {
			priced = false
		}
		r.Models = append(r.Models, mu)
	}
	if priced {
		r.EstimatedCostUSD = &total
	}
	e.Report = r
	return e, true
}

// apiError is the text of Claude Code's synthetic API error message: a
// usage limit's "…|<unix time>" says when it resets.
func apiError(text string, now time.Time) string {
	text = strings.TrimSpace(text)
	if msg, at, ok := strings.Cut(text, "|"); ok {
		if sec, err := strconv.ParseInt(strings.TrimSpace(at), 10, 64); err == nil {
			when := time.Unix(sec, 0).Local()
			format := "15:04"
			if when.Sub(now) > 20*time.Hour {
				format = "Mon 15:04"
			}
			return strings.TrimSpace(msg) + " · resets " + when.Format(format)
		}
	}
	return text
}

// Subagents are the session's subagent calls by tool call id, and
// whether each still runs.
func (t *Transcript) Subagents() map[string]bool {
	out := map[string]bool{}
	for id, c := range t.s.agents {
		out[id] = c.Status == "running"
	}
	return out
}

// Subagent is one line of the transcript of the subagent that tool call
// call runs: its calls go on that call's card, as stream-json's lines with
// a parent_tool_use_id do.
func (t *Transcript) Subagent(call string, b []byte) []session.Event {
	var l line
	var tl transcriptLine
	if json.Unmarshal(b, &l) != nil || json.Unmarshal(b, &tl) != nil {
		return nil
	}
	l.ParentToolUseID = call
	var out []session.Event
	switch l.Type {
	case "assistant":
		out = t.s.assistant(l)
	case "user":
		out = t.s.user(l)
	}
	for i := range out {
		out[i].CreatedAt = stamp(tl.Timestamp)
	}
	return out
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
	if u, ok := t.usage(); ok {
		out = append(out, u)
	}
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
func stamp(ts string) string { return stampAt(ts, 0) }

// stampAt is ts moved by d, as stamp writes it.
func stampAt(ts string, d time.Duration) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		at = time.Now()
	}
	return session.Stamp(at.Add(d))
}

// str is a JSON string's value, "" for anything else.
func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
