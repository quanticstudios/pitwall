package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A repository under the home folder here is the same folder under the
// home folder there; --repo names another, relative to that home unless
// it starts with / or ~/; a repository outside the home folder needs it.
func TestHostRepo(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	for _, c := range []struct{ root, repo, want string }{
		{filepath.Join(home, "Work", "app"), "", "~/Work/app"},
		{filepath.Join(home, "Work", "app"), "src/app", "~/src/app"},
		{filepath.Join(home, "Work", "app"), "/srv/app", "/srv/app"},
		{filepath.Join(home, "Work", "app"), "~/app", "~/app"},
		{filepath.Join(filepath.Dir(home), "app"), "/srv/app", "/srv/app"},
	} {
		if got, err := hostRepo(c.root, c.repo, "box"); err != nil || got != c.want {
			t.Errorf("hostRepo(%q, %q) = %q, %v; want %q", c.root, c.repo, got, err, c.want)
		}
	}
	for _, root := range []string{home, filepath.Join(filepath.Dir(home), "app")} {
		if _, err := hostRepo(root, "", "box"); err == nil || !strings.Contains(err.Error(), "--repo <path on box>") {
			t.Errorf("hostRepo(%q) = %v, want the --repo hint", root, err)
		}
	}
}

// git reaches the host over the shared connection, and from an agent's
// pane never prompts; the base ref is the tab's, so a retry with --id
// overwrites its own ref and no other worker's.
func TestHostPush(t *testing.T) {
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	r, err := newSSHHost("box", []config.Host{{Name: "box", SSH: "me@box"}})
	if err != nil {
		t.Skipf("no ssh: %v", err)
	}
	r.batch = true
	cmd := r.gitSSH()
	for _, want := range []string{"'-o' 'ControlMaster=auto'", "'-o' 'ControlPath=" + filepath.Join(run, "pitwall", "ssh-%C") + "'", "'-o' 'BatchMode=yes'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("GIT_SSH_COMMAND %s lacks %s", cmd, want)
		}
	}
	if strings.Contains(cmd, "me@box") || strings.Contains(cmd, "--") {
		t.Errorf("GIT_SSH_COMMAND %s names the destination, which git adds", cmd)
	}
	if got, want := pushArgs("me@box", "~/Work/app", "abc123", "0123456789abcdef"), []string{"push", "--no-verify", "--quiet", "me@box:~/Work/app", "+abc123:refs/pitwall/base/0123456789abcdef"}; !slices.Equal(got, want) {
		t.Errorf("pushArgs = %q, want %q", got, want)
	}
}

// worktree new refuses --branch with --host, and --id and --repo
// without it, before it reaches any daemon.
func TestWorktreeHostFlags(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for args, want := range map[string]string{
		"new x --host box --branch side": "--branch cannot be used with --host",
		"new x --id 0123456789abcdef":    "go with --host",
		"new x --repo src/app":           "go with --host",
		"new x --host local --repo app":  "go with --host",
	} {
		if code, _, errOut := worktreeOutput(strings.Fields(args)...); code == 0 || !strings.Contains(errOut, want) {
			t.Errorf("%s: %d %q, want %q", args, code, errOut, want)
		}
	}
}

// A host below WorktreeHostLevel would drop the lane's rules and not
// expand the repository's ~/, so worktree new --host refuses it before
// it pushes anything there.
func TestWorktreeHostLevel(t *testing.T) {
	fakeCLI(t, cliExchange{state: model.State{}, level: proto.WorktreeHostLevel - 1})
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(string, string) (*cliConn, func(), error) {
		c, err := dialCLI()
		return c, func() { c.Close() }, err
	}
	pushed := false
	wt := &hostWorktree{msg: proto.NewWorkspace{Root: "~/app"}, push: func(string) error { pushed = true; return nil }}
	_, err := startOnHost(nil, "here", &model.Session{ID: "s", Name: "main"}, "box", proto.NewSession{Name: "fix", Cmd: []string{"sh"}}, false, wt)
	if err == nil || !strings.HasPrefix(err.Error(), "box: ") || !strings.Contains(err.Error(), "cannot make a worktree") || pushed {
		t.Fatalf("a host below WorktreeHostLevel: %v, pushed %v", err, pushed)
	}
}

// A worktree the host made with a setup problem is our worker: the address
// and the problem both come back and the record stays. An id the host
// refuses because another parent's tab has it is a refusal: no address,
// and the record goes.
func TestWorktreeHostProblem(t *testing.T) {
	t.Setenv("PITWALL_PANE", "lp")
	const id = "0123456789abcdef"
	part := model.State{Sessions: []model.Session{{ID: "p", Name: "main", Origin: model.Origin{Daemon: "here", Session: "s"}}}}
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	isTab := func(got any) bool {
		m, ok := got.(proto.NewWorkspace)
		return ok && m.ID == id && m.ParentTab == "lw" && m.SessionID == "p" && m.Root == "~/app" && m.Name == "fix"
	}
	isAdd := func(got any) bool { m, ok := got.(proto.RemoteWorkers); return ok && len(m.Add) == 1 }
	wt := &hostWorktree{msg: proto.NewWorkspace{Root: "~/app"}}
	tab := proto.NewSession{Name: "fix", Cmd: []string{"claude"}, ID: id, ParentTab: "lw"}
	for _, c := range []struct {
		parent, want string
	}{{"lw", "box:1"}, {"other", ""}} {
		made := part
		made.Workspaces = []model.Workspace{{ID: id, SessionID: "p", Parent: c.parent, RemoteParent: true}}
		hostConns(t, []cliExchange{{state: part, level: proto.Level}, {request: isTab, state: made, level: proto.Level, error: "worktree fix is ready, but: setup failed"}})
		exchanges := []cliExchange{{request: isAdd, level: proto.Level}}
		if c.want == "" {
			exchanges = append(exchanges, cliExchange{request: proto.RemoteWorkers{SessionID: "s", Drop: []string{id}}, level: proto.Level})
		}
		got, err := startOnHost(fakeLocal(t, exchanges...), "here", session, "box", tab, false, wt)
		if got != c.want || err == nil || !strings.Contains(err.Error(), "setup failed") || c.want != "" && strings.Contains(err.Error(), "retry") {
			t.Errorf("tab with parent %s: %q, %v; want %q", c.parent, got, err, c.want)
		}
	}
}
