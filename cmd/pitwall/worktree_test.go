package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"commit", "--allow-empty", "-m", "init"}, {"branch", "side"}} {
		args = append([]string{"-C", dir, "-c", "user.name=CLI Test", "-c", "user.email=cli@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	return dir
}

func worktreeOutput(args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	code := runWorktree(args, &out, &stderr)
	return code, out.String(), stderr.String()
}

// new opens the tab through the daemon, with the repo's group, from an
// existing branch.
func TestWorktreeNew(t *testing.T) {
	repo := gitRepo(t)
	t.Chdir(repo)
	before := model.State{
		Sessions: []model.Session{{ID: "s", Name: "main"}},
		Projects: []model.Project{{ID: "g", SessionID: "s", Root: repo, Kind: model.ProjectGit}},
	}
	after := before
	after.Workspaces = []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Name: "side"}}
	fakeCLI(t, cliExchange{state: before},
		cliExchange{request: proto.NewWorkspace{ProjectID: "g", Name: "fix", From: model.WorktreeFrom{Kind: model.FromBranch, Ref: "side"}}, state: after})
	if code, out, stderr := worktreeOutput("new", "fix", "--branch", "side"); code != 0 || out != "#1\n" {
		t.Fatalf("%d %q %s", code, out, stderr)
	}
}

// With no session at all, new has the daemon make one with the repo's
// group, as pitwall new does.
func TestWorktreeNewFirst(t *testing.T) {
	repo := gitRepo(t)
	t.Chdir(repo)
	grouped := model.State{
		Sessions: []model.Session{{ID: "s", Name: "main"}},
		Projects: []model.Project{{ID: "g", SessionID: "s", Root: repo, Kind: model.ProjectGit}},
	}
	after := grouped
	after.Workspaces = []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Name: "fix"}}
	fakeCLI(t, cliExchange{}, cliExchange{request: proto.AddProject{Path: repo}, state: grouped},
		cliExchange{request: proto.NewWorkspace{ProjectID: "g", Name: "fix", From: model.WorktreeFrom{Ref: "side"}}, state: after})
	if code, out, stderr := worktreeOutput("new", "fix", "--from", "side"); code != 0 || out != "#1\n" {
		t.Fatalf("%d %q %s", code, out, stderr)
	}
}

// Without a daemon, ls lists the worktrees, rm refuses one with changes
// and names them, and prune lists the ones no tab uses.
func TestWorktreeOffline(t *testing.T) {
	repo := gitRepo(t)
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("PITWALL_PANE", "")
	t.Chdir(repo)
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "fix", filepath.Join(repo, ".worktrees", "fix")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktrees", "fix", "notes.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := worktreeOutput("ls")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); code != 0 || len(lines) != 3 ||
		!strings.HasPrefix(strings.Join(strings.Fields(lines[2]), " "), "- fix 1 file 0 0 ") {
		t.Fatalf("ls %d %q %s", code, out, stderr)
	}
	code, out, _ = worktreeOutput("prune")
	if code != 0 || !strings.Contains(out, "fix  ") || !strings.Contains(out, "changed") {
		t.Fatalf("prune %d %q", code, out)
	}
	code, _, stderr = worktreeOutput("rm", "fix")
	if code != 1 || !strings.Contains(stderr, "notes.txt") || !strings.Contains(stderr, "--force") {
		t.Fatalf("rm %d %s", code, stderr)
	}
	if code, _, stderr = worktreeOutput("rm", "fix", "--force"); code != 0 {
		t.Fatalf("rm --force %d %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees", "fix")); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
	for _, bad := range [][]string{{"rm", "fix"}, {"new", "x"}, {"ls", "x"}, {"new", "x", "--from", "main", "--pr", "2"}, {"frob"}} {
		if code, _, _ := worktreeOutput(bad...); code == 0 {
			t.Errorf("%q succeeded", bad)
		}
	}
}
