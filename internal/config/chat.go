package config

import "fmt"

// Chat is [chat]: how pitwall starts Claude Code, and how a chat tab's
// transcript draws.
type Chat struct {
	Claude string `toml:"claude" enum:"chat,terminal" doc:"chat starts Claude Code from the New task dialog, its presets and the welcome card as a desktop chat on the same folder or worktree, and shows every pane running Claude Code's TUI, however it started, as that chat, with the TUI one key away (chat_terminal); terminal runs the TUI in a pane and shows such panes as the TUI, still one key from the chat. Claude (terminal) in the New task dialog and the CLI's pitwall new always start the TUI"`
	Width  string `toml:"width" enum:"comfortable,standard,full" doc:"The transcript's column: comfortable is 736 dp, standard 960 dp (fits a 120-column diff), full uses the whole tab. Reply prose stops near 96 characters either way"`
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
	Width  int // the column's widest, in dp; 0 is the whole tab
}

func defaultChat() Chat { return Chat{Claude: "chat", Width: "standard"} }

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
