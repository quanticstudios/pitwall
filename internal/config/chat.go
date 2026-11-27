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
	FollowUp    string `toml:"follow_up" enum:"queue,steer" doc:"What the send key does while the agent works: queue (the default) holds the message above the box until the turn ends, and Ctrl+Enter (Cmd+Enter on macOS) steers instead: it sends the message into the running turn with everything queued ahead of it, as one message, where the agent takes it at its next step as it does a message typed in its terminal. steer swaps the two keys. A slash command never steers on the send key. An agent that takes nothing mid-turn queues either way"`
	SendKey     string `toml:"send_key" enum:"enter,ctrl+enter" doc:"The composer's send key: enter sends with Enter, Shift+Enter adds a line; ctrl+enter sends with Ctrl+Enter (Cmd+Enter on macOS), Enter adds a line, and Ctrl+Shift+Enter does the other follow-up"`
	ToolCalls   string `toml:"tool_calls" enum:"all,failed,none,zen,headlines" doc:"Which tool calls the desktop chat shows: all; failed hides a call once it succeeds; none hides every call; zen shows your messages, each turn's final reply and the agent's longer reports between calls; headlines shows each finished turn as one line from the agent's cheapest model (see away_summary), and a click opens it. Errors, approvals and questions always show. The chat header's Tools button cycles it"`
	SuggestNext bool   `toml:"suggest_next" doc:"After a turn ends, an empty composer shows a predicted next message in muted text; Tab fills it in. Each suggestion is one extra headless run of the chat's agent on its cheapest model, billed to the agent's plan, so it is off by default. Never while the agent works, at most once per turn, never in a guarded tab"`
	InboxDock   string `toml:"inbox_dock" enum:"open,folded" doc:"The inbox dock under the chat, which lists what waits on you in your other tabs and machines with its answers as buttons: open lists them, folded shows one line with the counts. A click on the dock's header switches it and saves the choice here"`
	AwaySummary string `toml:"away_summary" enum:"on,off" doc:"When you come back to a chat after 5 minutes or more and its agent worked meanwhile, a Since you left card above the box says how long you were away and what the log shows. on (the default) adds two lines from the chat's agent on its cheapest model: the state of the work and what it needs from you. It sends that model your last message, its first 1,500 characters, and the agent's replies since you left, their last 6,000, with secrets redacted, once per return. off shows the facts alone and calls no model"`
}

// Tool call visibility, [chat] tool_calls.
const (
	ToolsAll    = "all"
	ToolsFailed = "failed"
	ToolsNone   = "none"
	ToolsZen    = "zen"
	// ToolsHeadlines folds each finished turn to a one-line headline.
	ToolsHeadlines = "headlines"
)

// ToolModes are [chat] tool_calls' values, in the order the header lists them.
var ToolModes = []string{ToolsAll, ToolsFailed, ToolsNone, ToolsZen, ToolsHeadlines}

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
	// Steer is true when the send key steers a working agent and
	// Ctrl+Enter queues, false (the default) when the send key queues and
	// Ctrl+Enter steers.
	Steer bool
	// CtrlEnter is true when Ctrl+Enter sends and Enter adds a line.
	CtrlEnter bool
	ToolCalls string // one of ToolModes
	// SuggestNext shows a predicted next prompt in an empty composer after
	// a turn ends.
	SuggestNext bool
	// AwaySummary adds the model's two lines to a Since you left card.
	AwaySummary bool
	// DockFolded folds the inbox dock to its header.
	DockFolded bool
}

func defaultChat() Chat {
	return Chat{Claude: "chat", Codex: "chat", Antigravity: "chat", Width: "standard", Layout: ChatColumn, FollowUp: "queue", SendKey: "enter", ToolCalls: ToolsAll, InboxDock: "open", AwaySummary: "on"}
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
	case "", "queue": // why: a queued message shows above the box at once, where a steer mid-turn showed nothing until the agent took it
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
	case ToolsAll, ToolsFailed, ToolsNone, ToolsZen, ToolsHeadlines:
		out.ToolCalls = c.ToolCalls
	default:
		out.ToolCalls = ToolsAll
		issues = append(issues, issue{"chat.tool_calls", fmt.Sprintf("%q is not all, failed, none, zen or headlines; using all", c.ToolCalls)})
	}
	switch c.InboxDock {
	case "", "open":
	case "folded":
		out.DockFolded = true
	default:
		issues = append(issues, issue{"chat.inbox_dock", fmt.Sprintf("%q is not open or folded; using open", c.InboxDock)})
	}
	switch c.AwaySummary {
	case "", "on":
		out.AwaySummary = true
	case "off":
	default:
		out.AwaySummary = true
		issues = append(issues, issue{"chat.away_summary", fmt.Sprintf("%q is not on or off; using on", c.AwaySummary)})
	}
	return out, issues
}
