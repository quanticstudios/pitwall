package config

import "fmt"

// Chat is [chat]: how pitwall starts Claude Code, and how a chat tab's
// transcript draws.
type Chat struct {
	Claude      string `toml:"claude" enum:"chat,terminal" doc:"chat starts Claude Code from the New task dialog, its presets and the welcome card as a desktop chat on the same folder or worktree, and shows every pane running Claude Code's TUI, however it started, as that chat, with the TUI one key away (chat_terminal); terminal runs the TUI in a pane and shows such panes as the TUI, still one key from the chat. Claude (terminal) in the New task dialog and the CLI's pitwall new always start the TUI"`
	Codex       string `toml:"codex" enum:"chat,terminal" doc:"chat shows every pane running Codex's TUI, however it started, as a desktop chat once Codex's hooks name its session, with the TUI one key away (chat_terminal); terminal shows such panes as the TUI, still one key from the chat"`
	Antigravity string `toml:"antigravity" enum:"chat,terminal" doc:"chat starts Antigravity (agy) from the New task dialog and its presets as a desktop chat over agy acp, with Antigravity (terminal) beside it for the TUI, and shows every pane running agy's TUI as a chat once its hooks name the conversation; terminal runs the TUI in a pane and shows such panes as the TUI. pitwall new always starts the TUI"`
	Width       string `toml:"width" enum:"comfortable,standard,full" doc:"The transcript's and the composer's column: comfortable is 736 dp, standard 960 dp (fits a 120-column diff), full uses the whole tab. Reply prose stops near 96 characters either way"`
	Layout      string `toml:"layout" enum:"column,ledger" doc:"The desktop chat's layout: column (the default) puts replies in a reading column with folded work logs; ledger adds a time and agent gutter beside a 768 dp text column, shows each tool call as its own row, puts the plan in a strip under the header, and docks a floating composer card"`
	FollowUp    string `toml:"follow_up" enum:"queue,steer" doc:"What the send key does while the agent works: steer (the default) sends it into the running turn, where the agent takes it at its next step as it does a message typed in its terminal; queue holds it until the turn ends. A slash command always queues. Ctrl+Enter does the other one, Cmd+Enter on macOS"`
	SendKey     string `toml:"send_key" enum:"enter,ctrl+enter" doc:"The composer's send key: enter sends with Enter, Shift+Enter adds a line; ctrl+enter sends with Ctrl+Enter (Cmd+Enter on macOS), Enter adds a line, and Ctrl+Shift+Enter does the other follow-up"`
	ToolCalls   string `toml:"tool_calls" enum:"all,failed,none,zen" doc:"Which tool calls the desktop chat shows: all; failed hides a call once it succeeds; none hides every call; zen shows only your messages and each turn's final reply. Errors, approvals and questions always show. The chat header's Tools button cycles it"`
	SuggestNext bool   `toml:"suggest_next" doc:"After a turn ends, an empty composer shows a predicted next message in muted text; Tab fills it in. Each suggestion is one extra headless run of the chat's agent on its cheapest model, billed to the agent's plan, so it is off by default. Never while the agent works, at most once per turn, never in a guarded tab"`
}

// Tool call visibility, [chat] tool_calls.
const (
	ToolsAll    = "all"
	ToolsFailed = "failed"
	ToolsNone   = "none"
	ToolsZen    = "zen"
)

// ToolModes are [chat] tool_calls' values, in the order the header cycles them.
var ToolModes = []string{ToolsAll, ToolsFailed, ToolsNone, ToolsZen}

// Chat column widths in dp.
const (
	ChatComfortable = 736
	ChatStandard    = 960
)

// Chat layouts, [chat] layout.
const (
	ChatColumn = "column"
	ChatLedger = "ledger"
)

// ChatSettings is [chat] resolved.
type ChatSettings struct {
	// Claude is true when starting Claude Code opens a chat, not the TUI,
	// and a pane running Claude Code's TUI shows as a chat.
	Claude bool
	// Codex is true when a pane running Codex's TUI shows as a chat.
	Codex bool
	// Antigravity is true when starting agy from the New task dialog opens
	// a chat over ACP, not the TUI.
	Antigravity bool
	Width       int    // the column's widest, in dp; 0 is the whole tab
	Layout      string // ChatColumn or ChatLedger
	// Steer is true when the send key steers a working agent, false when
	// it queues.
	Steer bool
	// CtrlEnter is true when Ctrl+Enter sends and Enter adds a line.
	CtrlEnter bool
	ToolCalls string // one of ToolModes
	// SuggestNext shows a predicted next prompt in an empty composer after
	// a turn ends.
	SuggestNext bool
}

func defaultChat() Chat {
	return Chat{Claude: "chat", Codex: "chat", Antigravity: "chat", Width: "standard", Layout: ChatColumn, FollowUp: "steer", SendKey: "enter", ToolCalls: ToolsAll}
}

// resolveChat fills in [chat]'s values from c.
func resolveChat(c Chat) (ChatSettings, []issue) {
	out := ChatSettings{SuggestNext: c.SuggestNext}
	var issues []issue
	switch c.Claude {
	case "", "chat":
		out.Claude = true
	case "terminal":
	default:
		out.Claude = true
		issues = append(issues, issue{"chat.claude", fmt.Sprintf("%q is not chat or terminal; using chat", c.Claude)})
	}
	switch c.Codex {
	case "", "chat":
		out.Codex = true
	case "terminal":
	default:
		out.Codex = true
		issues = append(issues, issue{"chat.codex", fmt.Sprintf("%q is not chat or terminal; using chat", c.Codex)})
	}
	switch c.Antigravity {
	case "", "chat":
		out.Antigravity = true
	case "terminal":
	default:
		out.Antigravity = true
		issues = append(issues, issue{"chat.antigravity", fmt.Sprintf("%q is not chat or terminal; using chat", c.Antigravity)})
	}
	switch c.Width {
	case "", "standard":
		out.Width = ChatStandard
	case "comfortable":
		out.Width = ChatComfortable
	case "full":
	default:
		out.Width = ChatStandard
		issues = append(issues, issue{"chat.width", fmt.Sprintf("%q is not comfortable, standard or full; using standard", c.Width)})
	}
	switch c.Layout {
	case "", ChatColumn:
		out.Layout = ChatColumn
	case ChatLedger:
		out.Layout = ChatLedger
	default:
		out.Layout = ChatColumn
		issues = append(issues, issue{"chat.layout", fmt.Sprintf("%q is not column or ledger; using column", c.Layout)})
	}
	switch c.FollowUp {
	case "queue":
	case "", "steer":
		out.Steer = true // why: the agent takes a message mid-turn, as it does when typed in its terminal
	default:
		out.Steer = true
		issues = append(issues, issue{"chat.follow_up", fmt.Sprintf("%q is not queue or steer; using steer", c.FollowUp)})
	}
	switch c.SendKey {
	case "", "enter":
	case "ctrl+enter":
		out.CtrlEnter = true
	default:
		issues = append(issues, issue{"chat.send_key", fmt.Sprintf("%q is not enter or ctrl+enter; using enter", c.SendKey)})
	}
	switch c.ToolCalls {
	case "":
		out.ToolCalls = ToolsAll
	case ToolsAll, ToolsFailed, ToolsNone, ToolsZen:
		out.ToolCalls = c.ToolCalls
	default:
		out.ToolCalls = ToolsAll
		issues = append(issues, issue{"chat.tool_calls", fmt.Sprintf("%q is not all, failed, none or zen; using all", c.ToolCalls)})
	}
	return out, issues
}
