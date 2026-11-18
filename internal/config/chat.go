package config

import "fmt"

// Chat is [chat]: how pitwall starts Claude Code.
type Chat struct {
	Claude string `toml:"claude" enum:"chat,terminal" doc:"chat starts Claude Code from the New task dialog, its presets and the welcome card as a desktop chat on the same folder or worktree, with its own TUI one key away (chat_terminal); terminal runs the TUI in a pane, as before. Claude (terminal) in the New task dialog and the CLI's pitwall new always run the TUI. Agents already running are not touched"`
}

// ChatSettings is [chat] resolved.
type ChatSettings struct {
	// Claude is true when starting Claude Code opens a chat, not the TUI.
	Claude bool
}

func defaultChat() Chat { return Chat{Claude: "chat"} }

func resolveChat(c Chat) (ChatSettings, []issue) {
	switch c.Claude {
	case "", "chat":
		return ChatSettings{Claude: true}, nil
	case "terminal":
		return ChatSettings{}, nil
	}
	return ChatSettings{Claude: true}, []issue{{"chat.claude", fmt.Sprintf("%q is not chat or terminal; using chat", c.Claude)}}
}
