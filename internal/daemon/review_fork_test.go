package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// codex --cd other, launched from dir: the agent's working root is other.
func TestRevCodexCd(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir, other := t.TempDir(), t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Cmd: []string{"codex", "--cd", other}}))
	d.mu.Lock()
	src := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderCodex, "s"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: src}))
	cfg := f.pane(1).cfg
	t.Logf("fork cwd=%s cmd=%q (source working root %s)", cfg.Cwd, cfg.Cmd, other)
	if cfg.Cwd != other {
		t.Errorf("fork runs in %s, source agent in %s", cfg.Cwd, other)
	}
}

// A worktree fork gives Codex an explicit directory selector.
func TestRevCodexWorktreeDirectory(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "commit", "--allow-empty", "-m", "init")
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RepoRoot, o.AddWorktree, o.Branch = gitstat.RepoRoot, gitstat.AddWorktree, gitBranch
	d := newDaemon(t, o)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: repo, Cmd: []string{"codex", "--model", "m"}}))
	d.mu.Lock()
	src := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderCodex, "s"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: src, Worktree: "side"}))
	cfg := f.pane(1).cfg
	t.Logf("fork cwd=%s cmd=%q", cfg.Cwd, cfg.Cmd)
	if !slices.Equal(cfg.Cmd, []string{"codex", "fork", "--model", "m", "--cd", cfg.Cwd, "s"}) {
		t.Fatalf("worktree command %q", cfg.Cmd)
	}
}

// Same-folder fork of a pitwall worktree tab, then delete the source tab.
func TestRevDeleteSourceKeepsForkFolder(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "commit", "--allow-empty", "-m", "init")
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RepoRoot, o.AddWorktree, o.Branch, o.RemoveWorktree = gitstat.RepoRoot, gitstat.AddWorktree, gitBranch, gitstat.RemoveWorktree
	d := newDaemon(t, o)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: repo, Cmd: []string{"claude"}}))
	d.mu.Lock()
	first := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "s"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: first, Worktree: "side", Name: "wt"}))
	d.mu.Lock()
	wt := d.st.Panes[1].ID
	d.st.Panes[1].Provider, d.st.Panes[1].SessionID = model.ProviderClaude, "t"
	wtab := d.st.Panes[1].WorkspaceID
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: wt, Name: "same-folder"}))
	st := d.state()
	var path string
	for _, w := range st.Workspaces {
		t.Logf("tab %q path=%s root=%q", w.Name, w.Path, w.WorktreeRoot)
		if w.Name == "same-folder" {
			path = w.Path
		}
	}
	// The shared folder stays until removal is forced.
	if err := d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: wtab}); err == nil || !strings.Contains(err.Error(), "same-folder") {
		t.Fatalf("shared worktree deletion: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("deleting the source tab removed the running fork's folder: %v", err)
	}

	if r := d.worktreeQuery(ctx, proto.WorktreeQuery{WorkspaceID: wtab}); !slices.Equal(r.UsedBy, []string{"same-folder"}) {
		t.Fatalf("shared query %+v", r)
	}
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: wtab, Force: true}))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("forced removal: %v", err)
	}
}

// A pane whose tab is gone returns an error without keeping d.mu locked.
func TestRevOrphanPaneError(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	d.mu.Lock()
	d.st.Panes = append(d.st.Panes, model.Pane{ID: "orphan", WorkspaceID: "gone", Provider: model.ProviderClaude, SessionID: "s"})
	d.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic: %v", r)
			d.mu.TryLock()
			d.mu.Unlock()
		}
	}()
	if err := d.handle(context.Background(), proto.ForkPane{Pane: "orphan"}); err == nil || !strings.Contains(err.Error(), "has no tab") {
		t.Fatalf("orphan: %v", err)
	}
}

// The folder the pane last reported is gone but the tab's folder is not.
func TestRevDeletedCwd(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Cmd: []string{"claude"}}))
	d.mu.Lock()
	src := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "s"
	d.st.Panes[0].Cwd = filepath.Join(dir, "removed")
	f.pane(0).cfg.Cwd = filepath.Join(dir, "removed")
	d.mu.Unlock()
	err := d.handle(ctx, proto.ForkPane{Pane: src})
	if err != nil || len(d.state().Workspaces) != 2 || d.state().Workspaces[1].Path != dir {
		t.Fatalf("fallback: %v, %+v", err, d.state().Workspaces)
	}
}

// Ungrouped source with a parent: numbering and top order.
func TestRevUngroupedParent(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Loose: true}))
	parent := d.state().Panes[0].ID
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, ParentPane: parent, Loose: true, Cmd: []string{"claude"}, Name: "kid"}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Loose: true, Name: "later"}))
	d.mu.Lock()
	src := d.st.Panes[1].ID
	d.st.Panes[1].Provider, d.st.Panes[1].SessionID = model.ProviderClaude, "s"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: src, Name: "fork"}))
	st := d.state()
	for _, w := range st.Workspaces {
		t.Logf("tab %q id=%s parent=%q project=%q", w.Name, w.ID, w.Parent, w.ProjectID)
	}
	if fork := st.Workspaces[2]; fork.Parent != "" || fork.Name != "fork" {
		t.Fatalf("menu fork %+v", fork)
	}
	t.Logf("top=%v", st.TopOrder(st.Workspaces[0].SessionID))
}
