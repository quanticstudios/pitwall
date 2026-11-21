package config

import "fmt"

// Chat is [chat]: how pitwall starts Claude Code, and how a chat tab's
// transcript draws.
type Chat struct {
	Claude      string `toml:"claude" enum:"chat,terminal" doc:"chat starts Claude Code from the New task dialog, its presets and the welcome card as a desktop chat on the same folder or worktree, and shows every pane running Claude Code's TUI, however it started, as that chat, with the TUI one key away (chat_terminal); terminal runs the TUI in a pane and shows such panes as the TUI, still one key from the chat. Claude (terminal) in the New task dialog and the CLI's pitwall new always start the TUI"`
	Codex       string `toml:"codex" enum:"chat,terminal" doc:"chat shows every pane running Codex's TUI, however it started, as a desktop chat once Codex's hooks name its session, with the TUI one key away (chat_terminal); terminal shows such panes as the TUI, still one key from the chat"`
	Antigravity string `toml:"antigravity" enum:"chat,terminal" doc:"chat starts Antigravity (agy) from the New task dialog and its presets as a desktop chat over agy acp, with Antigravity (terminal) beside it for the TUI, and shows every pane running agy's TUI as a chat once its hooks name the conversation; terminal runs the TUI in a pane and shows such panes as the TUI. pitwall new always starts the TUI"`
	Width       string `toml:"width" enum:"comfortable,standard,full" doc:"The transcript's and the composer's column: comfortable is 736 dp, standard 960 dp (fits a 120-column diff), full uses the whole tab. Reply prose stops near 96 characters either way"`
	FollowUp    string `toml:"follow_up" enum:"queue,steer" doc:"What the send key does while the agent works: queue holds the message until the turn ends; steer sends it into the running turn, where the agent takes it (a slash command still queues). Ctrl+Enter does the other one, Cmd+Enter on macOS"`
	SendKey     string `toml:"send_key" enum:"enter,ctrl+enter" doc:"The composer's send key: enter sends with Enter, Shift+Enter adds a line; ctrl+enter sends with Ctrl+Enter (Cmd+Enter on macOS), Enter adds a line, and Ctrl+Shift+Enter does the other follow-up"`
	ToolCalls   string `toml:"tool_calls" enum:"all,failed,none,zen" doc:"Which tool calls the desktop chat shows: all; failed hides a call once it succeeds; none hides every call; zen shows only your messages and each turn's final reply. Errors, approvals and questions always show. The chat header's Tools button cycles it"`
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
	Width       int // the column's widest, in dp; 0 is the whole tab
	// Steer is true when the send key steers a working agent, false when
	// it queues.
	Steer bool
	// CtrlEnter is true when Ctrl+Enter sends and Enter adds a line.
	CtrlEnter bool
	ToolCalls string // one of ToolModes
}

func defaultChat() Chat {
	return Chat{Claude: "chat", Codex: "chat", Antigravity: "chat", Width: "standard", FollowUp: "queue", SendKey: "enter", ToolCalls: ToolsAll}
}

// resolveChat fills in [chat]'s values from c.
func resolveChat(c Chat) (ChatSettings, []issue) {
	var out ChatSettings
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
	switch c.FollowUp {
	case "", "queue":
	case "steer":
		out.Steer = true
	default:
		issues = append(issues, issue{"chat.follow_up", fmt.Sprintf("%q is not queue or steer; using queue", c.FollowUp)})
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
