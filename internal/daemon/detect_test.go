package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The fake process table: group 200 is the pane's shell, 100 runs Claude, 300
// Codex, 700 Gemini CLI, 800 pi, and any other group runs sleep. The shell may exec codex (execd).
func fakeSession(int) int { return 200 }

var execd atomic.Bool

// commGone makes the leader of group 700 report another comm, as when
// the program exited and its pid went to something else.
var commGone atomic.Bool

func fakeComm(pid int) string {
	switch {
	case pid == 200 && execd.Load():
		return "codex"
	case pid == 200:
		return shellComm
	case pid == 700 && commGone.Load():
		return "zsh"
	}
	_, comm := fakeIdentify(pid)
	return comm
}

func fakeIdentify(pg int) (model.Provider, string) {
	switch {
	case pg == 100:
		return model.ProviderClaude, "claude"
	case pg == 300, pg == 200 && execd.Load():
		return model.ProviderCodex, "codex"
	case pg == 700:
		return model.ProviderGemini, "gemini"
	case pg == 800:
		return model.ProviderPi, "pi"
	case pg == 701:
		return model.ProviderOpenCode, "opencode"
	}
	return "", "sleep"
}

const (
	codexWorking = "• Working (2s • esc to interrupt)\n› Ask Codex to do anything\n  ? for shortcuts"
	codexIdle    = "• done\n› Ask Codex to do anything\n  ? for shortcuts"
)

func (d *Daemon) activityOf(id string) model.Activity {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := d.activityIndex(id); i >= 0 {
		return d.st.Activities[i]
	}
	return model.Activity{}
}

// polls waits out a few liveness polls.
func polls() { time.Sleep(8 * livePoll) }

func TestDetectedAgentWorksThenCompletes(t *testing.T) {
	d, lp, id := openLive(t, 200)
	lp.show(codexWorking, false)
	lp.fgGroup.Store(300) // codex started from the shell, without hooks
	waitUntil(t, "working", func() bool { return d.stateOf(id) == model.StateWorking })
	if a := d.activityOf(id); a.Provider != model.ProviderCodex || a.SessionID != "" {
		t.Fatalf("got %+v, want provider codex and no session", a)
	}
	lp.show("a reply streaming", true)
	polls()
	if s := d.stateOf(id); s != model.StateWorking {
		t.Fatalf("a moving screen should stay working, got %q", s)
	}
	lp.show(codexIdle, false)
	waitUntil(t, "completed", func() bool { return d.stateOf(id) == model.StateCompleted })
	lp.show(codexIdle+"\n› typing", false) // typing at the prompt starts no turn
	polls()
	if s := d.stateOf(id); s != model.StateCompleted {
		t.Fatalf("typing changed completed to %q", s)
	}
}

func TestDetectedForm(t *testing.T) {
	d, lp, id := openLive(t, 100)
	lp.show(formScreen, false)
	waitUntil(t, "approval", func() bool { return d.stateOf(id) == model.StatePendingApproval })
	lp.show(idleScreen, false) // denied: idle, and no turn ran
	waitUntil(t, "cleared", func() bool { return d.stateOf(id) == "" })
}

func TestHookStopsDetection(t *testing.T) {
	d, lp, id := openLive(t, 300)
	lp.show(codexWorking, false)
	waitUntil(t, "working", func() bool { return d.stateOf(id) == model.StateWorking })
	must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte("pending-approval")}))
	lp.show(codexIdle, false)
	polls()
	if s := d.stateOf(id); s != model.StatePendingApproval {
		t.Fatalf("detection overrode the hook: %q", s)
	}
}

// Hooks own a pane only while the hooked agent's group is in the foreground:
// an agent without hooks started after it exits is read from its screen.
func TestHookOwnershipEndsWithItsGroup(t *testing.T) {
	d, lp, id := openLive(t, 300)
	must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte("clear")}))
	lp.fgGroup.Store(200) // the hooked codex exited to the shell
	polls()
	lp.show(formScreen, false)
	lp.fgGroup.Store(100) // claude, started without hooks
	waitUntil(t, "approval", func() bool { return d.stateOf(id) == model.StatePendingApproval })
}

