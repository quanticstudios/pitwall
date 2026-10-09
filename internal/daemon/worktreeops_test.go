package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-C", dir, "-c", "user.name=Daemon Test", "-c", "user.email=daemon@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The delete dialog learns what a worktree would lose, a dirty one is
// only removed with Force, and the cleanup dialog lists, and deletes, a
// worktree no tab uses, against a real repo.
func TestWorktreeQueries(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.AddWorktree, o.RemoveWorktree, o.Orphans = gitstat.AddWorktree, gitstat.RemoveWorktree, pruneOrphans
	d := newDaemon(t, o)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "commit", "--allow-empty", "-m", "init")
	gitIn(t, repo, "branch", "side")
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	pid := d.state().Projects[0].ID

	refs := d.worktreeQuery(ctx, proto.WorktreeQuery{ProjectID: pid})
	if refs.Err != "" || refs.Default != "main" || !slices.Equal(refs.Local, []string{"main", "side"}) || refs.GitHub {
		t.Fatalf("refs %+v", refs)
	}
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, From: model.WorktreeFrom{Kind: model.FromBranch, Ref: "side"}}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "feat", Cmd: []string{"make", "test"}}))
	st := d.state()
	side, feat := st.Workspaces[1], st.Workspaces[2]
	if side.Name != "side" || side.Branch != "side" || side.Path != filepath.Join(repo, ".worktrees", "side") || len(side.Tabs) != 0 {
		t.Fatalf("branch tab %+v", side)
	}
	if p := f.pane(1); len(feat.Tabs) != 1 || !slices.Equal(p.cfg.Cmd, []string{"make", "test"}) || p.cfg.Cwd != feat.Path || !st.Panes[1].Held {
		t.Fatalf("command tab %+v runs %q in %s", feat, p.cfg.Cmd, p.cfg.Cwd)
	}

	check := func(want proto.WorktreeInfo) {
		t.Helper()
		want.Query = proto.WorktreeQuery{WorkspaceID: feat.ID}
		if got := d.worktreeQuery(ctx, want.Query); !slices.Equal(got.Changed, want.Changed) || got.Unmerged != want.Unmerged || got.Err != "" {
			t.Fatalf("check %+v, want %+v", got, want)
		}
	}
	check(proto.WorktreeInfo{})
	if err := os.WriteFile(filepath.Join(feat.Path, "notes.txt"), []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check(proto.WorktreeInfo{Changed: []string{"notes.txt"}})
	gitIn(t, feat.Path, "commit", "--allow-empty", "-m", "work")
	check(proto.WorktreeInfo{Changed: []string{"notes.txt"}, Unmerged: true})

	if err := d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: feat.ID, RemoveBranch: true}); err == nil {
		t.Fatal("deleted a worktree with changes")
	}
	if _, err := os.Stat(filepath.Join(feat.Path, "notes.txt")); err != nil || len(d.state().Workspaces) != 3 {
		t.Fatalf("a refused delete lost the tab or the file: %v", err)
	}
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: feat.ID, RemoveBranch: true, Force: true}))
	if _, err := os.Stat(feat.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
	if out := gitIn(t, repo, "branch", "--list", "feat"); out != "" {
		t.Fatalf("unmerged branch kept under Force: %q", out)
	}

	stray, _, err := gitstat.AddWorktree(ctx, repo, "stray", model.WorktreeFrom{})
	if err != nil {
		t.Fatal(err)
	}
	got := d.worktreeQuery(ctx, proto.WorktreeQuery{Orphans: true})
	if got.Err != "" || len(got.Orphans) != 1 || got.Orphans[0].Path != stray || got.Orphans[0].Root != repo || got.Orphans[0].Dirty {
		t.Fatalf("orphans %+v", got)
	}
	d.noteOrphans(ctx)
	if n := d.state().Notice; !strings.HasPrefix(n, "A worktree under .worktrees has no tab.") {
		t.Errorf("notice %q", n)
	}
	for _, bad := range []proto.DeleteWorktree{
		{Root: filepath.Dir(repo), Path: stray}, // no group there
		{Root: repo, Path: repo},                // the main worktree
		{Root: repo, Path: side.Path},           // a tab's
		{Root: repo, Path: filepath.Dir(stray)}, // .worktrees itself
	} {
		if err := d.handle(ctx, bad); err == nil {
			t.Errorf("%+v deleted", bad)
		}
	}
	must(t, d.handle(ctx, proto.DeleteWorktree{Root: repo, Path: stray}))
	if got := d.worktreeQuery(ctx, proto.WorktreeQuery{Orphans: true}); len(got.Orphans) != 0 {
		t.Fatalf("orphans after delete %+v", got.Orphans)
	}
}
