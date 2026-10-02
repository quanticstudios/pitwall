package gitstat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "-c", "user.name=Gitstat Test", "-c", "user.email=gitstat@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Test change")
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "file.txt", "one\ntwo\nthree\n")
	writeFile(t, dir, ".gitignore", ".worktrees/\n")
	commit(t, dir)
	return dir
}

func checkStats(t *testing.T, dir string, want model.BranchStats) {
	t.Helper()
	got, err := Stats(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Stats = %+v, want %+v", got, want)
	}
}

func TestStats(t *testing.T) {
	dir := repo(t)
	checkStats(t, dir, model.BranchStats{MergeStatus: model.MergeUpToDate})
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "file.txt", "one\nthree\nfour\nfive\n")
	commit(t, dir)
	writeFile(t, dir, "staged.txt", "staged\n")
	runGit(t, dir, "add", "staged.txt")
	writeFile(t, dir, "file.txt", "one\nfour\nfive\nsix\n")
	writeFile(t, dir, "untracked.txt", "not in git diff\n")
	// Committed: +2/-1. Staged and unstaged against HEAD: +2/-1.
	checkStats(t, dir, model.BranchStats{Additions: 4, Deletions: 2, Ahead: 1, MergeStatus: model.MergeClean})
	// A default-branch commit is excluded from the merge-base diff.
	runGit(t, dir, "stash", "push", "--include-untracked")
	runGit(t, dir, "checkout", "main")
	writeFile(t, dir, "base.txt", "default only\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "feature")
	checkStats(t, dir, model.BranchStats{Additions: 2, Deletions: 1, Ahead: 1, Behind: 1, MergeStatus: model.MergeClean})
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "branch", "behind", "HEAD~1")
	runGit(t, dir, "checkout", "behind")
	checkStats(t, dir, model.BranchStats{Behind: 1, MergeStatus: model.MergeClean})
}

func TestMergeConflict(t *testing.T) {
	dir := repo(t)
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "file.txt", "feature\ntwo\nthree\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "main")
	writeFile(t, dir, "file.txt", "main\ntwo\nthree\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "feature")
	checkStats(t, dir, model.BranchStats{Additions: 1, Deletions: 1, Ahead: 1, Behind: 1, MergeStatus: model.MergeConflicts})
	if out := runGit(t, dir, "status", "--porcelain"); out != "" {
		t.Fatalf("merge-tree changed worktree: %s", out)
	}
}

func TestDefaultRef(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	runGit(t, dir, "checkout", "-b", "feature")
	for _, tc := range []struct {
		name, want string
		setup      func()
	}{
		{"main", "refs/heads/main", func() {}},
		{"master", "refs/heads/master", func() { runGit(t, dir, "branch", "-m", "main", "master") }},
		{"current", "refs/heads/feature", func() { runGit(t, dir, "branch", "-D", "master") }},
		{"remote main", "refs/remotes/origin/main", func() { runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD") }},
		{"origin HEAD", "refs/remotes/origin/trunk", func() {
			runGit(t, dir, "update-ref", "refs/remotes/origin/trunk", "HEAD")
			runGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			got, err := defaultRef(ctx, dir)
			if err != nil || got != tc.want {
				t.Fatalf("defaultRef = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	// Remote comparison wins over a diverged local default branch.
	runGit(t, dir, "branch", "trunk")
	writeFile(t, dir, "feature.txt", "feature\n")
	commit(t, dir)
	runGit(t, dir, "branch", "-f", "trunk", "HEAD")
	checkStats(t, dir, model.BranchStats{Additions: 1, Ahead: 1, MergeStatus: model.MergeClean})
}

func TestWorktrees(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	base := runGit(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "feature.txt", "feature only\n")
	commit(t, dir)
	path, branch, err := AddWorktree(ctx, dir, " Workspace 2 ")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "workspace-2" || path != filepath.Join(dir, ".worktrees", "workspace-2") {
		t.Fatalf("AddWorktree = %q, %q", path, branch)
	}
	if got := runGit(t, path, "rev-parse", "HEAD"); got != base {
		t.Fatalf("new branch starts at %s, want %s", got, base)
	}
	trees, err := ListWorktrees(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 || trees[1] != (Worktree{Path: path, Branch: branch, Head: base}) {
		t.Fatalf("ListWorktrees = %+v", trees)
	}
	if _, _, err := AddWorktree(ctx, dir, "Workspace 2"); err == nil {
		t.Fatal("duplicate worktree succeeded")
	}
	if _, _, err := AddWorktree(ctx, dir, " ../ "); err == nil {
		t.Fatal("empty slug succeeded")
	}
	writeFile(t, path, "dirty.txt", "keep me\n")
	if err := RemoveWorktree(ctx, dir, path, true); err == nil {
		t.Fatal("dirty worktree removed")
	}
	if _, err := os.Stat(filepath.Join(path, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(ctx, dir, path, false); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "show-ref", "--verify", "refs/heads/"+branch)
	path, branch, err = AddWorktree(ctx, dir, "delete me")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(ctx, dir, filepath.Join(".worktrees", branch), true); err != nil {
		t.Fatal(err)
	}
	if _, err := git(ctx, dir, "show-ref", "--verify", "refs/heads/"+branch); err == nil {
		t.Fatal("branch kept")
	}
	trees, err = ListWorktrees(ctx, dir)
	if err != nil || len(trees) != 1 {
		t.Fatalf("ListWorktrees after removal = %+v, %v", trees, err)
	}
}

func TestDetachedWorktreePath(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	path := filepath.Join(t.TempDir(), "with space\nwith newline")
	runGit(t, dir, "worktree", "add", "--detach", path, "HEAD")
	trees, err := ListWorktrees(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 || trees[1].Path != path || trees[1].Branch != "" || trees[1].Head == "" {
		t.Fatalf("detached worktree = %+v", trees)
	}
	if root, ok := RepoRoot(ctx, path); !ok || root != path {
		t.Fatalf("RepoRoot = %q, %v", root, ok)
	}
	if err := RemoveWorktree(ctx, dir, path, true); err != nil {
		t.Fatal(err)
	}
}

func TestRepoRootAndErrors(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if root, ok := RepoRoot(ctx, sub); !ok || root != dir {
		t.Fatalf("RepoRoot = %q, %v", root, ok)
	}
	nonrepo := t.TempDir()
	if root, ok := RepoRoot(ctx, nonrepo); ok || root != "" {
		t.Fatalf("non-repo = %q, %v", root, ok)
	}
	if _, err := Stats(ctx, nonrepo); err == nil {
		t.Fatal("non-repo stats succeeded")
	}
	if _, err := ListWorktrees(ctx, nonrepo); err == nil {
		t.Fatal("non-repo list succeeded")
	}
	empty := t.TempDir()
	runGit(t, empty, "init", "--initial-branch=main")
	checkStats(t, empty, model.BranchStats{})
	if _, _, err := AddWorktree(ctx, empty, "new"); err == nil {
		t.Fatal("unborn worktree succeeded")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Stats(cancelled, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Stats = %v", err)
	}
}

func TestRemoveKeepsUnmergedBranch(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	path, branch, err := AddWorktree(ctx, dir, "unmerged")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "unmerged.txt", "keep this commit\n")
	commit(t, path)
	if err := RemoveWorktree(ctx, dir, path, true); !errors.Is(err, ErrBranchKept) {
		t.Fatalf("got %v, want ErrBranchKept", err)
	}
	runGit(t, dir, "show-ref", "--verify", "refs/heads/"+branch)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}
