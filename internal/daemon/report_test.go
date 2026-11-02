package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

func report(t *testing.T, d *Daemon, id string, r agent.Report) {
	t.Helper()
	b, err := json.Marshal(r)
	must(t, err)
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: "report", Payload: b}))
}

func useReportHooks(d *Daemon) {
	d.mu.Lock()
	d.o.Derive, d.o.SessionID = agent.Derive, agent.SessionID
	d.mu.Unlock()
}

func reportedPane(d *Daemon, id string) model.Pane {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.st.Panes {
		if p.ID == id {
			return p
		}
	}
	return model.Pane{}
}

func TestReportLifecycle(t *testing.T) {
	d, lp, id := openLive(t, 400)
	useReportHooks(d)
	cmd := []string{"my-agent", "--resume", "session 1"}
	report(t, d, id, agent.Report{Name: "My agent", State: "working", ResumeCmd: cmd})
	if p := reportedPane(d, id); p.Provider != "agent:My agent" || !slices.Equal(p.ResumeCmd, cmd) {
		t.Fatalf("identity: %+v", p)
	}
	lp.fgGroup.Store(500) // a tool takes the foreground
	polls()
	if d.stateOf(id) != model.StateWorking {
		t.Fatal("a tool cleared the report")
	}
	report(t, d, id, agent.Report{Name: "My agent", State: "blocked", Message: "Choose a file"})
	if a := d.activityOf(id); a.State != model.StateAwaitingInput || a.Detail != "Choose a file" {
		t.Fatalf("blocked activity: %+v", a)
	}
	explained, ok := d.agentExplain(proto.AgentExplain{Pane: id}).(proto.AgentExplanation)
	if !ok || explained.Rule != "hook" || explained.Provider != "agent:My agent" || explained.State != model.StateAwaitingInput || explained.Hook == nil || explained.Hook.Provider != "agent:My agent" {
		t.Fatalf("self-reported explanation: %+v", explained)
	}
	d.mu.Lock()
	attention := d.attended()
	d.mu.Unlock()
	if len(attention) != 1 || !attention[0].Unseen {
		t.Fatalf("attention: %+v", attention)
	}
	d.noteInput(id, []byte("y"))
	settled()
	if d.stateOf(id) != model.StateAwaitingInput {
		t.Fatal("screen settling overrode the report")
	}
	report(t, d, id, agent.Report{Name: "My agent", ResumeCmd: []string{"my-agent", "--resume", "session 2"}})
	if d.stateOf(id) != model.StateAwaitingInput {
		t.Fatal("a session report changed state")
	}
	report(t, d, id, agent.Report{Name: "My agent", State: "done"})
	if d.stateOf(id) != model.StateCompleted {
		t.Fatal("done did not complete the turn")
	}
	report(t, d, id, agent.Report{Name: "My agent", State: "idle"})
	if d.stateOf(id) != "" || reportedPane(d, id).Provider != "agent:My agent" || len(reportedPane(d, id).ResumeCmd) == 0 {
		t.Fatal("idle cleared identity or kept activity")
	}
	report(t, d, id, agent.Report{Name: "My agent", ResumeCmd: []string{}})
	if store.Resumes(reportedPane(d, id)) {
		t.Fatal("empty argv did not clear resume")
	}
	report(t, d, id, agent.Report{Name: "Other agent", Release: true})
	if d.providerOf(id) != "agent:My agent" {
		t.Fatal("another label released the agent")
	}
	report(t, d, id, agent.Report{Name: "My agent", Release: true})
	if p := reportedPane(d, id); p.Provider != "" || len(p.ResumeCmd) != 0 || d.stateOf(id) != "" {
		t.Fatalf("release: %+v", p)
	}
}

