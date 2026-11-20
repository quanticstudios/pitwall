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

// chatOn reports whether starting agent p opens a desktop chat: [chat]
// claude or antigravity, on this machine, with a daemon that runs chats.
func (u *ui) chatOn(p model.Provider) bool {
	switch p {
	case model.ProviderClaude:
		return u.chatClaude()
	case model.ProviderAntigravity:
		return u.cfg.Chat.Antigravity && curHost() == "" && linkLevel(u.b) >= proto.AntigravityLevel
	}
	return false
}

// chatTask is the ChatNew that starts task t as a Claude Code or
// Antigravity chat, false when it has to run as a TUI: another agent, a
// preset's variables or guard, an account profile, a fresh worktree, or
// flags a chat does not take. agy's prompt follows -i, as taskCmd puts it.
func chatTask(t model.Task) (proto.ChatNew, bool) {
	if len(t.Cmd) < 2 || len(t.Env) > 0 || t.Profile != "" || t.FreshWorktree {
		return proto.ChatNew{}, false
	}
	base := strings.TrimSuffix(filepath.Base(t.Cmd[0]), ".exe")
	args, modeFlag, prov := t.Cmd[1:len(t.Cmd)-1], "--permission-mode", model.ProviderClaude
	switch base {
	case "claude":
	case "agy":
		if len(args) == 0 || args[len(args)-1] != "-i" {
			return proto.ChatNew{}, false
		}
		args, modeFlag, prov = args[:len(args)-1], "--mode", model.ProviderAntigravity
	default:
		return proto.ChatNew{}, false
	}
	mdl, effort, rest := config.SplitArgs(base, args)
	mode := ""
	if len(rest) == 2 && rest[0] == modeFlag {
		mode, rest = rest[1], nil
	}
	if len(rest) > 0 {
		return proto.ChatNew{}, false
	}
	if prov == model.ProviderAntigravity && effort == "" {
		// why: agy's CLI names a model with its effort ("gemini-3.8-flash-low"); its ACP model option names the model alone.
		for _, e := range config.Efforts(base) {
			if m, ok := strings.CutSuffix(mdl, "-"+e); ok && m != "" {
				mdl, effort = m, e
				break
			}
		}
	}
	return proto.ChatNew{SessionID: t.SessionID, GroupID: t.GroupID, Dir: t.Dir, Worktree: t.Worktree, Base: t.Base, Branch: t.Branch,
		Provider: prov, Model: mdl, Effort: effort, Mode: mode, Prompt: t.Cmd[len(t.Cmd)-1]}, true
}

// withTerminal adds "<agent> (terminal)" after the agent cmd runs in
// agents, the explicit way to the TUI once that agent opens a chat.
func withTerminal(agents []foundAgent, cmd string) []foundAgent {
	for i, a := range agents {
		if a.cmd == cmd && !a.preset && !a.terminal {
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
