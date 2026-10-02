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
	if w.Path != cwd || w.Name != filepath.Base(cwd) || w.ProjectID != "" || w.Layout.Pane != p.ID || p.Cwd != cwd || len(p.Cmd) != 0 || f.pane(0).cfg.Cwd != cwd {
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
	if len(st.Workspaces) != 1 || st.Workspaces[0].Path != home || st.Workspaces[0].Name != "~" {
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
	var names []string
	for _, w := range ws() {
		names = append(names, w.Name+"|"+w.Branch)
	}
	if want := []string{"api|", "api 2|", "api 3|", "~|", "repo|repo"}; !slices.Equal(names, want) {
		t.Fatalf("names %v, want %v", names, want)
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
	if w := ws()[5]; w.ProjectID != g.ID || w.Name != "api 4" {
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
	if len(d.st.Projects) != 1 || ws()[1].ProjectID != "" || ws()[5].ProjectID != "" || len(d.st.Panes) != 6 || ws()[1].Layout == nil {
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
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.st.Stats) != 1 || d.st.Stats[r].Additions != 5 || f.statsCalls[repo] == 0 {
		t.Fatalf("stats %+v calls %+v", d.st.Stats, f.statsCalls)
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
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.st.Projects[0].ID, Name: "feat"}))
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: d.st.Workspaces[0].ID}))
	if removed != 1 {
		t.Fatalf("RemoveWorktree ran %d times, want only for the pitwall worktree", removed)
	}
}
