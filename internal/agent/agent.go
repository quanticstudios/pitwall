// Package agent maps coding-agent hook events to model.Activity.
//
// Claude Code and Codex hooks send the same JSON shape on stdin
// (hook_event_name, session_id, cwd, tool_name, tool_input, ...), so one
// decoder serves both. Codex's older notify program sends a different object
// as its one argv argument; Derive tells the two apart by hook_event_name.
package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// payload holds the fields Derive reads from either agent.
type payload struct {
	Event          string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TranscriptPath json.RawMessage `json:"transcript_path"`
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
	BackgroundTasks  []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	} `json:"background_tasks"`

	// Codex legacy notify fields.
	Type     string `json:"type"`
	ThreadID string `json:"thread-id"`
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
//	Stop                                    completed, even when the turn ends on a question;
//	                                        working while a background subagent or workflow runs
//	StopFailure (Claude)                    error, Detail = the API error text
//	SubagentStop                            no change (the main turn is still going)
//	SessionStart                            no change: Claude and Codex fire it when
//	                                        the agent is idle at its prompt, which is no activity
//	                                        (source "compact" fires mid-turn, where working must stay)
//	SessionEnd, Interrupt (Codex)           remove
//	Codex notify agent-turn-complete        completed
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
		return model.Activity{}, prev != nil
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
	if p.Event == "" {
		// Codex notify program: agent-turn-complete is its only type.
		return model.StateCompleted, "", false, p.Type == "agent-turn-complete"
	}
	// Codex's /side fork runs hooks with a null transcript; it is not the
	// pane's main turn.
	if string(p.TranscriptPath) == "null" {
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
		return model.StateCompleted, "", false, true
	case "StopFailure":
		d := firstNonEmpty(p.LastMessage, p.ErrorDetails, p.Error)
		return model.StateError, d, false, true
	case "SessionEnd", "Interrupt":
		return "", "", true, true
	}
	return "", "", false, false
}

func decode(b []byte) (payload, error) {
	var p payload
	err := json.Unmarshal(b, &p)
	return p, err
}

func sessionID(p payload) string {
	if p.Event == "" {
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
// Claude and Codex hooks carry session_id (a subagent's hooks carry the
// parent's); Codex notify carries thread-id. The provider is not needed to
// tell them apart.
func SessionID(provider model.Provider, payload []byte) string {
	p, err := decode(payload)
	if err != nil {
		return ""
	}
	return sessionID(p)
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
	cmd := shellQuote(bin) + " hook " + provider
	out := map[string][]group{}
	for _, e := range events {
		out[e.name] = []group{{Matcher: e.matcher, Hooks: []handler{{Type: "command", Command: cmd, Timeout: 5}}}}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
