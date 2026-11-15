package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
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

func TestRemoteWorkerRecordOwnership(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	session := d.state().Workspaces[0].SessionID
	w := model.RemoteWorker{ID: "worker", Host: "box", Parent: "parent", Since: time.Now()}
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{w}}))
	for _, other := range []model.RemoteWorker{
		{ID: w.ID, Host: "box", Parent: "another", Since: w.Since},
		{ID: w.ID, Host: "example.com", Parent: w.Parent, Since: w.Since},
	} {
		if err := d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{other}, Set: []model.RemoteWorker{other}}); err == nil {
			t.Fatal("a retry took another worker's record")
		}
	}
	if got := d.state().Sessions[0].Workers[0]; got != w {
		t.Fatalf("refused retry changed record: %+v", got)
	}
}

func TestRemoteWorkerLostUpdateIsCurrent(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	session := d.state().Workspaces[0].SessionID
	w := model.RemoteWorker{ID: "worker", Host: "box", Parent: "parent", Since: time.Now().Add(-3 * time.Minute)}
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{w}}))
	stale := w
	d.mu.Lock()
	d.repoint(w.Parent, "reopened")
	d.mu.Unlock()
	stale.Lost = "never started"
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Set: []model.RemoteWorker{stale}}))
	if got := d.state().Sessions[0].Workers[0]; got.Parent != "reopened" || got.Lost != "" {
		t.Fatalf("a stale comparison undid reopen: %+v", got)
	}
	current := w
	current.Parent, current.Since = "reopened", time.Now()
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Set: []model.RemoteWorker{current}}))
	stale.Parent = "reopened"
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Set: []model.RemoteWorker{stale}}))
	if got := d.state().Sessions[0].Workers[0]; got != current {
		t.Fatalf("a stale comparison undid a retry: %+v", got)
	}
}

func TestRemoteWorkerRecordDoesNotExpire(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	session := d.state().Workspaces[0].SessionID
	w := model.RemoteWorker{ID: "worker", Host: "box", Parent: "parent", Since: time.Now().AddDate(0, 0, -model.ClosedDays-1)}
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{w}}))
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session}))
	if got := d.state().Sessions[0].Workers; len(got) != 1 || got[0] != w {
		t.Fatalf("an unresolved worker expired: %+v", got)
	}
}

func TestStartOnceAfterReopen(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint(remote), func(t *testing.T) {
			f := &fakes{statsCalls: map[string]int{}}
			d := newDaemon(t, f.options())
			ctx := context.Background()
			dir := t.TempDir()
			must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir}))
			part := d.state().Sessions[0].ID
			const id = "0123456789abcdef"
			tab := proto.NewSession{SessionID: part, Cwd: dir, Cmd: []string{"sh"}, ID: id}
			if remote {
				tab.ParentTab = "parent"
			}
			must(t, d.handle(ctx, tab))
			must(t, d.handle(ctx, proto.KillSession{WorkspaceID: id}))
			st := d.state()
			must(t, d.handle(ctx, proto.ReopenTab{ID: st.Closed[0].ID}))
			must(t, d.handle(ctx, tab))
			st = d.state()
			if tabs, panes := count(st, id); tabs != 1 || panes != 1 || len(st.Workspaces) != 2 {
				t.Fatalf("retry after reopen: %+v", st.Workspaces)
			}
		})
	}
}

// Once a part's last tab closes its session ends. Reopening its worker
// must restore the origin instead of joining somebody else's same name.
func TestRemoteWorkerReopenRestoresPart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	origin := model.Origin{Daemon: "here", Session: "s"}
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir, Origin: origin}))
	st := d.state()
	part, shell := st.PartOf(origin).ID, st.Workspaces[0].ID
	const id = "0123456789abcdef"
	tab := proto.NewSession{SessionID: part, Cwd: dir, Cmd: []string{"sh"}, ID: id, ParentTab: "parent"}
	must(t, d.handle(ctx, tab))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: shell}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: id}))
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir, Origin: model.Origin{Daemon: "another", Session: "s"}}))
	st = d.state()
	i := slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == id })
	must(t, d.handle(ctx, proto.ReopenTab{ID: st.Closed[i].ID}))
	st = d.state()
	w := st.Workspace(id)
	if w == nil || st.Session(w.SessionID).Origin != origin || st.Session(w.SessionID).Name != "work-2" {
		t.Fatalf("reopened worker lost its part: %+v, sessions %+v", w, st.Sessions)
	}
	tab.SessionID = w.SessionID
	must(t, d.handle(ctx, tab))
	must(t, d.handle(ctx, proto.Reparent{WorkspaceID: id, ParentTab: "new", Origin: origin}))
}

// A tab closed before its session became a part reopens in that session,
// not in a new one.
func TestReopenInStampedSession(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir}))
	session := d.state().Sessions[0].ID
	must(t, d.handle(ctx, proto.NewSession{SessionID: session, Cwd: dir, Cmd: []string{"sh"}}))
	gone := d.state().Workspaces[1].ID
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: gone}))
	must(t, d.handle(ctx, proto.SetOrigin{SessionID: session, Origin: model.Origin{Daemon: "here", Session: "s"}}))
	must(t, d.handle(ctx, proto.ReopenTab{ID: d.state().Closed[0].ID}))
	st := d.state()
	if len(st.Sessions) != 1 || len(st.Workspaces) != 2 || st.Workspaces[1].SessionID != session {
		t.Fatalf("reopened outside its session: sessions %+v, tabs %+v", st.Sessions, st.Workspaces)
	}
}

