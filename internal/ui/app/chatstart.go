package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// chatClaude reports whether starting Claude Code opens a desktop chat
// ([chat] claude): on this machine, with a daemon that runs chats.
func (u *ui) chatClaude() bool {
	return u.cfg.Chat.Claude && curHost() == "" && linkLevel(u.b) >= proto.ChatTerminalLevel
}

// chatTask is the ChatNew that starts task t as a Claude chat, false when
// it has to run as a TUI: another agent, a preset's variables or guard,
// an account profile, a fresh worktree, or flags a chat does not take.
func chatTask(t model.Task) (proto.ChatNew, bool) {
	if len(t.Cmd) < 2 || strings.TrimSuffix(filepath.Base(t.Cmd[0]), ".exe") != "claude" || len(t.Env) > 0 || t.Profile != "" || t.FreshWorktree {
		return proto.ChatNew{}, false
	}
	mdl, effort, rest := config.SplitArgs("claude", t.Cmd[1:len(t.Cmd)-1])
	mode := ""
	if len(rest) == 2 && rest[0] == "--permission-mode" {
		mode, rest = rest[1], nil
	}
	if len(rest) > 0 {
		return proto.ChatNew{}, false
	}
	return proto.ChatNew{SessionID: t.SessionID, GroupID: t.GroupID, Dir: t.Dir, Worktree: t.Worktree, Base: t.Base, Branch: t.Branch,
		Provider: model.ProviderClaude, Model: mdl, Effort: effort, Mode: mode, Prompt: t.Cmd[len(t.Cmd)-1]}, true
}

// withTerminalClaude adds "Claude Code (terminal)" after Claude Code in
// agents, the explicit way to the TUI once Claude Code opens a chat.
func withTerminalClaude(agents []foundAgent) []foundAgent {
	for i, a := range agents {
		if a.cmd == "claude" && !a.preset && !a.terminal {
			tui := a
			tui.name, tui.terminal = a.name+" (terminal)", true
			out := append(agents[:i+1:i+1], tui)
			return append(out, agents[i+1:]...)
		}
	}
	return agents
}

// welcomeChat is the ChatNew the welcome card sends for Claude Code: in
// the focused pane's folder, else home.
func welcomeChat(st *model.State, session, focused string) proto.ChatNew {
	dir, _ := os.UserHomeDir()
	if p := findPane(st, focused); p != nil && p.Cwd != "" {
		dir = p.Cwd
	}
	return proto.ChatNew{SessionID: session, Dir: dir, Provider: model.ProviderClaude}
}
