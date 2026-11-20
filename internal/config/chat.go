package config

import "fmt"

// Chat is [chat]: how pitwall starts Claude Code, and how a chat tab's
// transcript draws.
type Chat struct {
	Claude      string `toml:"claude" enum:"chat,terminal" doc:"chat starts Claude Code from the New task dialog, its presets and the welcome card as a desktop chat on the same folder or worktree, and shows every pane running Claude Code's TUI, however it started, as that chat, with the TUI one key away (chat_terminal); terminal runs the TUI in a pane and shows such panes as the TUI, still one key from the chat. Claude (terminal) in the New task dialog and the CLI's pitwall new always start the TUI"`
	Codex       string `toml:"codex" enum:"chat,terminal" doc:"chat shows every pane running Codex's TUI, however it started, as a desktop chat once Codex's hooks name its session, with the TUI one key away (chat_terminal); terminal shows such panes as the TUI, still one key from the chat"`
	Antigravity string `toml:"antigravity" enum:"chat,terminal" doc:"chat starts Antigravity (agy) from the New task dialog and its presets as a desktop chat over agy acp, with Antigravity (terminal) beside it for the TUI, and shows every pane running agy's TUI as a chat once its hooks name the conversation; terminal runs the TUI in a pane and shows such panes as the TUI. pitwall new always starts the TUI"`
	Width       string `toml:"width" enum:"comfortable,standard,full" doc:"The transcript's column: comfortable is 736 dp, standard 960 dp (fits a 120-column diff), full uses the whole tab. Reply prose stops near 96 characters either way"`
}

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
}

func defaultChat() Chat {
	return Chat{Claude: "chat", Codex: "chat", Antigravity: "chat", Width: "standard"}
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
	return out, issues
}
