package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// gitEnv keeps git in tests off the user's config, so no pager, hook or
// signing key of theirs runs.
func gitEnv(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Review Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "review@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Review Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "review@example.invalid")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run runs argv as a pane would and returns what it printed.
func run(t *testing.T, argv []string, env ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// TestDiffCmd runs the diff panes' commands in a real repository, from a
// subdirectory, with cat for a pager: the base is the merge base with the
// default branch gitstat finds, so a commit only on main shows nowhere,
// and committed and uncommitted work both show.
func TestDiffCmd(t *testing.T) {
	gitEnv(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	write(t, filepath.Join(dir, "sub", "b.txt"), "two\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	gitIn(t, dir, "checkout", "-b", "feature")
	write(t, filepath.Join(dir, "a.txt"), "one\ncommitted\n")
	gitIn(t, dir, "commit", "-am", "feature")
	gitIn(t, dir, "checkout", "main")
	write(t, filepath.Join(dir, "main.txt"), "main only\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "main")
	gitIn(t, dir, "checkout", "feature")
	write(t, filepath.Join(dir, "sub", "b.txt"), "two\nuncommitted\n")
	write(t, filepath.Join(dir, "new.txt"), "untracked\n")

	stats, err := gitstat.Stats(context.Background(), dir)
	if err != nil || stats.Base != "refs/heads/main" {
		t.Fatalf("Stats = %+v, %v; want Base refs/heads/main", stats, err)
	}
	sub := filepath.Join(dir, "sub")
	for _, c := range []struct {
		name      string
		file      *gitstat.FileStat
		want, not []string
	}{
		{"branch", nil, []string{"+committed", "+uncommitted"}, []string{"main only", "untracked"}},
		{"file", &gitstat.FileStat{Path: "sub/b.txt", Status: 'M'}, []string{"+uncommitted"}, []string{"+committed", "main only"}},
		{"untracked", &gitstat.FileStat{Path: "new.txt", Status: '?'}, []string{"+untracked"}, []string{"uncommitted"}},
	} {
		out := run(t, diffCmd(sub, stats.Base, c.file), "GIT_PAGER=cat")
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("%s: no color in %q", c.name, out)
		}
		out = regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out, "")
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: no %q in\n%s", c.name, w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("%s: %q in\n%s", c.name, n, out)
			}
		}
	}
}

// TestPRCmd runs the pull request tab's command against a local origin
// with a stub gh: a branch without an upstream is pushed first, one with
// an upstream is not, and gh gets pr create --fill either way.
func TestPRCmd(t *testing.T) {
	gitEnv(t)
	root := t.TempDir()
	origin, dir, bin := filepath.Join(root, "origin.git"), filepath.Join(root, "work"), filepath.Join(root, "bin")
	gitIn(t, root, "init", "--bare", "--initial-branch=main", origin)
	gitIn(t, root, "clone", origin, dir)
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	gitIn(t, dir, "push", "origin", "HEAD:main")
	gitIn(t, dir, "checkout", "-b", "feature")
	write(t, filepath.Join(dir, "a.txt"), "two\n")
	gitIn(t, dir, "commit", "-am", "feature")
	log := filepath.Join(root, "gh.log")
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$@\" >> '"+log+"'\necho https://example.invalid/pull/1\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	out := run(t, prCmd(dir), path)
	if !strings.Contains(out, "$ git push -u origin HEAD") || !strings.Contains(out, "https://example.invalid/pull/1") {
		t.Fatalf("no upstream: printed\n%s", out)
	}
	if up := gitIn(t, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/feature\n" {
		t.Fatalf("upstream after the push = %q", up)
	}
	write(t, filepath.Join(dir, "a.txt"), "three\n")
	gitIn(t, dir, "commit", "-am", "more")
	out = run(t, prCmd(dir), path)
	if strings.Contains(out, "git push") {
		t.Fatalf("with an upstream: printed\n%s", out)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "pr create --fill\npr create --fill\n" {
		t.Fatalf("gh called with %q, %v", calls, err)
	}
}

// TestReview: diff_pager splits the open tab, create_pr opens a tab in the
// tab's group, and a blocked action sends nothing.
func TestReview(t *testing.T) {
	old := ghInstalled
	t.Cleanup(func() { ghInstalled = old })
	ghInstalled = func() bool { return true }
	st := model.State{
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Branch: "feature", Path: "/repo"}},
		Stats:      map[string]model.BranchStats{"w": {Ahead: 1, MergeStatus: model.MergeClean, Base: "refs/remotes/origin/main"}},
	}
	n := nav{workspace: "w"}
	if got, want := n.review(&st, "w", "diff_pager"), (proto.OpenPane{WorkspaceID: "w", Dir: layout.Horizontal, Cmd: diffCmd("/repo", "refs/remotes/origin/main", nil)}); !reflect.DeepEqual(got, want) {
		t.Errorf("diff_pager = %+v, want %+v", got, want)
	}
	if got, want := n.review(&st, "w", "create_pr"), (proto.NewSession{Name: "PR feature", Cwd: "/repo", GroupID: "g", SessionID: "s", Cmd: prCmd("/repo")}); !reflect.DeepEqual(got, want) {
		t.Errorf("create_pr = %+v, want %+v", got, want)
	}
	ghInstalled = func() bool { return false }
	if got := n.review(&st, "w", "create_pr"); got != nil {
		t.Errorf("create_pr without gh = %+v", got)
	}
	if got := n.reviewBlocked(&st, "create_pr"); got != "no gh CLI" {
		t.Errorf("palette reason %q", got)
	}
}
