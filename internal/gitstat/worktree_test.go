package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// githubRepo is repo with an origin whose URL names github.com but which
// git fetches from a local bare repository, through url.insteadOf. Origin
// has main, side, remote-only, and pull/7/head and pull/9/head, a commit
// on no branch.
func githubRepo(t *testing.T) (dir, bare, pr string) {
	t.Helper()
	dir = repo(t)
	bare = filepath.Join(t.TempDir(), "origin.git")
	runGit(t, dir, "init", "--bare", bare)
	runGit(t, dir, "remote", "add", "origin", "https://github.com/acme/app.git")
	runGit(t, dir, "config", "url."+bare+".insteadOf", "https://github.com/acme/app.git")
	runGit(t, dir, "branch", "side")
	runGit(t, dir, "checkout", "-b", "remote-only")
	writeFile(t, dir, "remote.txt", "remote\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "-b", "pr-head")
	writeFile(t, dir, "pr.txt", "pr\n")
	commit(t, dir)
	pr = runGit(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "push", "origin", "main", "side", "remote-only", "pr-head:refs/pull/7/head", "pr-head:refs/pull/9/head")
	runGit(t, dir, "branch", "-D", "remote-only", "pr-head")
	runGit(t, dir, "fetch", "origin")
	return dir, bare, pr
}

func TestWorktreeFrom(t *testing.T) {
	ctx := context.Background()
	dir, bare, pr := githubRepo(t)
	main := runGit(t, dir, "rev-parse", "main")
	runGit(t, dir, "checkout", "side")
	writeFile(t, dir, "side.txt", "side\n")
	commit(t, dir)
	side := runGit(t, dir, "rev-parse", "side")
	runGit(t, dir, "checkout", "main")
	remote := runGit(t, dir, "rev-parse", "origin/remote-only")

	refs, err := ListRefs(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Default != "origin/main" || !refs.GitHub || !slices.Equal(refs.Local, []string{"main", "side"}) ||
		!slices.Equal(refs.Remote, []string{"origin/main", "origin/remote-only", "origin/side"}) {
		t.Fatalf("ListRefs = %+v", refs)
	}

	for _, c := range []struct {
		name       string
		from       model.WorktreeFrom
		branch     string
		head       string
		upstream   string // "" for none
		wantFailed bool
	}{
		{name: "fresh", from: model.WorktreeFrom{}, branch: "fresh", head: main},
		{name: "off side", from: model.WorktreeFrom{Ref: "side"}, branch: "off-side", head: side},
		{name: "off remote", from: model.WorktreeFrom{Ref: "origin/remote-only"}, branch: "off-remote", head: remote},
		{name: "side", from: model.WorktreeFrom{Kind: model.FromBranch, Ref: "side"}, branch: "side", head: side},
		{name: "tracked", from: model.WorktreeFrom{Kind: model.FromRemote, Ref: "origin/remote-only"}, branch: "remote-only", head: remote, upstream: "origin/remote-only"},
		{name: "pr 7", from: model.WorktreeFrom{Kind: model.FromPR, PR: 7}, branch: "pr-7", head: pr},
		{name: "no branch", from: model.WorktreeFrom{Kind: model.FromBranch, Ref: "missing"}, wantFailed: true},
		{name: "remote as local", from: model.WorktreeFrom{Kind: model.FromBranch, Ref: "remote-only"}, wantFailed: true},
		{name: "no remote", from: model.WorktreeFrom{Kind: model.FromRemote, Ref: "origin/missing"}, wantFailed: true},
		{name: "no pr", from: model.WorktreeFrom{Kind: model.FromPR, PR: 8}, wantFailed: true},
		{name: "option", from: model.WorktreeFrom{Ref: "-q"}, wantFailed: true}, // git would read a flag and branch off HEAD
	} {
		path, branch, err := AddWorktree(ctx, dir, c.name, c.from)
		if c.wantFailed {
			if err == nil {
				t.Errorf("%s: made %s on %s", c.name, path, branch)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if branch != c.branch || filepath.Base(path) != Slug(c.name) {
			t.Errorf("%s: branch %q at %s", c.name, branch, path)
		}
		if got := runGit(t, path, "rev-parse", "HEAD"); got != c.head {
			t.Errorf("%s: HEAD %s, want %s", c.name, got, c.head)
		}
		if got := runGit(t, path, "branch", "--show-current"); got != c.branch {
			t.Errorf("%s: checked out %q", c.name, got)
		}
		up, _ := git(ctx, path, "rev-parse", "--abbrev-ref", "@{upstream}")
		if up = strings.TrimSpace(up); up != c.upstream {
			t.Errorf("%s: upstream %q, want %q", c.name, up, c.upstream)
		}
	}

	runGit(t, dir, "remote", "set-url", "origin", bare) // fetchable, but not GitHub
	if GitHub(ctx, dir) {
		t.Fatal("a local origin counts as GitHub")
	}
	if _, _, err := AddWorktree(ctx, dir, "pr", model.WorktreeFrom{Kind: model.FromPR, PR: 9}); err == nil {
		t.Fatal("a pull request from an origin off GitHub")
	}
}

func TestExcludeWorktrees(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "file.txt", "one\n")
	commit(t, dir)
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.WriteFile(exclude, []byte("*.log"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if _, _, err := AddWorktree(ctx, dir, name, model.WorktreeFrom{}); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(exclude); err != nil || string(b) != "*.log\n/.worktrees/\n" {
		t.Fatalf("exclude = %q, %v", b, err)
	}
	if got := runGit(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("status lists %q", got)
	}

	// A .gitignore that covers .worktrees already is enough.
	ignored := repo(t)
	before, _ := os.ReadFile(filepath.Join(ignored, ".git", "info", "exclude"))
	if _, _, err := AddWorktree(ctx, ignored, "one", model.WorktreeFrom{}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(ignored, ".git", "info", "exclude")); string(after) != string(before) {
		t.Fatalf("exclude changed to %q", after)
	}
}

func TestRemoveDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	path, branch, err := AddWorktree(ctx, dir, "dirty", model.WorktreeFrom{})
	if err != nil {
		t.Fatal(err)
	}
	if merged, err := Merged(ctx, dir, branch); err != nil || !merged {
		t.Fatalf("new branch Merged = %v, %v", merged, err)
	}
	writeFile(t, path, "work.txt", "committed\n")
	commit(t, path)
	writeFile(t, path, "file.txt", "changed\n")
	writeFile(t, path, "new.txt", "untracked\n")
	if got, err := Status(ctx, path); err != nil || !slices.Equal(got, []string{"file.txt", "new.txt"}) {
		t.Fatalf("Changed = %q, %v", got, err)
	}
	if merged, err := Merged(ctx, dir, branch); err != nil || merged {
		t.Fatalf("branch with a commit Merged = %v, %v", merged, err)
	}
	if err := RemoveWorktree(ctx, dir, path, true, false); err == nil {
		t.Fatal("removed a worktree with changes")
	}
	if _, err := os.Stat(filepath.Join(path, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(ctx, dir, path, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
	if hasRef(ctx, dir, "refs/heads/"+branch) {
		t.Fatal("force kept the unmerged branch")
	}
	for _, p := range []string{dir, "."} {
		if err := RemoveWorktree(ctx, dir, p, false, true); err == nil {
			t.Fatalf("removed the main worktree as %q", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestOrphans(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	var paths []string
	for _, name := range []string{"used", "clean", "dirty", "gone"} {
		path, _, err := AddWorktree(ctx, dir, name, model.WorktreeFrom{})
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	runGit(t, dir, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "elsewhere"))
	writeFile(t, paths[2], "new.txt", "untracked\n")
	if err := os.RemoveAll(paths[3]); err != nil {
		t.Fatal(err)
	}
	if err := Prune(ctx, dir); err != nil {
		t.Fatal(err)
	}
	got, err := Orphans(ctx, dir, func(p string) bool { return p == paths[0] })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != paths[1] || got[0].Dirty || got[0].Branch != "clean" || got[0].Root != dir ||
		got[1].Path != paths[2] || !got[1].Dirty || got[0].Committed.IsZero() {
		t.Fatalf("Orphans = %+v", got)
	}
}