func TestWorktreeStartOnceConcurrent(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	add := o.AddWorktree
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	o.AddWorktree = func(ctx context.Context, root, name string, from model.WorktreeFrom) (string, string, error) {
		if calls.Add(1) != 1 {
			return "", "", errors.New("worktree already exists")
		}
		close(entered)
		<-release
		return add(ctx, root, name, from)
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: repo}))
	tab := proto.NewWorkspace{Root: repo, SessionID: d.state().Sessions[0].ID, Name: "feat", Cmd: []string{"sh"}, ID: "0123456789abcdef", ParentTab: "parent"}
	results := make(chan error, 2)
	go func() { results <- d.handle(ctx, tab) }()
	<-entered
	go func() { results <- d.handle(ctx, tab) }()
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	for range 2 {
		must(t, <-results)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d worktrees made for one start", n)
	}
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
	must(t, d.handle(ctx, proto.NewSession{SessionID: session, Cwd: dir, ParentPane: d.st.Panes[0].ID}))
	finished := d.st.Workspaces[2].ID
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: finished}))
	since := time.Now()
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{
		{ID: "w1", Host: "box", Parent: ws, Since: since},
		{ID: "w2", Host: "box", Parent: "someone-else", Since: since},
	}}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: ws}))
	i := slices.IndexFunc(d.st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == ws })
	c := d.st.Closed[i]
	must(t, d.handle(ctx, proto.ReopenTab{ID: c.ID}))
	st := d.state()
	if got := st.Workspace(child).Parent; got != c.ID {
		t.Fatalf("local child's parent %q, want %q", got, c.ID)
	}
	i = slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == finished })
	if st.Closed[i].Tab.Parent != c.ID {
		t.Fatal("a closed child's result did not follow its reopened parent")
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
	i = slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == id })
	reopened := st.Closed[i].Tab.ID
	must(t, d.handle(ctx, proto.ReopenTab{ID: st.Closed[i].ID}))
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
	if err := d.handle(ctx, proto.RemoteWorkers{SessionID: session, Add: []model.RemoteWorker{b}}); err == nil {
		t.Fatal("a different parent took the record")
	}
	st := d.state()
	if w := st.Session(session).Workers; len(w) != 1 || w[0].Parent != "p" {
		t.Fatalf("after two adds: %+v", w)
	}
	b.Lost = "never started"
	b.Parent = a.Parent
	must(t, d.handle(ctx, proto.RemoteWorkers{SessionID: session, Set: []model.RemoteWorker{b, {ID: "unknown", Host: "box", Parent: "p"}}}))
	st = d.state()
	if w := st.Session(session).Workers; len(w) != 1 || w[0].Parent != "p" || w[0].Lost == "" {
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
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: id}))
	if err := d.handle(ctx, proto.Reparent{WorkspaceID: id, ParentTab: "reopened", Origin: model.Origin{Daemon: "another", Session: "s"}}); err == nil {
		t.Fatal("another origin moved a closed worker")
	}
	must(t, d.handle(ctx, proto.Reparent{WorkspaceID: id, ParentTab: "reopened", Origin: o}))
	st = d.state()
	i := slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == id })
	if st.Closed[i].Tab.Parent != "reopened" {
		t.Fatal("a closed worker's result did not follow its parent")
	}
}

