package app

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestChatTask: a Claude task maps to a chat with its model, effort and
// mode; anything a chat cannot carry keeps the TUI.
func TestChatTask(t *testing.T) {
	base := model.Task{SessionID: "s1", GroupID: "g1", Dir: "/src/acme-api", Worktree: "fix-login", Base: "main"}
	with := func(cmd []string, edit func(*model.Task)) model.Task {
		t := base
		t.Cmd = cmd
		if edit != nil {
			edit(&t)
		}
		return t
	}
	c, ok := chatTask(with([]string{"/usr/bin/claude", "--model", "opus", "--effort", "high", "--permission-mode", "plan", "Fix the login"}, nil))
	want := proto.ChatNew{SessionID: "s1", GroupID: "g1", Dir: "/src/acme-api", Worktree: "fix-login", Base: "main",
		Provider: model.ProviderClaude, Model: "opus", Effort: "high", Mode: "plan", Prompt: "Fix the login"}
	if !ok || !reflect.DeepEqual(c, want) {
		t.Fatalf("chat %#v, %v\nwant %#v", c, ok, want)
	}
	for name, task := range map[string]model.Task{
		"codex":   with([]string{"codex", "Fix it"}, nil),
		"env":     with([]string{"claude", "Fix it"}, func(t *model.Task) { t.Env = []string{"PITWALL_GUARD=1"} }),
		"profile": with([]string{"claude", "Fix it"}, func(t *model.Task) { t.Profile = "work" }),
		"flag":    with([]string{"claude", "--add-dir", "/tmp", "Fix it"}, nil),
	} {
		if _, ok := chatTask(task); ok {
			t.Errorf("%s: started as a chat", name)
		}
	}
}

// stubAgents puts stub agent binaries first on PATH, with an empty HOME.
func stubAgents(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, a := range names {
		if err := os.WriteFile(filepath.Join(bin, exeName(a)), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	return bin
}

// TestChatStartTask: with [chat] claude on, the New task dialog starts
// Claude Code as a chat and lists Claude Code (terminal) for the TUI; a
// queued task and the setting off keep the TUI.
func TestChatStartTask(t *testing.T) {
	bin := stubAgents(t, "claude", "codex")
	b := NewFakeBackend()
	u := &ui{b: b}
	u.cfg.Chat.Claude = true
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	u.gui.TaskAgent = "claude"
	start := func(agent string, queue bool) any {
		t.Helper()
		u.openTask(&st, "")
		d := &u.task
		i := slices.IndexFunc(d.agents, func(a foundAgent) bool { return a.name == agent })
		if i < 0 {
			t.Fatalf("no %q in %v", agent, d.agents)
		}
		d.pickAgent(i, false)
		d.modes[i] = 1
		d.where = whereHere
		d.prompt.SetText("Fix the login")
		u.submitTask(&st, queue)
		sent := b.Sent()
		return sent[len(sent)-1]
	}
	names := func() []string {
		var out []string
		for _, a := range u.task.agents {
			out = append(out, a.name)
		}
		return out
	}

	got := start("Claude Code", false)
	want := proto.ChatNew{SessionID: "s1", GroupID: "g2", Dir: fakeHome + "/src/web-app", Provider: model.ProviderClaude, Mode: "acceptEdits", Prompt: "Fix the login"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude Code sent %#v\nwant %#v", got, want)
	}
	if n := names(); !slices.Equal(n[:2], []string{"Claude Code", "Claude Code (terminal)"}) {
		t.Fatalf("agents %q", n)
	}
	tui := []string{filepath.Join(bin, exeName("claude")), "--permission-mode", "acceptEdits", "Fix the login"}
	if nt, ok := start("Claude Code (terminal)", false).(proto.NewTask); !ok || !slices.Equal(nt.Task.Cmd, tui) {
		t.Fatalf("Claude Code (terminal) sent %#v", nt)
	}
	if nt, ok := start("Claude Code", true).(proto.NewTask); !ok || !nt.Queue || !slices.Equal(nt.Task.Cmd, tui) {
		t.Fatalf("queued Claude Code sent %#v", nt)
	}
	u.cfg.Chat.Claude = false
	if nt, ok := start("Claude Code", false).(proto.NewTask); !ok || !slices.Equal(nt.Task.Cmd, tui) {
		t.Fatalf("[chat] claude = terminal sent %#v", nt)
	}
	if slices.Contains(names(), "Claude Code (terminal)") {
		t.Fatalf("terminal entry with the TUI as default: %q", names())
	}
}

// TestChatStartWelcome: the welcome card's Claude Code opens a chat in the
// focused pane's folder when [chat] claude is on, the TUI otherwise.
func TestChatStartWelcome(t *testing.T) {
	stubAgents(t, "claude")
	for _, on := range []bool{true, false} {
		b := NewFakeBackend()
		u := &ui{b: b, th: theme.Dark()}
		u.cfg.Chat.Claude = on
		st := b.State()
		u.nav.sync(&st)
		u.welcome.on = true
		var ops op.Ops
		gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1200, 800))}
		u.drawWelcome(gtx, &st)
		if len(u.welcome.start) != 1 {
			t.Fatalf("welcome lists %d agents", len(u.welcome.start))
		}
		u.welcome.start[0].Click()
		ops.Reset()
		u.drawWelcome(gtx, &st)
		sent := b.Sent()
		last := sent[len(sent)-1]
		if c, ok := last.(proto.ChatNew); on && (!ok || c.Provider != model.ProviderClaude || c.Dir == "" || c.SessionID != u.nav.session) {
			t.Fatalf("chat on: sent %#v", last)
		}
		if ns, ok := last.(proto.NewSession); !on && (!ok || !slices.Equal(ns.Cmd, []string{"claude"})) {
			t.Fatalf("chat off: sent %#v", last)
		}
	}
}

// TestWithTerminalClaude: the TUI entry follows Claude Code, not presets.
func TestWithTerminalClaude(t *testing.T) {
	in := []foundAgent{{cmd: "codex", name: "Codex"}, {cmd: "claude", name: "Claude Code"}, {cmd: "claude", name: "Opus high", preset: true}}
	out := withTerminalClaude(slices.Clone(in))
	if len(out) != 4 || out[2].name != "Claude Code (terminal)" || !out[2].terminal || out[3].name != "Opus high" || in[2].name != "Opus high" {
		t.Fatalf("agents %+v", out)
	}
}
