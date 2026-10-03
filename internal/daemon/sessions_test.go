package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestFirstSessionOnHello(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	cwd := t.TempDir()
	st := dialIn(t, sock, "gui", cwd).waitState("first state", func(model.State) bool { return true })
	if len(st.Workspaces) != 1 || len(st.Panes) != 1 {
		t.Fatalf("first state should hold one session with one pane: %+v", st)
	}
	w, p := st.Workspaces[0], st.Panes[0]
	if w.Path != cwd || w.Name != "" || w.NameSet || w.ProjectID != "" || len(w.Tabs) != 1 || w.ActiveTab != w.Tabs[0].ID || lay(w).Pane != p.ID || p.Cwd != cwd || len(p.Cmd) != 0 || f.pane(0).cfg.Cwd != cwd {
		t.Fatalf("session %+v pane %+v", w, p)
	}

	st = dialIn(t, sock, "gui", t.TempDir()).waitState("second hello", func(model.State) bool { return true })
	if len(st.Workspaces) != 1 {
		t.Fatalf("second Hello opened another session: %+v", st.Workspaces)
	}
	stop()

	sock, stop = run(t, f)
	defer stop()
	st = dialIn(t, sock, "gui", t.TempDir()).waitState("restored", func(model.State) bool { return true })
	if len(st.Workspaces) != 1 || len(st.Panes) != 1 {
		t.Fatalf("restore opened another session: %+v", st)
	}
}

func TestHelloCwdFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	st := dialIn(t, sock, "gui", filepath.Join(home, "missing")).waitState("first state", func(model.State) bool { return true })
	if len(st.Workspaces) != 1 || st.Workspaces[0].Path != home {
		t.Fatalf("session: %+v", st.Workspaces)
	}
}

func TestSessionsAndGroups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "api")
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, dir)
	mkdir(t, repo)
	for _, c := range []string{dir, dir, dir, "", repo} {
		must(t, d.handle(ctx, proto.NewSession{Cwd: c}))
	}
	if err := d.handle(ctx, proto.NewSession{Cwd: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("NewSession in a missing directory succeeded")
	}
	ws := func() []model.Workspace { d.mu.Lock(); defer d.mu.Unlock(); return d.snapshot().Workspaces }
	var branches []string
	for _, w := range ws() {
		branches = append(branches, w.Branch)
	}
	if want := []string{"", "", "", "", "repo"}; !slices.Equal(branches, want) {
		t.Fatalf("branches %v, want %v", branches, want)
	}
	if len(d.st.Panes) != 5 {
		t.Fatalf("every session should open a shell: %d panes", len(d.st.Panes))
	}
	a, b, r := ws()[0].ID, ws()[1].ID, ws()[4].ID

	must(t, d.handle(ctx, proto.NewGroup{WorkspaceIDs: []string{a, b}}))
	g := d.st.Projects[0]
	if g.Name != "Group 1" || g.Kind != model.ProjectGroup || g.Root != "" || ws()[0].ProjectID != g.ID || ws()[1].ProjectID != g.ID || ws()[2].ProjectID != "" {
		t.Fatalf("group %+v sessions %+v", g, ws())
	}
	must(t, d.handle(ctx, proto.NewGroup{Name: "empty"}))
	if d.st.Projects[1].Name != "empty" {
		t.Fatalf("named group: %+v", d.st.Projects[1])
	}
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, GroupID: g.ID}))
	if w := ws()[5]; w.ProjectID != g.ID {
		t.Fatalf("grouped session: %+v", w)
	}
	must(t, d.handle(ctx, proto.SetSessionGroup{WorkspaceID: a}))
	if ws()[0].ProjectID != "" {
		t.Fatal("SetSessionGroup \"\" did not ungroup")
	}
	if d.handle(ctx, proto.SetSessionGroup{WorkspaceID: a, GroupID: "nope"}) == nil || d.handle(ctx, proto.NewGroup{WorkspaceIDs: []string{"nope"}}) == nil {
		t.Fatal("unknown group or session accepted")
	}
	must(t, d.handle(ctx, proto.RenameGroup{GroupID: g.ID, Name: "agents"}))
	if d.st.Projects[0].Name != "agents" {
		t.Fatalf("rename: %+v", d.st.Projects[0])
	}

	must(t, d.handle(ctx, proto.DeleteGroup{GroupID: g.ID}))
	if len(d.st.Projects) != 1 || ws()[1].ProjectID != "" || ws()[5].ProjectID != "" || len(d.st.Panes) != 6 || lay(ws()[1]) == nil {
		t.Fatalf("delete group: projects %+v sessions %+v", d.st.Projects, ws())
	}
	for i := range 6 {
		p := f.pane(i)
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			t.Fatalf("pane %d closed", i)
		}
	}

	// The stats loop covers ungrouped sessions in a repo, by their own path.
	d.refreshStats(ctx, "")
	// why: refreshStats goroutines from NewSession may still be writing statsCalls.
	f.mu.Lock()
	calls := f.statsCalls[repo]
	f.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.st.Stats) != 1 || d.st.Stats[r].Additions != 5 || calls == 0 {
		t.Fatalf("stats %+v calls %d", d.st.Stats, calls)
	}
}

