package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

func TestMultiSkillshareMissingPane(t *testing.T) {
	local, _ := localBackend(t, hostState("ls", "lw", "lp"))
	m := newMulti(local, nil)
	_, err := m.Skillshare(context.Background(), "box", proto.Skillshare{Pane: "removed", Call: skillshare.Call{Op: skillshare.OpUninstall}}, nil)
	if err == nil {
		t.Fatal("a removed pane's operation fell back to the current machine")
	}
}

func TestSkillshareCancel(t *testing.T) {
	b, daemon := localBackend(t, hostState("ls", "lw", "lp"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.Skillshare(ctx, "", proto.Skillshare{Call: skillshare.Call{Op: skillshare.OpDiscover}}, nil)
		done <- err
	}()
	call, ok := recv(daemon).(proto.Skillshare)
	if !ok || call.Cancel {
		t.Fatal("the daemon did not receive the call")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call: %v", err)
	}
	if msg, ok := recv(daemon).(proto.Skillshare); !ok || !msg.Cancel || msg.ID != call.ID {
		t.Fatalf("cancel: %+v", msg)
	}
}

// answer is a fake daemon answering the next Skillshare it reads with a
// step and then reply, and reporting which one it got.
func answer(e end, reply skillshare.Reply) chan proto.Skillshare {
	got := make(chan proto.Skillshare, 1)
	go func() {
		m, ok := recv(e).(proto.Skillshare)
		if !ok {
			close(got)
			return
		}
		e.c.Send(proto.SkillshareResult{ID: m.ID, Step: "Syncing global…"})
		e.c.Send(proto.SkillshareResult{ID: m.ID, Done: true, Reply: reply})
		got <- m
	}()
	return got
}

// TestMultiSkillshareRoutes: the popup's work for a remote tab's pane runs
// on that tab's host, for a machine picked by name on that machine, and
// its steps and reply come back to the caller.
func TestMultiSkillshareRoutes(t *testing.T) {
	local, ld := localBackend(t, hostState("ls", "lw", "lp"))
	conns := make(chan end, 1)
	box := hostState("rs", "rw", "rp")
	box.Skillshare = true
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
	if on, old := m.SkillshareOn("box"); !on || old {
		t.Fatalf("box: installed %v, old %v", on, old)
	}
	if on, _ := m.SkillshareOn(""); on {
		t.Fatal("this machine's daemon did not say it has skillshare")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := answer(rd, skillshare.Reply{Outcome: skillshare.Outcome{Text: "Synced global"}})
	var steps []string
	r, err := m.Skillshare(ctx, "", proto.Skillshare{Pane: "rp", Call: skillshare.Call{Op: skillshare.OpSync}}, func(s string) { steps = append(steps, s) })
	if err != nil || r.Outcome.Text != "Synced global" || len(steps) != 1 {
		t.Fatalf("reply %+v, steps %q, err %v", r, steps, err)
	}
	if call := <-got; call.Pane != "rp" || call.Call.Op != skillshare.OpSync {
		t.Fatalf("box got %+v", call)
	}

	got = answer(rd, skillshare.Reply{Machine: skillshare.Machine{Version: "0.30.7"}})
	if r, err := m.Skillshare(ctx, "box", proto.Skillshare{Call: skillshare.Call{Op: skillshare.OpMachine}}, nil); err != nil || r.Machine.Version != "0.30.7" {
		t.Fatalf("picked box: %+v, %v", r, err)
	}
	<-got

	// This machine's pane stays here.
	got = answer(ld, skillshare.Reply{Project: "/here"})
	if r, err := m.Skillshare(ctx, "box", proto.Skillshare{Pane: "lp", Call: skillshare.Call{Op: skillshare.OpProject}}, nil); err != nil || r.Project != "/here" {
		t.Fatalf("local pane: %+v, %v", r, err)
	}
	<-got
}

// TestSkillshareOldDaemon: a daemon below SkillshareLevel is reported old,
// and the window sends it nothing.
func TestSkillshareOldDaemon(t *testing.T) {
	b, d := localBackend(t, hostState("ls", "lw", "lp"))
	b.mu.Lock()
	b.link.Level = proto.SkillshareLevel - 1
	b.mu.Unlock()
	if on, old := b.SkillshareOn(""); on || !old {
		t.Fatalf("installed %v, old %v", on, old)
	}
	if _, err := b.Skillshare(context.Background(), "", proto.Skillshare{Call: skillshare.Call{Op: skillshare.OpMachine}}, nil); err == nil {
		t.Fatal("sent to a daemon that does not know the message")
	}
	if m := recv(d); m != nil {
		t.Fatalf("the old daemon got %#v", m)
	}
}
