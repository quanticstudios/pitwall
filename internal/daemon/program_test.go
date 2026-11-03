package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeGroups stands in for the process table: a group is alive until it
// is ended.
func fakeGroups(t *testing.T) (end func(int)) {
	var mu sync.Mutex
	dead := map[int]bool{}
	old := groupAlive
	groupAlive = func(g int) bool { mu.Lock(); defer mu.Unlock(); return !dead[g] }
	t.Cleanup(func() { groupAlive = old })
	return func(g int) { mu.Lock(); dead[g] = true; mu.Unlock() }
}

// status sends what pitwall status sends for body from process group group.
func status(t *testing.T, d *Daemon, id, body string, group int) {
	t.Helper()
	b, _ := json.Marshal(programPayload{Program: body, Group: group})
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: "report", Payload: b}))
}

// recordsOf is how many records pane id holds.
func recordsOf(d *Daemon, id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.prog[id].store.Records())
}

func programOf(d *Daemon, id string) *model.Program { return reportedPane(d, id).Program }

// shown is pane id's activity as clients see it.
func (d *Daemon) shown(id string) model.Activity {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.attended() {
		if a.PaneID == id {
			return a
		}
	}
	return model.Activity{}
}

// TestProgramStatus follows a build's reports from a script in a shell pane:
// what the pane and its activity show, and each way they end.
func TestProgramStatus(t *testing.T) {
	end := fakeGroups(t)
	d, lp, id := openLive(t, 900) // a command in the foreground
	msg := base64.StdEncoding.EncodeToString([]byte("Compiling"))
	status(t, d, id, "state=working:app=cargo:progress=40:msg="+msg, 900)
	if p := programOf(d, id); p == nil || *p != (model.Program{App: "cargo", State: "working", Progress: 40, Message: "Compiling", UpdatedAt: p.UpdatedAt}) {
		t.Fatalf("program: %+v", p)
	}
	if a := d.shown(id); a.State != model.StateWorking || a.Detail != "Compiling" || a.Provider != model.ProviderTerminal {
		t.Fatalf("working shows as %+v", a)
	}
	status(t, d, id, "state=blocked:kind=permission:app=cargo", 900)
	if a := d.shown(id); a.State != model.StatePendingApproval || !a.Unseen {
		t.Fatalf("blocked shows as %+v", a)
	}
	status(t, d, id, "state=blocked:app=cargo", 900)
	if a := d.shown(id); a.State != model.StateAwaitingInput {
		t.Fatalf("blocked without a kind shows as %+v", a)
	}
	// A record under the root that failed beats a working root.
	status(t, d, id, "state=working:app=cargo", 900)
	status(t, d, id, "state=error:id=test:msg="+base64.StdEncoding.EncodeToString([]byte("2 failed")), 900)
	if a := d.shown(id); a.State != model.StateError || a.Detail != "2 failed" || !a.Unseen {
		t.Fatalf("error shows as %+v", a)
	}
	// The script exits: its working root goes, the failure stays.
	end(900)
	lp.fgGroup.Store(200)
	waitUntil(t, "the ended group's records go", func() bool { return recordsOf(d, id) == 1 })
	if p := programOf(d, id); p == nil || p.State != "error" {
		t.Fatalf("the failure went with its group: %+v", p)
	}
	// Focus reports are not typing; a key is.
	d.noteInput(id, []byte("\x1b[I"))
	if programOf(d, id) == nil {
		t.Fatal("a focus report ended the failure")
	}
	d.noteInput(id, []byte("x"))
	if p := programOf(d, id); p != nil || d.shown(id).State != "" {
		t.Fatalf("typing kept %+v", p)
	}
	// Reports out of order: the older one changes nothing.
	seq := d.progSeq.Add(2)
	d.programReport(id, progstatus.Report{State: progstatus.Done, Progress: progstatus.NoProgress}, seq, 0)
	d.programReport(id, progstatus.Report{State: progstatus.Working, Progress: progstatus.NoProgress}, seq-1, 0)
	if p := programOf(d, id); p == nil || p.State != "done" {
		t.Fatalf("an older report won: %+v", p)
	}
	// Idle shows nothing, and so does a malformed report.
	status(t, d, id, "state=idle", 0)
	status(t, d, id, "state=working:id=a//b", 0)
	if p := programOf(d, id); p != nil {
		t.Fatalf("idle or malformed shows %+v", p)
	}
}

// TestProgramFromVT takes the foreground group as the report's, unless the
// shell holds the foreground.
func TestProgramFromVT(t *testing.T) {
	end := fakeGroups(t)
	d, lp, id := openLive(t, 900)
	w := progstatus.Report{State: progstatus.Working, Progress: progstatus.NoProgress}
	d.programFromVT(id, w, d.progSeq.Add(1))
	lp.fgGroup.Store(200)
	d.programFromVT(id, progstatus.Report{State: progstatus.Working, ID: "bg", Progress: progstatus.NoProgress}, d.progSeq.Add(1))
	d.mu.Lock()
	recs := d.prog[id].store.Records()
	d.mu.Unlock()
	if len(recs) != 2 || recs[0].Group != 900 || recs[1].Group != 0 {
		t.Fatalf("groups: %+v", recs)
	}
	end(900)
	waitUntil(t, "the root ends with its group", func() bool { return recordsOf(d, id) == 1 })
}

// TestProgramYieldsToAgent shows an agent's state over a build's that runs
// under it, and keeps the build on the pane for the row.
func TestProgramYieldsToAgent(t *testing.T) {
	fakeGroups(t)
	d, _, id := liveDaemon(t, model.StatePendingApproval)
	status(t, d, id, "state=error:app=make", 0)
	if a := d.shown(id); a.State != model.StatePendingApproval || a.Provider != model.ProviderClaude {
		t.Fatalf("the program took the agent's row: %+v", a)
	}
	if p := programOf(d, id); p == nil || p.App != "make" {
		t.Fatalf("program under an agent: %+v", p)
	}
	// The agent at its prompt, with no activity, still owns the pane.
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("")}))
	if a := d.shown(id); a.State != "" {
		t.Fatalf("an idle agent's pane shows %+v", a)
	}
}

// TestProgramEndsOnExit ends a held command's working record when it
// exits, and keeps its done one.
func TestProgramEndsOnExit(t *testing.T) {
	fakeGroups(t)
	d, lp, id := openLive(t, 400, "build")
	d.mu.Lock()
	d.st.Panes[0].Held = true
	d.mu.Unlock()
	status(t, d, id, "state=done:id=compile", 0)
	status(t, d, id, "state=working", 0)
	lp.Close()
	waitUntil(t, "held command exits", func() bool { return reportedPane(d, id).Exited })
	if p := programOf(d, id); p == nil || p.State != "done" {
		t.Fatalf("after exit: %+v", p)
	}
}