func TestReportClearsAtShell(t *testing.T) {
	d, lp, id := openLive(t, 400)
	useReportHooks(d)
	report(t, d, id, agent.Report{Name: "My agent", State: "working", ResumeCmd: []string{"my-agent", "--resume", "s"}})
	lp.fgGroup.Store(200)
	waitUntil(t, "self-report clears at shell", func() bool { return d.providerOf(id) == "" })
	if p := reportedPane(d, id); store.Resumes(p) || len(p.ResumeCmd) != 0 || d.stateOf(id) != "" {
		t.Fatalf("shell keeps report: %+v", p)
	}
}

func TestReportRejectsStaleShellRead(t *testing.T) {
	oldSession, oldIdentify, oldComm := sessionOf, identify, commOf
	sessionOf, identify, commOf = fakeSession, fakeIdentify, fakeComm
	t.Cleanup(func() { sessionOf, identify, commOf = oldSession, oldIdentify, oldComm })
	f := &fakes{statsCalls: map[string]int{}}
	id := "p"
	d := fromState(f.options(), model.State{Panes: []model.Pane{{ID: id}}})
	lp := &livePane{fakePane: &fakePane{}}
	lp.fgGroup.Store(200)
	d.panes[id] = lp
	useReportHooks(d)
	report(t, d, id, agent.Report{Name: "My agent", State: "working"})
	d.mu.Lock()
	at := d.live.hookAt[id]
	l := look{id: id, p: lp, shell: shellComm, hooked: true, reported: true, reportSeq: d.live.hookSeq[id], sid: 200}
	d.mu.Unlock()
	report(t, d, id, agent.Report{Name: "My agent", State: "blocked"})
	d.mu.Lock()
	d.live.hookAt[id] = at // consecutive reports can share a clock tick
	d.mu.Unlock()
	d.lookAt(context.Background(), l)
	if d.providerOf(id) != "agent:My agent" {
		t.Fatal("a stale shell read cleared the new report")
	}
}

func TestReportAuthorityAndValidation(t *testing.T) {
	t.Run("known foreground", func(t *testing.T) {
		d, _, id := openLive(t, 300)
		useReportHooks(d)
		report(t, d, id, agent.Report{Name: "My agent", State: "blocked", ResumeCmd: []string{"wrong"}})
		if p := reportedPane(d, id); model.ReportedName(p.Provider) != "" || len(p.ResumeCmd) != 0 {
			t.Fatalf("overrode a known agent: %+v", p)
		}
	})
	t.Run("hook takes authority", func(t *testing.T) {
		d, _, id := openLive(t, 400)
		useReportHooks(d)
		report(t, d, id, agent.Report{Name: "My agent", State: "blocked", ResumeCmd: []string{"wrong"}})
		must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte(`{"type":"agent-turn-complete","thread-id":"s"}`)}))
		report(t, d, id, agent.Report{Name: "My agent", State: "working"})
		if p := reportedPane(d, id); p.Provider != model.ProviderCodex || p.SessionID != "s" || len(p.ResumeCmd) != 0 {
			t.Fatalf("hook lost authority: %+v", p)
		}
	})
	t.Run("invalid report is atomic", func(t *testing.T) {
		d, _, id := openLive(t, 400)
		useReportHooks(d)
		report(t, d, id, agent.Report{Name: "My agent", State: "blocked", ResumeCmd: []string{"my-agent"}})
		err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: "report", Payload: []byte(`{"name":"Other","state":"wrong","resume_cmd":["wrong"]}`)})
		if p := reportedPane(d, id); err == nil || p.Provider != "agent:My agent" || p.ResumeCmd[0] != "my-agent" || d.stateOf(id) != model.StateAwaitingInput {
			t.Fatalf("invalid report changed pane: %+v, %v", p, err)
		}
		report(t, d, id, agent.Report{Name: "Other"})
		if p := reportedPane(d, id); p.Provider != "agent:Other" || len(p.ResumeCmd) != 0 || d.stateOf(id) != "" {
			t.Fatalf("new identity kept old state: %+v", p)
		}
	})
}

