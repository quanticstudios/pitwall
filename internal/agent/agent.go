// Package agent maps coding-agent hook events to model.Activity.
//
// Claude Code and Codex hooks send the same JSON shape on stdin
// (hook_event_name, session_id, cwd, tool_name, tool_input, ...), so one
// decoder serves both. Codex's older notify program sends a different object
// as its one argv argument; Derive tells the two apart by hook_event_name.
// pitwall's pi extension sends a small object of its own keyed by "event".
package agent

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
)

// payload holds the fields Derive reads from either agent.
type payload struct {
	Event          string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath json.RawMessage `json:"transcript_path"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      struct {
		Description string `json:"description"`
		Questions   []struct {
			Question string `json:"question"`
		} `json:"questions"`
	} `json:"tool_input"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
	LastMessage      string `json:"last_assistant_message"`
	Error            string `json:"error"`
	ErrorDetails     string `json:"error_details"`
	PermissionMode   string `json:"permission_mode"`
	BackgroundTasks  []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	} `json:"background_tasks"`

	// Codex legacy notify fields.
	Type            string `json:"type"`
	ThreadID        string `json:"thread-id"`
	LastMessageDash string `json:"last-assistant-message"`

	// pi extension fields; it also sends session_id, prompt, tool_name,
	// message and error.
	PiEvent    string `json:"event"`
	StopReason string `json:"stop_reason"`
	Ephemeral  bool   `json:"ephemeral"` // a --no-session session, which cannot be resumed
	Runtime    string `json:"runtime"`   // random per loaded extension; /reload makes a new one
}

// Derive maps one hook payload to the pane's next activity. ok is false when
// the event changes nothing. ok is true with a zero State when the pane's
// activity should be removed: the session ended (SessionEnd) or a Codex turn
// was interrupted, so the agent sits idle at its prompt.
//
// Event to state, for both providers unless noted:
//
//	UserPromptSubmit, PreToolUse, PostToolUse,
//	PostToolUseFailure (Claude)             working
//	PreToolUse/PermissionRequest of
//	  AskUserQuestion                       awaiting-input, Detail = first question
//	  ExitPlanMode                          plan-ready
//	PermissionRequest (other tools)         pending-approval, Detail = tool description or name
//	Notification permission_prompt          pending-approval, Detail = message,
//	                                        unless the pane already shows a question or plan
//	Notification elicitation_dialog,
//	  elicitation_url_dialog                awaiting-input, Detail = message
//	Notification idle_prompt, others        no change (an idle reminder must not undo completed)
//	Stop                                    completed, Detail = the start of the last message,
//	                                        even when the turn ends on a question;
//	                                        working while a background subagent or workflow runs
//	StopFailure (Claude)                    error, Detail = the API error text
//	SubagentStop                            no change (the main turn is still going)
//	SessionStart                            no change: Claude and Codex fire it when
//	                                        the agent is idle at its prompt, which is no activity
//	                                        (source "compact" fires mid-turn, where working must stay)
//	SessionEnd, Interrupt (Codex)           remove
//	Codex notify agent-turn-complete        completed, Detail = the start of the last message
//
// pi, from the extension PiExtension writes:
//
//	before_agent_start, agent_start         working
//	tool_call                               working, Detail = tool name
//	agent_settled                           completed, Detail = start of the last reply;
//	                                        error when the run failed, Detail = the error,
//	                                        both redacted like Stop's;
//	                                        remove when the user aborted it
//	session_start                           no change (pi is idle at its prompt)
//	session_shutdown                        remove, unless prev belongs to another pi
//	                                        session: a late report from one pi ended
//	                                        by /new, /resume or /reload
//
// pi has no permission prompts of its own, so it never reports
// pending-approval, awaiting-input or plan-ready.
//
// Tool failures stay working: an agent recovers from a failed command inside
// the same turn, so error is reserved for turns the API ended. A Claude turn
// the user interrupts fires no hook; the daemon ends it from the keys it
// forwards and ReadScreen.
func Derive(prev *model.Activity, provider model.Provider, payload []byte, now time.Time) (next model.Activity, ok bool) {
	p, err := decode(payload)
	if err != nil {
		return model.Activity{}, false
	}
	state, detail, remove, ok := mapEvent(p, prev)
	if !ok {
		return model.Activity{}, false
	}
	if remove {
		stale := p.PiEvent != "" && prev != nil && p.SessionID != "" && prev.SessionID != "" && p.SessionID != prev.SessionID
		return model.Activity{}, prev != nil && !stale
	}
	sid := sessionID(p)
	if prev != nil {
		if prev.State == state && prev.Detail == detail && (sid == "" || sid == prev.SessionID) {
			return model.Activity{}, false
		}
		next = *prev
	}
	next.Provider = provider
	if sid != "" {
		next.SessionID = sid
	}
	next.State = state
	next.Detail = detail
	next.UpdatedAt = now
	return next, true
}