// A session grouped into a git project after the fact is not a worktree
// pitwall made, so deleting it must not ask git to remove one.
func TestDeleteGroupedSessionKeepsDirectory(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	removed := 0
	o.RemoveWorktree = func(context.Context, string, string, bool) error { removed++; return nil }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir(), GroupID: d.st.Projects[0].ID}))
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: d.st.Workspaces[0].ID}))
	// A worktree the user made by hand where pitwall puts its own, grouped in
	// later, is not pitwall's to remove.
	handmade := filepath.Join(repo, ".worktrees", "handmade")
	mkdir(t, handmade)
	must(t, d.handle(ctx, proto.NewSession{Cwd: handmade}))
	must(t, d.handle(ctx, proto.SetSessionGroup{WorkspaceID: d.st.Workspaces[0].ID, GroupID: d.st.Projects[0].ID}))
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: d.st.Workspaces[0].ID}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.st.Projects[0].ID, Name: "feat"}))
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: d.st.Workspaces[0].ID}))
	if removed != 1 {
		t.Fatalf("RemoveWorktree ran %d times, want only for the pitwall worktree", removed)
	}
}

func TestNewSessionFromPaneCwd(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start, there := t.TempDir(), t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: start}))
	pane := d.st.Panes[0].ID
	// The shell cd'd since the session opened; the fake reports cfg.Cwd.
	d.panes[pane].(*fakePane).cfg.Cwd = there
	must(t, d.handle(ctx, proto.NewSession{Cwd: start, FromPane: pane}))
	if got := d.st.Workspaces[1].Path; got != there {
		t.Fatalf("new session in %s, want the pane's current %s", got, there)
	}
	must(t, d.handle(ctx, proto.NewSession{Cwd: start, FromPane: "gone"}))
	if got := d.st.Workspaces[2].Path; got != start {
		t.Fatalf("unknown pane: session in %s, want Cwd %s", got, start)
	}
}

// A hand-made worktree grouped into its repo must stay the user's across a
// restart: ownership is recorded, never re-inferred from the path.
func TestHandmadeWorktreeSurvivesRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	removed := 0
	o.RemoveWorktree = func(context.Context, string, string, bool) error { removed++; return nil }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	handmade := filepath.Join(repo, ".worktrees", "handmade")
	mkdir(t, handmade)
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: handmade, GroupID: d.st.Projects[0].ID}))
	id := d.st.Workspaces[0].ID
	d.shutdown()

	d, err = NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: id}))
	if removed != 0 {
		t.Fatal("restart made pitwall own a worktree the user created")
	}
}
