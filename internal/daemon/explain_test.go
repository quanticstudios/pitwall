package daemon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func explanation(t *testing.T, d *Daemon, id string) proto.AgentExplanation {
	t.Helper()
	r, ok := d.agentExplain(proto.AgentExplain{Pane: id}).(proto.AgentExplanation)
	if !ok {
		t.Fatalf("explanation: %v", d.agentExplain(proto.AgentExplain{Pane: id}))
	}
	return r
}

func TestAgentExplainDetection(t *testing.T) {
	d, lp, id := openLive(t, 300)
	lp.show(codexWorking, false)
	waitUntil(t, "screen explanation", func() bool {
		r := explanation(t, d, id)
		return r.State == model.StateWorking && r.Rule == "busy/esc to interrupt"
	})
	r := explanation(t, d, id)
	if r.Provider != model.ProviderCodex || r.Screen.Matched == nil || r.Screen.Matched.Line == "" ||
		r.Process == nil || r.Process.Foreground != 300 || r.Process.Provider != model.ProviderCodex || r.Process.Comm != "codex" || r.Hook != nil {
		t.Fatalf("screen: %+v", r)
	}
	lp.show(codexIdle, false)
	waitUntil(t, "still screen explanation", func() bool {
		r := explanation(t, d, id)
		return r.State == model.StateCompleted && r.Rule == "screen-still" && r.Screen.State == ""
	})
	lp.fgGroup.Store(400)
	waitUntil(t, "terminal explanation", func() bool {
		r := explanation(t, d, id)
		return r.State == model.StateTerminalRunning && r.Rule == "terminal-process" && r.Process.Comm == "sleep"
	})
	lp.fgGroup.Store(200)
	waitUntil(t, "shell explanation", func() bool {
		r := explanation(t, d, id)
		return r.State == "" && r.Rule == "shell-prompt"
	})
}

func TestAgentExplainHook(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StateAwaitingInput)
	lp.show(codexWorking, false)
	d.mu.Lock()
	h := d.live.lastHook[id]
	h.At = time.Now().Add(-2 * time.Second)
	d.live.lastHook[id] = h
	d.mu.Unlock()
	before := d.state()
	r := explanation(t, d, id)
	if r.State != model.StateAwaitingInput || r.Rule != "hook" || r.Hook == nil ||
		r.Hook.State != model.StateAwaitingInput || r.Hook.Provider != model.ProviderClaude || r.Hook.AgeMS < 2000 ||
		r.Screen.State != model.StateWorking || r.Screen.Matched == nil {
		t.Fatalf("hook must own the decision: %+v", r)
	}
	if after := d.state(); !reflect.DeepEqual(before, after) {
		t.Fatal("explain changed daemon state")
	}
	lp.fgGroup.Store(200)
	waitUntil(t, "hook agent left", func() bool { return d.stateOf(id) == "" })
	if r := explanation(t, d, id); r.Hook == nil || r.Hook.State != model.StateAwaitingInput {
		t.Fatalf("lost last hook after agent exit: %+v", r)
	}
}

func TestAgentExplainSettle(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StatePendingApproval)
	lp.show(idleScreen, false)
	d.settle(id, time.Now())
	r := explanation(t, d, id)
	if r.State != "" || r.Rule != "settle-still" || r.Hook == nil || r.Hook.State != model.StatePendingApproval {
		t.Fatalf("settle: %+v", r)
	}
	if err := d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("working")}); err != nil {
		t.Fatal(err)
	}
	lp.show(spinnerScreen, false)
	d.settle(id, time.Now())
	r = explanation(t, d, id)
	if r.State != model.StateWorking || r.Rule != "settle-working" || r.Screen.Matched == nil {
		t.Fatalf("settle working: %+v", r)
	}
}

func TestAgentExplainValidation(t *testing.T) {
	d, lp, id := openLive(t, 200)
	for _, m := range []proto.AgentExplain{{Pane: "absent"}, {Pane: id, Provider: "unknown"}} {
		if _, ok := d.agentExplain(m).(proto.Error); !ok {
			t.Fatalf("accepted %+v", m)
		}
	}
	r := d.agentExplain(proto.AgentExplain{Pane: id, Provider: model.ProviderGemini}).(proto.AgentExplanation)
	if r.Screen.Provider != model.ProviderGemini {
		t.Fatalf("ignored provider hint: %+v", r)
	}
	lp.show(codexWorking, false)
	lp.fgGroup.Store(400)
	waitUntil(t, "unrecognized process", func() bool { return d.stateOf(id) == model.StateTerminalRunning })
	r = d.agentExplain(proto.AgentExplain{Pane: id, Provider: model.ProviderCodex}).(proto.AgentExplanation)
	if r.Provider != model.ProviderTerminal || r.State != model.StateTerminalRunning ||
		r.Screen.Provider != model.ProviderCodex || r.Screen.State != model.StateWorking {
		t.Fatalf("provider hint must affect only the screen explanation: %+v", r)
	}
}
