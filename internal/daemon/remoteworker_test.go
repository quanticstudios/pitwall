package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// count is how many of st's tabs have id, and how many panes they hold.
func count(st model.State, id string) (tabs, panes int) {
	for _, w := range st.Workspaces {
		if w.ID == id {
			tabs++
		}
	}
	for _, p := range st.Panes {
		if p.WorkspaceID == id {
			panes++
		}
	}
	return tabs, panes
}

// A start with a client's id runs once, however often it is sent, keeps
// the parent on the other machine, and refuses an id that is malformed or
// another tab's, live or closed.
func TestStartOnce(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	o := model.Origin{Daemon: "here", Session: "s"}
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir, Origin: o}))
	st0 := d.state()
	part := st0.PartOf(o).ID
	const id = "0123456789abcdef"
	tab := proto.NewSession{SessionID: part, Cwd: dir, Cmd: []string{"sh"}, ID: id, ParentTab: "orchestrator"}
	must(t, d.handle(ctx, tab))
	must(t, d.handle(ctx, tab))
	st := d.state()
	if tabs, panes := count(st, id); tabs != 1 || panes != 1 {
		t.Fatalf("two starts made %d tabs with %d panes", tabs, panes)
	}
	if w := st.Workspace(id); w.Parent != "orchestrator" || !w.RemoteParent {
		t.Fatalf("worker %+v", w)
	}
	other := tab
	other.ParentTab = "another"
	if err := d.handle(ctx, other); err == nil || !strings.Contains(err.Error(), "another tab") {
		t.Fatalf("another parent took the id: %v", err)
	}
	bad := tab
	bad.ID = "../x"
	if err := d.handle(ctx, bad); err == nil || !strings.Contains(err.Error(), "16 hex") {
		t.Fatalf("a malformed id: %v", err)
	}
	// A worker that finished and closed is not started again.
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: id}))
	must(t, d.handle(ctx, tab))
	if tabs, _ := count(d.state(), id); tabs != 0 {
		t.Fatal("a repeated start opened a closed worker again")
	}
	if err := d.handle(ctx, other); err == nil {
		t.Fatal("another parent took a closed worker's id")
	}

	// A worktree tab with an id makes its worktree once, in the part the
	// request names.
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	const wid = "fedcba9876543210"
	wt := proto.NewWorkspace{Root: repo, SessionID: part, Name: "feat", Cmd: []string{"sh"}, ID: wid, ParentTab: "orchestrator"}
	must(t, d.handle(ctx, wt))
	must(t, d.handle(ctx, wt))
	st = d.state()
	if tabs, panes := count(st, wid); tabs != 1 || panes != 1 || len(f.worktrees) != 1 {
		t.Fatalf("two worktree starts: %d tabs, %d panes, worktrees %v", tabs, panes, f.worktrees)
	}
	if w := st.Workspace(wid); w.SessionID != part || w.Parent != "orchestrator" || !w.RemoteParent {
		t.Fatalf("worktree worker %+v", w)
	}
}

// Reopening a tab gives it a new id; its workers here, and the records of
// its workers elsewhere, follow it. A reopened worker keeps its parent on
// another machine.
func TestReopenRepoints(t *testing.T) {
	dir := t.TempDir()
	d, _, ws := closedDaemon(t, dir)
	ctx := context.Background()
	session := d.st.Workspaces[0].SessionID
	must(t, d.handle(ctx, proto.NewSession{SessionID: session, Cwd: dir, ParentPane: d.st.Panes[0].ID}))
	child := d.st.Workspaces[1].ID
	since := time.Now()
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{
		{ID: "w1", Host: "box", Parent: ws, Since: since},
		{ID: "w2", Host: "box", Parent: "someone-else", Since: since},
	}}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: ws}))
	c := d.st.Closed[0]
	must(t, d.handle(ctx, proto.ReopenTab{ID: c.ID}))
	st := d.state()
	if got := st.Workspace(child).Parent; got != c.ID {
		t.Fatalf("local child's parent %q, want %q", got, c.ID)
	}
	s := st.Session(session)
	if s.Workers[0].Parent != c.ID || s.Workers[1].Parent != "someone-else" {
		t.Fatalf("records %+v", s.Workers)
	}

	// A worker of a tab on another machine reopens under that tab.
	o := model.Origin{Daemon: "here", Session: "s"}
	must(t, d.handle(ctx, proto.SessionNew{Name: "part", Cwd: dir, Origin: o}))
	const id = "0123456789abcdef"
	st = d.state()
	must(t, d.handle(ctx, proto.NewSession{SessionID: st.PartOf(o).ID, Cwd: dir, ID: id, ParentTab: "far"}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: id}))
	st = d.state()
	i := slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == id })
	reopened := st.Closed[i].ID
	must(t, d.handle(ctx, proto.ReopenTab{ID: reopened}))
	st = d.state()
	if w := st.Workspace(reopened); w.Parent != "far" || !w.RemoteParent {
		t.Fatalf("reopened worker %+v", w)
	}
}

// Session.Workers: Add keeps the first record of an id, Set replaces one,
// Drop removes it; a record needs an id, a host and a parent.
func TestRemoteWorkers(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	session := d.state().Workspaces[0].SessionID
	a := model.RemoteWorker{ID: "a", Host: "box", Parent: "p", Since: time.Now()}
	b := a
	b.Parent = "q"
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{a}}))
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{b}}))
	st := d.state()
	if w := st.Session(session).Workers; len(w) != 1 || w[0].Parent != "p" {
		t.Fatalf("after two adds: %+v", w)
	}
	b.Lost = "never started"
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Set: []model.RemoteWorker{b, {ID: "unknown", Host: "box", Parent: "p"}}}))
	st = d.state()
	if w := st.Session(session).Workers; len(w) != 1 || w[0].Parent != "q" || w[0].Lost == "" {
		t.Fatalf("after set: %+v", w)
	}
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Drop: []string{"a"}}))
	st = d.state()
	if w := st.Session(session).Workers; w != nil {
		t.Fatalf("after drop: %+v", w)
	}
	if d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{{ID: "x", Host: "box"}}}) == nil {
		t.Fatal("a record with no parent")
	}
}

// Only the session that made a part may move its worker to another parent.
func TestReparent(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	o := model.Origin{Daemon: "here", Session: "s"}
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir, Origin: o}))
	const id = "0123456789abcdef"
	st := d.state()
	must(t, d.handle(ctx, proto.NewSession{SessionID: st.PartOf(o).ID, Cwd: dir, ID: id, ParentTab: "old"}))
	for _, wrong := range []model.Origin{{}, {Daemon: "there", Session: "s"}} {
		if d.handle(ctx, proto.Reparent{WorkspaceID: id, ParentTab: "new", Origin: wrong}) == nil {
			t.Fatalf("origin %+v moved the worker", wrong)
		}
	}
	must(t, d.handle(ctx, proto.Reparent{WorkspaceID: id, ParentTab: "new", Origin: o}))
	st = d.state()
	if w := st.Workspace(id); w.Parent != "new" || !w.RemoteParent {
		t.Fatalf("worker %+v", w)
	}
}

// A message from a pane this daemon does not know comes from another
// machine; pitwall send @<that pane> would not reach it from here.
func TestHeaderFromAnotherMachine(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	d.mu.Lock()
	got := d.header("0123456789abcdef")
	d.mu.Unlock()
	if want := "[pitwall: message from an agent on another machine; answer in your reply, which it reads with pitwall read]"; got != want {
		t.Fatalf("header %q, want %q", got, want)
	}
}
