package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestForkPane(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	parent := d.state().Panes[0].ID
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, ParentPane: parent, Cmd: []string{"codex", "--model", "gpt-6"}, Env: []string{"CODEX_HOME=/saved/account", "PITWALL_PROFILE=work", "OTHER=keep"}}))
	d.mu.Lock()
	source := d.st.Panes[1].ID
	d.st.Panes[1].Provider, d.st.Panes[1].SessionID = model.ProviderCodex, "original"
	d.st.Workspaces[1].Profile = "work" // A saved account need not remain in config.
	d.mu.Unlock()
	before := d.state()
	must(t, d.handle(ctx, proto.NewGroup{Name: "agents", WorkspaceIDs: []string{before.Workspaces[1].ID}}))
	must(t, d.handle(ctx, proto.ForkPane{Pane: source, Name: "side"}))
	st := d.state()
	w := st.Workspaces[2]
	if w.Parent != before.Workspaces[0].ID || w.ProjectID != st.Workspaces[1].ProjectID || w.SessionID != before.Workspaces[1].SessionID || w.Profile != "work" || w.Path != dir || w.WorktreeRoot != "" || w.Name != "side" {
		t.Fatalf("fork tab %+v", w)
	}
	if got := f.pane(2).cfg; !slices.Equal(got.Cmd, []string{"codex", "fork", "--model", "gpt-6", "original"}) || !slices.Equal(got.Env, before.Panes[1].Env) {
		t.Fatalf("fork config %+v", got)
	}
	if p := st.Panes[1]; p.SessionID != "original" || !slices.Equal(p.Cmd, before.Panes[1].Cmd) || f.closed(1) {
		t.Fatalf("source changed: %+v", p)
	}
	for _, tc := range []struct{ pane, why string }{{parent, "cannot fork agent"}, {"missing", "no pane"}} {
		if err := d.handle(ctx, proto.ForkPane{Pane: tc.pane}); err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("fork %s = %v", tc.pane, err)
		}
	}
	d.mu.Lock()
	d.st.Panes[1].SessionID = ""
	d.mu.Unlock()
	if err := d.handle(ctx, proto.ForkPane{Pane: source, Worktree: "unused"}); err == nil || !strings.Contains(err.Error(), "no stored session id yet") {
		t.Fatalf("no-session fork = %v", err)
	}
	if len(d.state().Workspaces) != 3 || len(f.worktrees) != 0 {
		t.Fatal("failed fork created a tab or worktree")
	}
}

func TestForkPaneWorktree(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "config", "core.autocrlf", "false")
	must(t, os.WriteFile(filepath.Join(repo, "file"), []byte("committed\n"), 0o600))
	gitIn(t, repo, "add", "file")
	gitIn(t, repo, "commit", "-m", "init")
	sourceTree := filepath.Join(repo, ".worktrees", "source")
	gitIn(t, repo, "worktree", "add", "--detach", sourceTree)
	gitIn(t, sourceTree, "commit", "--allow-empty", "-m", "source")
	head := gitIn(t, sourceTree, "rev-parse", "HEAD")
	must(t, os.WriteFile(filepath.Join(sourceTree, "file"), []byte("dirty\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(sourceTree, "untracked"), []byte("dirty\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(repo, "local.env"), []byte("setting\n"), 0o600))
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RepoRoot, o.AddWorktree, o.Branch = gitstat.RepoRoot, gitstat.AddWorktree, gitBranch
	o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{Copy: []string{"local.env"}, PortBase: 3000, PortStep: 10, Setup: "echo setup"}, nil
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: sourceTree, Cmd: []string{"claude", "--model=opus"}}))
	d.mu.Lock()
	pane := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "original"
	d.st.Workspaces[0].Ports = model.PortBlock{First: 3010, Last: 3019}
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.ForkPane{Pane: pane, Worktree: "approach", Name: "second"}))
	w := d.state().Workspaces[1]
	if w.WorktreeRoot != repo || w.Path != filepath.Join(repo, ".worktrees", "approach") || w.Branch != "approach" || w.Ports.First != 3020 || gitIn(t, w.Path, "rev-parse", "HEAD") != head {
		t.Fatalf("fork worktree %+v", w)
	}
	data, err := os.ReadFile(filepath.Join(w.Path, "file"))
	if err != nil || string(data) != "committed\n" {
		t.Fatalf("fork file %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "untracked")); !os.IsNotExist(err) {
		t.Fatalf("untracked file carried over: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(w.Path, "local.env")); err != nil || string(data) != "setting\n" {
		t.Fatalf("worktree copy = %q, %v", data, err)
	}
	if got := f.pane(1).cfg; got.Cwd != w.Path || !slices.Contains(got.Env, "PORT=3020") || !slices.Equal(got.Cmd, []string{"claude", "--model=opus", "--fork-session", "--resume", "original"}) {
		t.Fatalf("fork config %+v", got)
	}
	waitUntil(t, "setup in its shell", func() bool { return f.pane(2).got() == "echo setup\r" })
	if len(d.state().Panes) != 3 || f.pane(2).cfg.Cwd != w.Path {
		t.Fatal("setup shell missing")
	}
	if got := gitIn(t, sourceTree, "status", "--porcelain"); !strings.Contains(got, "file") || !strings.Contains(got, "untracked") {
		t.Fatalf("source changes lost: %s", got)
	}
}
