package panel

import (
	"github.com/quanticstudios/pitwall/internal/session"
)

// Tool call visibility, as [chat] tool_calls names it. Adapted from T3
// Code (MIT): packages/contracts/src/settings.ts ToolCallVisibility and
// apps/web/src/components/chat/MessagesTimeline.logic.ts timelineRowEntries.
const (
	ToolsAll    = "all"    // every call
	ToolsFailed = "failed" // a call while it runs, and after only if it failed
	ToolsNone   = "none"   // no calls; thinking still shows
	ToolsZen    = "zen"    // the prompts and each turn's final reply
)

// toolMode is the visibility the rows follow this frame: ToolCalls, all
// when unset, and zen shows the words between calls while the find bar
// is open, since find looks in them, as T3 Code's Find does.
func (t *Transcript) toolMode() string {
	switch t.ToolCalls {
	case ToolsFailed, ToolsNone:
		return t.ToolCalls
	case ToolsZen:
		if t.FindOpen() {
			return ToolsNone
		}
		return ToolsZen
	}
	return ToolsAll
}

// asksUser is whether c asks the user something, a question or a plan,
// which shows whatever the visibility.
func asksUser(c *session.ToolCall) bool {
	switch c.ToolName {
	case "AskUserQuestion", "ExitPlanMode", "request_user_input":
		return true
	}
	return false
}

// callHidden is whether mode hides tool call c: failed hides one that
// completed, none and zen every one. A question, a plan and a subagent
// never hide, as T3 Code hides only commands, edits, searches and tools.
func callHidden(mode string, c *session.ToolCall) bool {
	if c == nil || mode == ToolsAll || asksUser(c) || isSubagent(c) {
		return false
	}
	if mode == ToolsFailed {
		return c.Status == "completed"
	}
	return true
}

// zenShows is whether zen draws item it of a turn's work: a call that
// asks the user, and the lines that are the user's own or an error, never
// the agent's words between calls.
func zenShows(it *session.Item) bool {
	switch it.Kind {
	case session.KindToolCall:
		return it.Tool != nil && asksUser(it.Tool)
	case session.KindSystem:
		switch it.Phase {
		case session.PhaseError, session.PhaseCompact, session.PhaseCommand, session.PhaseOutput:
			return true
		}
	}
	return false
}