// `exec codex` keeps the shell's pid and group, so the session leader is in
// the foreground and only its comm tells it is no longer the shell.
func TestExecStartedAgentIsDetected(t *testing.T) {
	d, lp, id := openLive(t, 200)
	lp.show(codexWorking, false)
	polls()
	if s := d.stateOf(id); s != "" {
		t.Fatalf("a shell at its prompt got %q", s)
	}
	execd.Store(true)
	waitUntil(t, "working", func() bool { return d.stateOf(id) == model.StateWorking })
}

func TestForegroundLeavingDetectedAgentClears(t *testing.T) {
	d, lp, id := openLive(t, 300)
	lp.show(codexWorking, false)
	waitUntil(t, "working", func() bool { return d.stateOf(id) == model.StateWorking })
	lp.fgGroup.Store(200)
	waitUntil(t, "cleared", func() bool { return d.stateOf(id) == "" })
}

func TestTerminalRunning(t *testing.T) {
	d, lp, id := openLive(t, 200)
	polls()
	if s := d.stateOf(id); s != "" {
		t.Fatalf("a shell at its prompt got %q", s)
	}
	lp.fgGroup.Store(400)
	waitUntil(t, "running", func() bool { return d.stateOf(id) == model.StateTerminalRunning })
	if a := d.activityOf(id); a.Provider != model.ProviderTerminal || a.Detail != "sleep" {
		t.Fatalf("got %+v, want terminal sleep", a)
	}
	lp.fgGroup.Store(200)
	waitUntil(t, "cleared", func() bool { return d.stateOf(id) == "" })
}

// A pane opened with a command runs it as the session leader, so the
// foreground is its own group and still a terminal command. A shell named as
// the command is still a shell at its prompt.
func TestPaneCommandIsTerminalRunning(t *testing.T) {
	t.Run("command", func(t *testing.T) {
		d, _, id := openLive(t, 200, "sleep", "60")
		waitUntil(t, "running", func() bool { return d.stateOf(id) == model.StateTerminalRunning })
		if a := d.activityOf(id); a.Provider != model.ProviderTerminal || a.Detail != "sleep" {
			t.Fatalf("got %+v, want terminal sleep", a)
		}
	})
	t.Run("shell", func(t *testing.T) {
		d, _, id := openLive(t, 200, "/bin/"+shellComm, "-l")
		polls()
		if s := d.stateOf(id); s != "" {
			t.Fatalf("a shell command at its prompt got %q", s)
		}
	})
}

func TestAgentIsNotTerminalRunning(t *testing.T) {
	for _, hooked := range []bool{false, true} {
		t.Run(fmt.Sprint("hooked=", hooked), func(t *testing.T) {
			d, lp, id := openLive(t, 300)
			lp.show(codexIdle, false)
			if hooked {
				must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte("clear")}))
				lp.fgGroup.Store(100) // a second agent after the hooked one
			}
			polls()
			if s := d.stateOf(id); s != "" {
				t.Errorf("idle agent got %q", s)
			}
		})
	}
}

func TestProcReads(t *testing.T) {
	pid := os.Getpid()
	if sessionOf(pid) <= 0 {
		t.Error("no session for this process")
	}
	if p, comm := identify(pid); p != "" || comm == "" {
		t.Errorf("identify(self) = %q, %q", p, comm)
	}
}

// BenchmarkIdentify is the process read for a foreground that is no agent:
// comm, exe and the walk of its group (this test binary, with its threads).
func BenchmarkIdentify(b *testing.B) {
	pid := os.Getpid()
	for b.Loop() {
		identify(pid)
	}
}

// BenchmarkScreen is the screen read for a detected agent at 100x30.
func BenchmarkScreen(b *testing.B) {
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(codexWorking, "\n", "\r\n")))
	for b.Loop() {
		agent.State(e.Snapshot())
	}
}

func (d *Daemon) providerOf(id string) model.Provider {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.st.Panes {
		if p.ID == id {
			return p.Provider
		}
	}
	return ""
}

