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

// gitRepo is a repo in a temp folder, by the name git prints for it: on
// Windows the long form of %TEMP%'s 8.3 short names, on macOS /private/var.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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

// new --lane starts the lane's preset with the words after -- as one
// brief, the worker contract before it, and names the lane to the daemon;
// lanes lists them, and fleet mode off or an unknown lane is refused.
func TestWorktreeLane(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	sh, _ = filepath.Abs(sh)
	repo := gitRepo(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PITWALL_PANE", "")
	contract := filepath.Join(cfg, "contract.md")
	os.WriteFile(contract, []byte("Commit on the branch.\n"), 0o644)
	os.MkdirAll(filepath.Join(cfg, "pitwall"), 0o755)
	write := func(on bool) {
		os.WriteFile(filepath.Join(cfg, "pitwall", "config.toml"), []byte(`[[agents.preset]]
name = "Quick"
agent = "sh"
args = ["-c", "true"]

[fleet]
enabled = `+map[bool]string{true: "true", false: "false"}[on]+`
contract = "`+filepath.ToSlash(contract)+`"

[fleet.small]
preset = "Quick"
timebox = 15
`), 0o600)
	}
	write(true)
	t.Chdir(repo)
	before := model.State{
		Sessions: []model.Session{{ID: "s", Name: "main"}},
		Projects: []model.Project{{ID: "g", SessionID: "s", Root: repo, Kind: model.ProjectGit}},
	}
	after := before
	after.Workspaces = []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Name: "fix"}}
	fakeCLI(t, cliExchange{state: before, level: proto.Level},
		cliExchange{request: proto.NewWorkspace{ProjectID: "g", Name: "fix", Cmd: []string{sh, "-c", "true", "Commit on the branch.\n\nFix the login redirect."}, Lane: "small"}, state: after})
	if code, out, stderr := worktreeOutput("new", "fix", "--lane", "small", "--", "Fix the login", "redirect."); code != 0 || out != "#1\n" {
		t.Fatalf("%d %q %s", code, out, stderr)
	}
	if code, out, _ := worktreeOutput("lanes"); code != 0 || !strings.Contains(out, "small   Quick") || !strings.Contains(out, "15m") || !strings.Contains(out, "(none: lane off)") {
		t.Fatalf("lanes %d %q", code, out)
	}
	for args, want := range map[string]string{
		"new x --lane big -- go":                  `no lane "big"`,
		"new x --lane small":                      "--lane needs a brief",
		"new x --lane small --preset Quick -- go": "one of --preset and --lane",
	} {
		if code, _, stderr := worktreeOutput(strings.Fields(args)...); code == 0 || !strings.Contains(stderr, want) {
			t.Errorf("%s: %d %q, want %q", args, code, stderr, want)
		}
	}
	write(false)
	if code, _, stderr := worktreeOutput("new", "x", "--lane", "small", "--", "go"); code == 0 || !strings.Contains(stderr, "fleet mode") {
		t.Errorf("fleet off: %d %q", code, stderr)
	}
}