func TestReportRestoresExactSession(t *testing.T) {
	d, _, id := openLive(t, 400)
	useReportHooks(d)
	cmd := []string{"my-agent", "--resume", "a b", "--model", "small"}
	report(t, d, id, agent.Report{Name: "My agent", State: "idle", ResumeCmd: cmd})
	d.mu.Lock()
	saved := d.snapshot()
	d.mu.Unlock()
	path := filepath.Join(t.TempDir(), "state.json")
	must(t, store.Save(path, saved))
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Load = func() (model.State, error) { return store.Load(path) }
	o.RestoreCmd = store.RestoreCmd
	restored, err := NewWith(o)
	must(t, err)
	if p := reportedPane(restored, id); !slices.Equal(f.pane(0).cfg.Cmd, cmd) || p.Provider != "agent:My agent" || f.pane(0).cfg.Cwd != reportedPane(d, id).Cwd {
		t.Fatalf("restored %+v with %q", p, f.pane(0).cfg.Cmd)
	}
	saved.Panes[0].ResumeCmd = nil
	o.Load = func() (model.State, error) { return saved, nil }
	fresh, err := NewWith(o)
	must(t, err)
	if p := reportedPane(fresh, id); p.Provider != "" || len(p.ResumeCmd) != 0 || len(f.pane(1).cfg.Cmd) != 0 {
		t.Fatalf("no-command identity survived restart: %+v", p)
	}
	f.pane(0).Close()
	waitUntil(t, "resumed agent exits to shell", func() bool { return restored.providerOf(id) == "" })
	if p := reportedPane(restored, id); len(p.ResumeCmd) != 0 {
		t.Fatalf("resume command survived exit: %+v", p)
	}
}

func TestReportHeldExitClearsResume(t *testing.T) {
	d, lp, id := openLive(t, 400, "my-agent")
	useReportHooks(d)
	d.mu.Lock()
	d.st.Panes[0].Held = true
	d.mu.Unlock()
	report(t, d, id, agent.Report{Name: "My agent", State: "done", ResumeCmd: []string{"my-agent", "--resume", "s"}})
	lp.Close()
	waitUntil(t, "held agent exits", func() bool { return reportedPane(d, id).Exited })
	if p := reportedPane(d, id); store.Resumes(p) || len(p.ResumeCmd) != 0 {
		t.Fatalf("held agent kept resume command: %+v", p)
	}
}

func TestReportedAgentsQueueOnRestore(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	for i := range f.saved.Panes {
		p := &f.saved.Panes[i]
		if p.ID == "recent-b" || p.ID == "old-a" {
			p.Provider, p.SessionID = "agent:My agent", ""
			p.ResumeCmd = []string{"my-agent", "--resume", p.ID}
		}
	}
	o := f.options()
	o.RestoreCmd = store.RestoreCmd
	o.StartupPerAgentDelay = func() time.Duration { return time.Hour }
	d, err := NewWith(o)
	must(t, err)
	defer d.shutdown()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Equal(d.restoring.pending, []string{"recent-b", "old-a", "old-b"}) {
		t.Fatalf("restore queue = %v", d.restoring.pending)
	}
	f.mu.Lock()
	var launched []string
	for _, p := range f.panes {
		launched = append(launched, p.cfg.ID)
	}
	f.mu.Unlock()
	if !slices.Equal(launched, []string{"shell", "command", "recent-a"}) {
		t.Fatalf("initial launches = %v", launched)
	}
	for _, id := range []string{"recent-b", "old-a"} {
		if !d.live.hookAt[id].IsZero() {
			t.Fatalf("%s has hook ownership before launch", id)
		}
		d.restoring.timer.Stop()
		d.restoreNext()
		f.mu.Lock()
		cmd := f.panes[len(f.panes)-1].cfg.Cmd
		f.mu.Unlock()
		if !slices.Equal(cmd, []string{"my-agent", "--resume", id}) || d.resumed[id].IsZero() || d.live.hookAt[id].IsZero() {
			t.Fatalf("%s restores with %q without resume or hook ownership", id, cmd)
		}
	}
}
