//go:build unix

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestRemoteWorktreeLive starts workers in worktrees on box through two
// real daemons on this machine: this machine's, and box's, whose
// repository the CLI pushes to through the fake ssh of the remote tests.
// The worktree starts from a commit box never had, a repeated start with
// --id starts nothing, a lane with a host sends its worker there with the
// lane's timebox and --host local keeps one here, a missing repository
// leaves nothing behind, and removing the tab there deletes its base ref.
func TestRemoteWorktreeLive(t *testing.T) {
	dir := fakeHost(t)
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	setHome(t, home)
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_ATTACH", "")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", filepath.Join(dir, "bin")+":/usr/bin:/bin") // no real agent
	sockR, sockL := sockPath(t, "box.sock"), sockPath(t, "here.sock")
	envOf := func(name, run string) {
		t.Setenv("XDG_STATE_HOME", filepath.Join(dir, name, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, name, "config"))
		t.Setenv("XDG_RUNTIME_DIR", run)
	}
	serve := func(sock string) (stop func()) {
		d, err := daemon.New()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- d.Serve(ctx, ln) }()
		return func() {
			cancel()
			if err := <-served; err != nil {
				t.Error(err)
			}
		}
	}
	envOf("box", filepath.Dir(sockR))
	t.Cleanup(serve(sockR))
	envOf("here", filepath.Dir(sockL))
	t.Cleanup(serve(sockL))
	t.Setenv("PITWALL_SOCKET", sockL)
	cfg := filepath.Join(dir, "here", "config", "pitwall", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`[[hosts]]
name = "box"
ssh = "box"

[[agents.preset]]
name = "Quick"
agent = "sh"
args = ["-c", "sleep 60"]

[fleet]
enabled = true
nudge = "Wrap up."

[fleet.small]
preset = "Quick"
timebox = 15
host = "box"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	prevDial := dialHost
	t.Cleanup(func() { dialHost = prevDial })
	dialHost = func(_, kind string) (*cliConn, func(), error) {
		nc, err := net.Dial("unix", sockR)
		if err != nil {
			return nil, nil, err
		}
		c, err := hello(nc, kind)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { c.Close() }, nil
	}
	stateOf := func(dial func() (*cliConn, error)) model.State {
		t.Helper()
		c, err := dial()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		st, err := syncCLI(c)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	here := func() model.State { return stateOf(dialCLI) }
	box := func() model.State {
		return stateOf(func() (*cliConn, error) { c, _, err := dialHost("box", "cli"); return c, err })
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		args = append([]string{"-C", dir, "-c", "user.name=CLI Test", "-c", "user.email=cli@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// The repository here has a commit box's clone lacks.
	repo, boxRepo := filepath.Join(home, "here", "app"), filepath.Join(home, "box", "app")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "--initial-branch=main")
	git(repo, "commit", "--allow-empty", "-m", "init")
	git(home, "clone", "--quiet", repo, boxRepo)
	git(repo, "commit", "--allow-empty", "-m", "not pushed anywhere")
	git(repo, "remote", "add", "origin", "https://example.com/app.git")
	sha := git(repo, "rev-parse", "HEAD")

	// An orchestrator's tab here, whose pane runs the CLI.
	if code, _, errOut := cliOutput("new", "-n", "orchestrator", repo, "--", "sleep", "60"); code != 0 {
		t.Fatalf("the orchestrator's tab: %d %q", code, errOut)
	}
	st := here()
	i := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.Name == "orchestrator" })
	if i < 0 {
		t.Fatalf("no orchestrator tab: %+v", st.Workspaces)
	}
	orchestrator, session := st.Workspaces[i].ID, st.Workspaces[i].SessionID
	pane := st.Panes[slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.WorkspaceID == orchestrator })].ID
	t.Setenv("PITWALL_PANE", pane)
	t.Chdir(repo)
	workers := func() []model.RemoteWorker { st := here(); return st.Session(session).Workers }

	code, out, errOut := worktreeOutput("new", "w1", "--host", "box", "--repo", "box/app", "--", "sh", "-c", "sleep 60")
	if code != 0 || !strings.HasPrefix(out, "#box:") || !strings.Contains(errOut, "box is not shown in windows") || len(workers()) != 1 {
		log, _ := os.ReadFile(filepath.Join(dir, "ssh.log"))
		t.Fatalf("worktree new --host box: %d %q %q; records %+v; ssh got:\n%s", code, out, errOut, workers(), log)
	}
	id := workers()[0].ID
	bs := box()
	w := bs.Workspace(id)
	path := filepath.Join(boxRepo, ".worktrees", "w1")
	if w == nil || w.Path != path || w.Parent != orchestrator || !w.RemoteParent || workers()[0].Parent != orchestrator {
		t.Fatalf("the worker on box: %+v; records %+v", w, workers())
	}
	if got := git(path, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("box's worktree is at %s, want this machine's %s", got, sha)
	}
	if got := git(boxRepo, "rev-parse", gitstat.BaseRef(id)); got != sha {
		t.Fatalf("box's base ref is %s, want %s", got, sha)
	}

	// The same start with its id finds it there.
	n := len(bs.Workspaces)
	if code, out, errOut := worktreeOutput("new", "w1", "--host", "box", "--repo", "box/app", "--id", id, "--", "sh", "-c", "sleep 60"); code != 0 || !strings.HasPrefix(out, "#box:") || len(box().Workspaces) != n || len(workers()) != 1 {
		t.Fatalf("a repeated start: %d %q %q; box has %d tabs, had %d", code, out, errOut, len(box().Workspaces), n)
	}

	// The small lane's host is box; the worker brings the lane's timebox.
	if code, out, errOut := worktreeOutput("new", "w2", "--lane", "small", "--repo", "box/app", "--", "Fix it."); code != 0 || !strings.HasPrefix(out, "#box:") || len(workers()) != 2 {
		t.Fatalf("a lane worker: %d %q %q", code, out, errOut)
	}
	bs = box()
	w2 := bs.Workspace(workers()[1].ID)
	if w2 == nil || w2.Lane.Name != "small" || w2.Lane.Timebox != 15*time.Minute || w2.Lane.Nudge != "Wrap up." {
		t.Fatalf("the lane worker on box: %+v", w2)
	}
	// --host local keeps one here.
	if code, out, errOut := worktreeOutput("new", "w3", "--lane", "small", "--host", "local", "--", "Fix it here."); code != 0 || !strings.HasPrefix(out, "#") || strings.HasPrefix(out, "#box:") || len(workers()) != 2 {
		t.Fatalf("a lane worker kept here: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees", "w3")); err != nil {
		t.Fatalf("no worktree here: %v", err)
	}

	// A repository box does not have: the clone command, and nothing made.
	n = len(box().Workspaces)
	code, _, errOut = worktreeOutput("new", "w4", "--host", "box", "--repo", "nowhere", "--", "sh")
	if code == 0 || !strings.Contains(errOut, "box has no repository at ~/nowhere") || !strings.Contains(errOut, "ssh box git clone https://example.com/app.git ~/nowhere") ||
		len(box().Workspaces) != n || len(workers()) != 2 {
		t.Fatalf("a missing repository: %d %q; box has %d tabs, had %d", code, errOut, len(box().Workspaces), n)
	}

	// Removing the tab on box deletes its base ref.
	c, done, err := dialHost("box", "cli")
	if err != nil {
		t.Fatal(err)
	}
	_, err = syncCLI(c, proto.DeleteWorkspace{WorkspaceID: id, Force: true})
	done()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", boxRepo, "rev-parse", "--verify", "--quiet", gitstat.BaseRef(id)).Output(); err == nil {
		t.Fatalf("box kept the base ref of a removed tab: %s", out)
	}

	// A guard refusal must leave no worker or worktree claiming its id.
	const refusedID = "fedcba9876543210"
	configDir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	n = len(box().Workspaces)
	code, _, errOut = worktreeOutput("new", "guarded", "--host", "box", "--repo", "box/app", "--id", refusedID, "--guard", "--", "claude")
	if code == 0 || !strings.Contains(errOut, "guarded tab not started") || len(box().Workspaces) != n || len(workers()) != 2 {
		t.Fatalf("guard refusal: %d %q; tabs %+v; records %+v", code, errOut, box().Workspaces, workers())
	}
	if _, err := os.Stat(filepath.Join(boxRepo, ".worktrees", "guarded")); !os.IsNotExist(err) {
		t.Fatalf("guard refusal kept the worktree: %v", err)
	}
	// Once hooks are installed, the exact same command can start.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(`{"hooks":`+string(agent.ClaudeHooks(os.Args[0]))+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "claude"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := worktreeOutput("new", "guarded", "--host", "box", "--repo", "box/app", "--id", refusedID, "--guard", "--", "claude"); code != 0 || len(box().Workspaces) != n+1 {
		t.Fatalf("retry after refusal: %d %q", code, errOut)
	}

	// A person's shell has a session but no parent tab on either machine.
	t.Setenv("PITWALL_PANE", "")
	const humanID = "0123456789abcdef"
	if code, out, errOut := worktreeOutput("new", "human", "--host", "box", "--repo", "box/app", "--id", humanID, "--", "sh", "-c", "sleep 60"); code != 0 || !strings.HasPrefix(out, "#box:") {
		t.Fatalf("worktree from a person's shell: %d %q %q", code, out, errOut)
	}
	bs = box()
	if w := bs.Workspace(humanID); w == nil || w.Parent != "" || w.RemoteParent || git(w.Path, "rev-parse", "HEAD") != sha {
		t.Fatalf("worktree from a person's shell: %+v", w)
	}
}
