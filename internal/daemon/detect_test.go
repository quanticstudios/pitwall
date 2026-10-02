package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The fake process table: group 200 is the pane's shell, 100 runs Claude, 300
// Codex, and any other group runs sleep.
func fakeSession(int) int { return 200 }

func fakeIdentify(pg int) (model.Provider, string) {
	switch pg {
	case 100:
		return model.ProviderClaude, "claude"
	case 300:
		return model.ProviderCodex, "codex"
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