// A detected agent names its pane while it is the foreground, even idle
// at its prompt with no activity, and stops when it exits to the shell.
func TestDetectedProviderFollowsForeground(t *testing.T) {
	d, lp, id := openLive(t, 200)
	lp.show("❯ ", false) // idle: no activity
	lp.fgGroup.Store(100)
	waitUntil(t, "claude named", func() bool { return d.providerOf(id) == model.ProviderClaude })
	if s := d.stateOf(id); s != "" {
		t.Fatalf("idle claude got activity %q", s)
	}
	lp.fgGroup.Store(200)
	waitUntil(t, "cleared at the shell", func() bool { return d.providerOf(id) == "" })
}

// A hook names the pane's agent; its group leaving the foreground clears it.
func TestHookedProviderClearsOnExit(t *testing.T) {
	d, lp, id := openLive(t, 300)
	must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte("clear")}))
	if p := d.providerOf(id); p != model.ProviderCodex {
		t.Fatalf("hook left provider %q", p)
	}
	lp.fgGroup.Store(200) // the hooked codex exited to the shell
	waitUntil(t, "cleared", func() bool { return d.providerOf(id) == "" })
}

func (d *Daemon) hooksMissing(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.st.Panes {
		if p.ID == id {
			return p.HooksMissing
		}
	}
	return false
}

// An agent seen starting that sends no hook within hookGrace, or one whose
// screen shows a turn without a hook, marks its pane; a hook clears it.
// Codex sends nothing while idle, and an agent already running when the
// daemon started may have hooks, so neither is marked by time alone.
func TestHooksMissing(t *testing.T) {
	old := hookGrace
	hookGrace = 50 * time.Millisecond
	t.Cleanup(func() { hookGrace = old })

	d, lp, id := openLive(t, 200)
	lp.show("❯ ", false)
	polls()
	lp.fgGroup.Store(100) // claude starts, without hooks
	waitUntil(t, "claude marked", func() bool { return d.hooksMissing(id) })
	must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("clear")}))
	if d.hooksMissing(id) {
		t.Fatal("a hook left the mark")
	}

	lp.fgGroup.Store(200)
	waitUntil(t, "back at the shell", func() bool { return d.providerOf(id) == "" })
	lp.show(codexIdle, false)
	lp.fgGroup.Store(300) // codex, idle
	polls()
	if d.hooksMissing(id) {
		t.Fatal("idle codex marked")
	}
	lp.show(codexWorking, false)
	waitUntil(t, "codex in a turn marked", func() bool { return d.hooksMissing(id) })
	lp.fgGroup.Store(200)
	waitUntil(t, "mark cleared at the shell", func() bool { return !d.hooksMissing(id) })
	lp.fgGroup.Store(700) // gemini, by its process name
	waitUntil(t, "gemini marked", func() bool { return d.hooksMissing(id) })
}

// Without hooks or a decision model, Gemini CLI's state comes from its
// screen's text, and its finished turn from the screen going quiet.
func TestGeminiScreenRules(t *testing.T) {
	d, lp, id := openLive(t, 200)
	lp.show("$ ", false)
	polls()
	lp.show(" ⠏ Reading the directory (esc to cancel, 4s)", false)
	lp.fgGroup.Store(700)
	waitUntil(t, "gemini working", func() bool { return d.stateOf(id) == model.StateWorking })
	if a := d.activityOf(id); a.Provider != model.ProviderGemini {
		t.Errorf("activity %+v", a)
	}
	lp.show("│ >   Type your message or @path/to/file │", false)
	waitUntil(t, "gemini done", func() bool { return d.stateOf(id) == model.StateCompleted })
	lp.show("│ Action Required │\n│ ● 1. Allow once │", false)
	waitUntil(t, "gemini asks", func() bool { return d.stateOf(id) == model.StatePendingApproval })
}

func TestHooksMissingSkipsAgentFoundRunning(t *testing.T) {
	old := hookGrace
	hookGrace = 50 * time.Millisecond
	t.Cleanup(func() { hookGrace = old })
	d, lp, id := openLive(t, 100) // claude running at the first poll
	lp.show("❯ ", false)
	polls()
	if d.hooksMissing(id) {
		t.Fatal("an agent found running marked by time")
	}
}