func mapEvent(p payload, prev *model.Activity) (state model.AgentState, detail string, remove, ok bool) {
	if p.PiEvent != "" {
		return mapPi(p)
	}
	if p.Event == "" {
		// Codex notify program: agent-turn-complete is its only type.
		return model.StateCompleted, summary(p.LastMessageDash), false, p.Type == "agent-turn-complete"
	}
	if sideFork(p) {
		return "", "", false, false
	}
	switch p.Event {
	case "UserPromptSubmit", "PostToolUse", "PostToolUseFailure":
		return model.StateWorking, "", false, true
	case "PreToolUse", "PermissionRequest":
		switch p.ToolName {
		case "AskUserQuestion":
			q := "question"
			if len(p.ToolInput.Questions) > 0 && p.ToolInput.Questions[0].Question != "" {
				q = p.ToolInput.Questions[0].Question
			}
			return model.StateAwaitingInput, q, false, true
		case "ExitPlanMode":
			return model.StatePlanReady, "", false, true
		}
		if p.Event == "PreToolUse" {
			return model.StateWorking, "", false, true
		}
		d := p.ToolInput.Description
		if d == "" {
			d = p.ToolName
		}
		return model.StatePendingApproval, d, false, true
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt":
			if prev != nil && (prev.State == model.StateAwaitingInput || prev.State == model.StatePlanReady) {
				return "", "", false, false
			}
			return model.StatePendingApproval, p.Message, false, true
		case "elicitation_dialog", "elicitation_url_dialog":
			return model.StateAwaitingInput, p.Message, false, true
		}
		return "", "", false, false
	case "Stop":
		for _, t := range p.BackgroundTasks {
			if (t.Type == "subagent" || t.Type == "workflow") && t.Status == "running" {
				return model.StateWorking, "", false, true
			}
		}
		return model.StateCompleted, summary(p.LastMessage), false, true
	case "StopFailure":
		d := firstNonEmpty(p.LastMessage, p.ErrorDetails, p.Error)
		return model.StateError, d, false, true
	case "SessionEnd", "Interrupt":
		return "", "", true, true
	}
	return "", "", false, false
}

// summaryLen is how much of a turn's last message a completed activity
// keeps as its Detail.
const summaryLen = 300

// summary is the start of msg, its secrets redacted first, with
// whitespace collapsed, cut between words so no token is split in half.
// It is shown in the sidebar and in desktop notifications.
func summary(msg string) string { return Summary(msg) }

// Summary is msg as a completed activity's Detail: secrets found by
// decide's patterns, and each of known, are redacted in the whole text
// before it is cut to summaryLen runes.
func Summary(msg string, known ...string) string {
	r := []rune(strings.Join(strings.Fields(decide.Redact(msg, known...)), " "))
	if len(r) <= summaryLen {
		return string(r)
	}
	s := string(r[:summaryLen-1])
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		s = s[:i]
	} else {
		s = "" // one long token: none of it
	}
	return s + "…"
}

func mapPi(p payload) (state model.AgentState, detail string, remove, ok bool) {
	switch p.PiEvent {
	case "before_agent_start", "agent_start":
		return model.StateWorking, "", false, true
	case "tool_call":
		return model.StateWorking, p.ToolName, false, true
	case "agent_settled":
		switch p.StopReason {
		case "error":
			return model.StateError, summary(firstNonEmpty(p.Error, p.Message)), false, true
		case "aborted":
			return "", "", true, true
		}
		return model.StateCompleted, summary(p.Message), false, true
	case "session_shutdown":
		return "", "", true, true
	}
	return "", "", false, false
}

func decode(b []byte) (payload, error) {
	var p payload
	err := json.Unmarshal(b, &p)
	return p, err
}

// sideFork reports a hook from Codex's /side fork, which runs with a null
// transcript; it is not the pane's main session.
func sideFork(p payload) bool { return string(p.TranscriptPath) == "null" }