func TestAdoptLocalOrphan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parent  string
		remote  bool
		session string
		wantErr bool
	}{
		{"orphan", "gone", false, "s", false},
		{"live parent", "parent", false, "s", true},
		{"root", "", false, "s", true},
		{"other session", "gone", false, "other", true},
		{"remote", "gone", true, "s", true},
		{"cycle", "gone", false, "s", true},
		{"cycle through a closed tab", "gone", false, "s", true},
		{"closed worker", "gone", false, "s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakes{statsCalls: map[string]int{}}
			d := newDaemon(t, f.options())
			d.st.Workspaces = []model.Workspace{
				{ID: "worker", Parent: tc.parent, RemoteParent: tc.remote, SessionID: "s"},
				{ID: "parent", SessionID: tc.session},
			}
			switch tc.name {
			case "cycle":
				d.st.Workspaces[1].Parent = "worker"
			case "cycle through a closed tab":
				// Reopening the closed tab would make worker its own ancestor.
				d.st.Workspaces[1].Parent = "closed"
				d.st.Closed = []model.ClosedTab{{ID: "c", Tab: model.Workspace{ID: "closed", Parent: "worker", SessionID: "s"}}}
			case "closed worker":
				d.st.Closed = []model.ClosedTab{{ID: "c", Tab: d.st.Workspaces[0]}}
				d.st.Workspaces = d.st.Workspaces[1:]
			}
			err := d.handle(context.Background(), proto.Reparent{WorkspaceID: "worker", ParentTab: "parent"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("adopt: %v; want error %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				st := d.state()
				if w := st.Workspace("worker"); w.Parent != "parent" || w.RemoteParent {
					t.Fatalf("adopted worker: %+v", w)
				}
			}
		})
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

// pitwall adopt moves a gone session's part to another session of the
// same daemon, with its closed tabs, which can then be reparented. A part
// never moves to another daemon's session, or onto a session that has one.
func TestSetOriginAdopt(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	gone, next := model.Origin{Daemon: "here", Session: "gone"}, model.Origin{Daemon: "here", Session: "next"}
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir, Origin: gone}))
	st := d.state()
	part := st.PartOf(gone).ID
	must(t, d.handle(ctx, proto.NewSession{SessionID: part, Cwd: dir, Cmd: []string{"sh"}, ID: "0123456789abcdef", ParentTab: "old"}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: "0123456789abcdef"}))
	if d.handle(ctx, proto.SetOrigin{SessionID: part, Origin: model.Origin{Daemon: "there", Session: "next"}}) == nil {
		t.Fatal("a part moved to another daemon's session")
	}
	must(t, d.handle(ctx, proto.SessionNew{Name: "taken", Cwd: dir, Origin: model.Origin{Daemon: "here", Session: "taken"}}))
	if d.handle(ctx, proto.SetOrigin{SessionID: part, Origin: model.Origin{Daemon: "here", Session: "taken"}}) == nil {
		t.Fatal("a part moved onto a session that has one")
	}
	must(t, d.handle(ctx, proto.SetOrigin{SessionID: part, Origin: next}))
	st = d.state()
	if p := st.PartOf(next); p == nil || p.ID != part {
		t.Fatalf("part after the move: %+v", p)
	}
	must(t, d.handle(ctx, proto.Reparent{WorkspaceID: "0123456789abcdef", ParentTab: "adopter", Origin: next}))
	st = d.state()
	if i := slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == "0123456789abcdef" }); i < 0 || st.Closed[i].Tab.Parent != "adopter" {
		t.Fatalf("closed worker after adopt: %+v", st.Closed)
	}
}

// A refused start removes the worktree it made, and only a branch git made
// for it just now; a tab that opened the worktree meanwhile keeps it.
func TestWorktreeRefusedStartCleanup(t *testing.T) {
	for _, c := range []struct {
		name   string
		from   model.WorktreeFrom
		inUse  bool
		remove string
	}{
		{"new", model.WorktreeFrom{Kind: model.FromNew}, false, "branch"},
		{"remote", model.WorktreeFrom{Kind: model.FromRemote, Ref: "origin/fix"}, false, "branch"},
		{"branch", model.WorktreeFrom{Kind: model.FromBranch, Ref: "fix"}, false, "keep branch"},
		{"pr", model.WorktreeFrom{Kind: model.FromPR, PR: 7}, false, "keep branch"},
		{"in use", model.WorktreeFrom{Kind: model.FromNew}, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakes{statsCalls: map[string]int{}}
			o := f.options()
			start, add := o.StartPane, o.AddWorktree
			o.StartPane = func(cfg pane.Config) (Pane, error) {
				if len(cfg.Cmd) > 0 && cfg.Cmd[0] == "refused" {
					return nil, errors.New("guarded tab not started")
				}
				return start(cfg)
			}
			var d *Daemon
			o.AddWorktree = func(ctx context.Context, root, name string, from model.WorktreeFrom) (string, string, error) {
				path, branch, err := add(ctx, root, name, from)
				if c.inUse {
					must(t, d.handle(ctx, proto.NewSession{Cwd: path}))
				}
				return path, branch, err
			}
			var removed []string
			o.RemoveWorktree = func(_ context.Context, _, path string, branch, force bool) error {
				removed = append(removed, fmt.Sprintf("%s %v %v", filepath.Base(path), branch, force))
				return nil
			}
			d = newDaemon(t, o)
			ctx := context.Background()
			repo := filepath.Join(t.TempDir(), "repo")
			mkdir(t, repo)
			must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: repo}))
			n := len(d.state().Workspaces)
			id := "0123456789abcdef"
			err := d.handle(ctx, proto.NewWorkspace{Root: repo, SessionID: d.state().Sessions[0].ID, Name: "feat", From: c.from, Cmd: []string{"refused"}, ID: id})
			if err == nil || !strings.Contains(err.Error(), "guarded tab not started") {
				t.Fatalf("err %v", err)
			}
			if tabs, _ := count(d.state(), id); tabs != 0 {
				t.Fatalf("refused start kept its tab: %+v", d.state().Workspaces)
			}
			want := map[string][]string{"branch": {"feat true true"}, "keep branch": {"feat false true"}}[c.remove]
			if c.inUse {
				if len(d.state().Workspaces) != n+1 || !strings.Contains(err.Error(), "worktree kept") {
					t.Fatalf("in-use worktree: %v; tabs %+v", err, d.state().Workspaces)
				}
			}
			if !slices.Equal(removed, want) {
				t.Fatalf("removed %q, want %q", removed, want)
			}
		})
	}
}
