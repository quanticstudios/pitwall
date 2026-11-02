package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

// closedDaemon is a daemon that resumes panes with store.RestoreCmd, and
// one tab in a group of session "work", in dir, whose pane runs Claude
// Code session "abc" in plan mode.
func closedDaemon(t *testing.T, dir string) (*Daemon, *fakes, string) {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RestoreCmd = store.RestoreCmd
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.SessionNew{Name: "work", Cwd: dir}))
	ws := d.st.Workspaces[0].ID
	must(t, d.handle(ctx, proto.NewGroup{Name: "agents", WorkspaceIDs: []string{ws}}))
	must(t, d.handle(ctx, proto.RenameWorkspace{WorkspaceID: ws, Name: "fix auth"}))
	d.mu.Lock()
	p := &d.st.Panes[0]
	p.Provider, p.SessionID, p.AgentMode, p.Prompt = model.ProviderClaude, "abc", "plan", "fix the login"
	d.mu.Unlock()
	return d, f, ws
}

// lastCmd is the command the newest pane started with.
func (f *fakes) lastCmd() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes[len(f.panes)-1].cfg.Cmd
}

// TestReopenClosedTab records a tab that closes with its last pane, and
// reopens it in its session and group, which ended with it and come back by
// name, its agent resumed as a restart would.
func TestReopenClosedTab(t *testing.T) {
	dir := t.TempDir()
	d, f, ws := closedDaemon(t, dir)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.ClosePane{Pane: d.st.Panes[0].ID}))
	if len(d.st.Workspaces) != 0 || len(d.st.Sessions) != 0 || len(d.st.Closed) != 1 {
		t.Fatalf("after close: %d tabs, %d sessions, %d closed", len(d.st.Workspaces), len(d.st.Sessions), len(d.st.Closed))
	}
	c := d.st.Closed[0]
	if c.Tab.ID != ws || c.Session != "work" || c.Group.Name != "agents" || c.Pane.SessionID != "abc" || c.Folder() != dir || c.Gone || c.ClosedAt.IsZero() {
		t.Fatalf("record: %+v", c)
	}

	must(t, d.handle(ctx, proto.ReopenTab{ID: c.ID}))
	w := d.st.Workspace(c.ID)
	if w == nil || len(d.st.Closed) != 0 {
		t.Fatalf("reopen: tab %v, %d closed left", w, len(d.st.Closed))
	}
	s, g := d.st.Session(w.SessionID), d.project(w.ProjectID)
	if s == nil || s.Name != "work" || g == nil || g.Name != "agents" || w.Name != "fix auth" || !w.NameSet || w.Path != dir {
		t.Fatalf("reopened tab: %+v in %+v, %+v", w, s, g)
	}
	want := store.RestoreCmd(model.Pane{Provider: model.ProviderClaude, SessionID: "abc", AgentMode: "plan"})
	if got := f.lastCmd(); !slices.Equal(got, want) || !slices.Contains(got, "abc") {
		t.Fatalf("resume command %q, want %q", got, want)
	}
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == c.ID })
	if p := d.st.Panes[i]; p.SessionID != "abc" || p.Provider != model.ProviderClaude || p.Held {
		t.Fatalf("reopened pane: %+v", p)
	}
	if _, ok := d.resumed[d.st.Panes[i].ID]; !ok {
		t.Fatal("reopened agent is not marked resumed, so its exit would not give back a shell")
	}
	if err := d.handle(ctx, proto.ReopenTab{ID: c.ID}); err == nil {
		t.Fatal("reopened the same record twice")
	}
}

// TestReopenMissingWorktree reports a closed tab whose worktree is gone,
// and opens it in the repository it came from when asked.
func TestReopenMissingWorktree(t *testing.T) {
	repo := t.TempDir()
	tree := filepath.Join(repo, ".worktrees", "fix")
	must(t, os.MkdirAll(tree, 0o755))
	d, f, ws := closedDaemon(t, tree)
	ctx := context.Background()
	d.mu.Lock()
	d.st.Workspace(ws).WorktreeRoot = repo
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: ws}))
	must(t, os.RemoveAll(tree))
	d.checkClosed()
	c := d.st.Closed[0]
	if !c.Gone || c.Root() != repo {
		t.Fatalf("record: gone %v, root %q", c.Gone, c.Root())
	}
	if err := d.handle(ctx, proto.ReopenTab{ID: c.ID}); err == nil || len(d.st.Closed) != 1 {
		t.Fatalf("reopened into a missing folder: %v", err)
	}
	must(t, d.handle(ctx, proto.ReopenTab{ID: c.ID, AtRoot: true}))
	w := d.st.Workspace(c.ID)
	if w == nil || w.Path != repo || w.WorktreeRoot != "" || len(d.st.Closed) != 0 {
		t.Fatalf("at root: %+v, %d closed", w, len(d.st.Closed))
	}
	if got := f.lastCmd(); !slices.Contains(got, "abc") {
		t.Fatalf("resume at root: %q", got)
	}
}

// TestReopenShell brings back a tab whose pane ran a command, not an
// agent's resume, as a shell in its folder, and forgets a record on request.
func TestReopenShell(t *testing.T) {
	dir := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Cmd: []string{"gh", "pr", "merge"}}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: d.st.Workspaces[0].ID}))
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: d.st.Workspaces[0].ID}))
	if len(d.st.Closed) != 2 || d.st.Closed[0].Pane.Cmd[0] != "gh" {
		t.Fatalf("closed: %+v", d.st.Closed)
	}
	must(t, d.handle(ctx, proto.ReopenTab{ID: d.st.Closed[0].ID}))
	if got := f.lastCmd(); len(got) != 0 {
		t.Fatalf("a closed command tab ran %q again", got)
	}
	if p := d.st.Panes[len(d.st.Panes)-1]; p.Held || len(p.Cmd) != 0 || p.Cwd != dir {
		t.Fatalf("reopened shell: %+v", p)
	}
	must(t, d.handle(ctx, proto.ForgetTab{ID: d.st.Closed[0].ID}))
	if len(d.st.Closed) != 0 {
		t.Fatalf("forget left %d", len(d.st.Closed))
	}
	if err := d.handle(ctx, proto.ForgetTab{ID: "nope"}); err == nil {
		t.Fatal("no error for a missing record")
	}
}

// TestClosedSurvivesRestart keeps State.Closed in the saved state.
func TestClosedSurvivesRestart(t *testing.T) {
	d, f, ws := closedDaemon(t, t.TempDir())
	must(t, d.handle(context.Background(), proto.KillSession{WorkspaceID: ws}))
	d.save()
	d2, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.st.Closed) != 1 || d2.st.Closed[0].Pane.SessionID != "abc" {
		t.Fatalf("after restart: %+v", d2.st.Closed)
	}
}