func sessionID(p payload) string {
	if p.Event == "" && p.PiEvent == "" {
		return p.ThreadID
	}
	return p.SessionID
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// SessionID returns the agent session id carried by the payload, or "".
// Claude and Codex hooks and pi's events carry session_id (a subagent's
// hooks carry the parent's); Codex notify carries thread-id. The provider is
// not needed to tell them apart. A pi session that cannot be resumed, and
// pi's shutdown report, which may arrive after the next session started,
// return "". A /side fork's hook returns "": resuming it would lose
// the main session.
func SessionID(provider model.Provider, payload []byte) string {
	p, err := decode(payload)
	if err != nil || sideFork(p) || p.Ephemeral || p.PiEvent == "session_shutdown" {
		return ""
	}
	return sessionID(p)
}

// PermissionMode returns the permission mode a Claude Code or Codex hook
// reports, both under these names, or "" for any other value. A /side
// fork's mode returns "": it is not the pane's main session.
func PermissionMode(payload []byte) string {
	p, err := decode(payload)
	if err != nil || sideFork(p) {
		return ""
	}
	switch p.PermissionMode {
	case "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions":
		return p.PermissionMode
	}
	return ""
}

// Prompt returns the prompt text of a UserPromptSubmit hook or a pi
// before_agent_start event, or "". All three carry it in "prompt". A /side
// fork's prompt returns "": it is not the pane's main session.
func Prompt(provider model.Provider, payload []byte) string {
	p, err := decode(payload)
	if err != nil || (p.Event != "UserPromptSubmit" && p.PiEvent != "before_agent_start") || sideFork(p) {
		return ""
	}
	// why: a slash command (/clear, /model) says nothing about the work, so
	// the first real prompt names the tab instead.
	if strings.HasPrefix(strings.TrimSpace(p.Prompt), "/") {
		return ""
	}
	return p.Prompt
}

// claudeEvents are the Claude Code events Derive acts on. Notification is
// limited to the types that change state, so idle reminders never spawn the
// hook at all.
var claudeEvents = []hookEvent{
	{name: "UserPromptSubmit"},
	{name: "PreToolUse"},
	{name: "PostToolUse"},
	{name: "PostToolUseFailure"},
	{name: "PermissionRequest"},
	{name: "Notification", matcher: "permission_prompt|elicitation_dialog|elicitation_url_dialog"},
	{name: "Stop"},
	{name: "StopFailure"},
	{name: "SessionEnd"},
}

// codexEvents are the Codex hook events Derive acts on.
var codexEvents = []hookEvent{
	{name: "UserPromptSubmit"},
	{name: "PreToolUse"},
	{name: "PostToolUse"},
	{name: "PermissionRequest"},
	{name: "Stop"},
	{name: "Interrupt"},
	{name: "SessionEnd"},
}

type hookEvent struct{ name, matcher string }

// ClaudeHooks returns the "hooks" object for ~/.claude/settings.json that
// runs `<bin> hook claude` on every event Derive reads. The command no-ops
// when PITWALL_PANE is unset, so registering it globally is harmless for
// Claude sessions started outside pitwall.
func ClaudeHooks(bin string) []byte { return hooksJSON(bin, "claude", claudeEvents) }

// CodexHooks returns the "hooks" object for ~/.codex/hooks.json that runs
// `<bin> hook codex` on every Codex hook event Derive reads. Codex runs a
// hook only after the user trusts it once with /hooks. Like ClaudeHooks, the
// command no-ops outside a pitwall pane.
func CodexHooks(bin string) []byte { return hooksJSON(bin, "codex", codexEvents) }

// CodexNotify returns the `notify = [...]` line for ~/.codex/config.toml.
// Notify reports only finished turns; CodexHooks covers every state. The
// command no-ops outside a pitwall pane.
func CodexNotify(bin string) string {
	return "notify = [" + strconv.Quote(bin) + `, "hook", "codex"]`
}

//go:embed pi_extension.ts
var piTemplate string

const piBinToken = "__PITWALL_BIN__"

// PiExtension returns the TypeScript extension pi loads from
// <agent dir>/extensions/pitwall.ts. Inside a pitwall pane, on each event it
// runs `<bin> hook pi` with a JSON payload on stdin, without a shell, one at
// a time in the background; pi waits for it only at shutdown, at most 1s.
// Outside a pane it registers nothing. bin is quoted as a JSON string, which is a
// valid TypeScript string literal for any path.
func PiExtension(bin string) []byte {
	q, _ := json.Marshal(bin)
	return []byte(strings.Replace(piTemplate, piBinToken, string(q), 1))
}

// piReleased are the SHA-256 sums of pi_extension.ts as earlier releases
// shipped it, so an install replaces those files as unedited.
var piReleased = []string{
	"6075137b80ebb0c738c88c2abca9f192e9fc3858eab4e215f4702bceb3bcca02", // v0.1.0-alpha.5
}

// IsPiExtension reports whether b is exactly what PiExtension returns for
// some binary path, now or in an earlier release: a file pitwall wrote and
// nobody edited since.
func IsPiExtension(b []byte) bool {
	// why: JSON escapes newlines, so the bin literal ends at the first ";\n".
	pre, rest, ok := strings.Cut(string(b), "const bin = ")
	lit, post, ok2 := strings.Cut(rest, ";\n")
	var bin string
	if !ok || !ok2 || json.Unmarshal([]byte(lit), &bin) != nil {
		return false
	}
	if q, _ := json.Marshal(bin); string(q) != lit {
		return false
	}
	tmpl := pre + "const bin = " + piBinToken + ";\n" + post
	sum := sha256.Sum256([]byte(tmpl))
	return tmpl == piTemplate || slices.Contains(piReleased, hex.EncodeToString(sum[:]))
}

func hooksJSON(bin, provider string, events []hookEvent) []byte {
	type handler struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string    `json:"matcher,omitempty"`
		Hooks   []handler `json:"hooks"`
	}
	// Hooks run synchronously so events reach pitwall in order; the short
	// timeout keeps a stuck daemon from stalling the agent.
	cmd := commandPath(runtime.GOOS, bin) + " hook " + provider
	out := map[string][]group{}
	for _, e := range events {
		out[e.name] = []group{{Matcher: e.matcher, Hooks: []handler{{Type: "command", Command: cmd, Timeout: 5}}}}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

// commandPath quotes bin for the shell an agent runs its hooks in: sh on
// Unix. On Windows that is Git Bash for Claude Code and may be cmd or
// PowerShell elsewhere, so the path gets forward slashes, which all three
// take, and double quotes only when it needs them, which PowerShell would
// read as a string instead of a command.
func commandPath(goos, bin string) string {
	if goos != "windows" {
		return "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
	}
	bin = strings.ReplaceAll(bin, `\`, "/")
	if strings.ContainsAny(bin, " &()[]{}^=;!'+,`~$%@#") {
		return `"` + bin + `"`
	}
	return bin
}

// Request is the tool call a PreToolUse or PermissionRequest hook is
// about: the event, the tool's name and raw input, and the agent's cwd.
// ok is false for other payloads and for a /side fork.
func Request(payload []byte) (event, tool string, input json.RawMessage, cwd string, ok bool) {
	var p struct {
		Event          string          `json:"hook_event_name"`
		TranscriptPath json.RawMessage `json:"transcript_path"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		Cwd            string          `json:"cwd"`
	}
	if json.Unmarshal(payload, &p) != nil || string(p.TranscriptPath) == "null" || p.ToolName == "" {
		return "", "", nil, "", false
	}
	if p.Event != "PreToolUse" && p.Event != "PermissionRequest" {
		return "", "", nil, "", false
	}
	return p.Event, p.ToolName, p.ToolInput, p.Cwd, true
}

// PiRuntime returns the runtime nonce of a pi extension report, "" for
// other payloads, and whether the report is a session_start, which every
// runtime sends first. A pi extension loaded again by /reload keeps the
// session id but not the nonce, so the old one's late reports can be told
// from the new one's.
func PiRuntime(payload []byte) (runtime string, start bool) {
	p, err := decode(payload)
	if err != nil || p.PiEvent == "" {
		return "", false
	}
	return p.Runtime, p.PiEvent == "session_start"
}

// UserPrompt is the prompt of any UserPromptSubmit hook from the main
// session, or of pi's before_agent_start, or "".
func UserPrompt(payload []byte) string {
	p, err := decode(payload)
	if err != nil || (p.Event != "UserPromptSubmit" && p.PiEvent != "before_agent_start") || sideFork(p) {
		return ""
	}
	return p.Prompt
}

// LastMessage is the agent's final message of a finished turn: Stop's
// last_assistant_message, Codex notify's last-assistant-message, or the
// message of pi's agent_settled (whole, up to 16000 runes).
func LastMessage(payload []byte) string {
	p, err := decode(payload)
	if err != nil {
		return ""
	}
	if p.PiEvent == "agent_settled" {
		return p.Message
	}
	return firstNonEmpty(p.LastMessage, p.LastMessageDash)
}

// NeedsInteraction reports tools whose permission request is really a
// question for the user (AskUserQuestion) or a plan to approve
// (ExitPlanMode); no model may answer those for the user.
func NeedsInteraction(tool string) bool {
	return tool == "AskUserQuestion" || tool == "ExitPlanMode"
}
