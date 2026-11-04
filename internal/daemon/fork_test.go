package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
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
	if w.Parent != "" || w.ProjectID != st.Workspaces[1].ProjectID || w.SessionID != before.Workspaces[1].SessionID || w.Profile != "work" || w.Path != dir || w.WorktreeRoot != "" || w.Name != "side" {
		t.Fatalf("fork tab %+v", w)
	}
	if got := f.pane(2).cfg; !slices.Equal(got.Cmd, []string{"codex", "fork", "--model", "gpt-6", "--cd", dir, "original"}) || !slices.Equal(got.Env, before.Panes[1].Env) {
		t.Fatalf("fork config %+v", got)
	}
	if p := st.Panes[1]; p.SessionID != "original" || !slices.Equal(p.Cmd, before.Panes[1].Cmd) || f.closed(1) {
		t.Fatalf("source changed: %+v", p)
	}
	for _, tc := range []struct{ pane, why string }{{parent, "no agent is running in this pane"}, {"missing", "no pane"}} {
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

func TestForkCallerAndHookIdentity(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RestoreCmd = store.RestoreCmd
	d := newDaemon(t, o)
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Cmd: []string{"claude"}}))
	d.mu.Lock()
	source := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "source"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	caller := d.state().Panes[1].ID
	must(t, d.handle(ctx, proto.ForkPane{Pane: source, ParentPane: caller}))
	st := d.state()
	if w := st.Workspaces[2]; w.Parent != st.Workspaces[1].ID {
		t.Fatalf("CLI parent %+v", w)
	}
	child := st.Panes[2].ID
	useReportHooks(d)
	for _, sid := range []string{"source", "child", "source"} {
		payload := []byte(fmt.Sprintf(`{"hook_event_name":"SessionStart","source":"fork","session_id":%q,"transcript_path":%q}`, sid, "/"+sid+".jsonl"))
		must(t, d.handle(ctx, proto.AgentEvent{Pane: child, Provider: model.ProviderClaude, Payload: payload}))
		p := reportedPane(d, child)
		if sid == "source" && p.SessionID == "source" || p.Transcript == "/source.jsonl" {
			t.Fatalf("source hook accepted: %+v", p)
		}
	}
	p := reportedPane(d, child)
	if p.SessionID != "child" || !store.Resumes(p) || !slices.Equal(store.RestoreCmd(p), []string{"claude", "--resume", "child"}) {
		t.Fatalf("child restore %+v", p)
	}
}

func TestForkFailureNotice(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	if err := d.handle(t.Context(), proto.ForkPane{Pane: "missing"}); err == nil || !strings.Contains(d.state().Notice, "no pane missing") {
		t.Fatalf("error = %v, notice = %q", err, d.state().Notice)
	}
}

func TestForkSuccessNotice(t *testing.T) {
	for _, notice := range []string{"", "A worktree has no tab."} {
		t.Run(notice, func(t *testing.T) {
			f := &fakes{statsCalls: map[string]int{}}
			d := newDaemon(t, f.options())
			must(t, d.handle(t.Context(), proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"codex", "fork", "source"}}))
			d.mu.Lock()
			src := d.st.Panes[0].ID
			d.st.Panes[0].Provider = model.ProviderCodex
			d.mu.Unlock()
			if err := d.handle(t.Context(), proto.ForkPane{Pane: src}); err == nil || !strings.HasPrefix(d.state().Notice, "Fork not started: ") {
				t.Fatalf("fresh fork: error=%v, notice=%q", err, d.state().Notice)
			}
			d.mu.Lock()
			d.st.Panes[0].SessionID = "child"
			if notice != "" {
				d.st.Notice = notice
			}
			d.mu.Unlock()
			must(t, d.handle(t.Context(), proto.ForkPane{Pane: src}))
			if got := d.state().Notice; got != notice {
				t.Errorf("successful fork notice = %q, want %q", got, notice)
			}
		})
	}
}

func TestPendingForkRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	dir := t.TempDir()
	must(t, d.handle(t.Context(), proto.NewSession{Cwd: dir, Cmd: []string{"claude"}}))
	d.mu.Lock()
	source := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderClaude, "source"
	d.mu.Unlock()
	must(t, d.handle(t.Context(), proto.ForkPane{Pane: source}))
	child := d.state().Panes[1].ID
	useReportHooks(d)
	must(t, d.handle(t.Context(), proto.AgentEvent{Pane: child, Provider: model.ProviderClaude, Payload: []byte(`{"hook_event_name":"SessionStart","source":"fork","session_id":"source"}`)}))
	snapshot := d.state()
	o := f.options()
	o.Load = func() (model.State, error) { return snapshot, nil }
	o.RestoreCmd = store.RestoreCmd
	restarted := newDaemon(t, o)
	if p := reportedPane(restarted, child); !p.Exited || store.Resumes(p) || p.SessionID != "" {
		t.Fatalf("pending fork restarted: %+v", p)
	}
}

func TestCodexWorktreeFromDirectorySelector(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "commit", "--allow-empty", "-m", "init")
	launch := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RepoRoot, o.AddWorktree, o.Branch = gitstat.RepoRoot, gitstat.AddWorktree, gitBranch
	d := newDaemon(t, o)
	must(t, d.handle(t.Context(), proto.NewSession{Cwd: launch, Cmd: []string{"codex", "--cd", repo}}))
	d.mu.Lock()
	source := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderCodex, "s"
	d.mu.Unlock()
	must(t, d.handle(t.Context(), proto.ForkPane{Pane: source, Worktree: "fork"}))
	target := filepath.Join(repo, ".worktrees", "fork")
	if cfg := f.pane(1).cfg; cfg.Cwd != target || !slices.Equal(cfg.Cmd, []string{"codex", "fork", "--cd", target, "s"}) {
		t.Fatalf("fork config %+v", cfg)
	}
}

func TestForkCodexRelativeDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub")
	must(t, os.Mkdir(target, 0o700))
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	must(t, d.handle(t.Context(), proto.NewSession{Cwd: dir, Cmd: []string{"codex", "-Csub"}}))
	d.mu.Lock()
	source := d.st.Panes[0].ID
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = model.ProviderCodex, "s"
	d.mu.Unlock()
	must(t, d.handle(t.Context(), proto.ForkPane{Pane: source}))
	if cfg := f.pane(1).cfg; cfg.Cwd != target || !slices.Equal(cfg.Cmd, []string{"codex", "fork", "--cd", target, "s"}) {
		t.Fatalf("relative directory applied twice: %+v", cfg)
	}
}

// codex --cd other, launched from dir: the agent's working root is other.
func TestForkCodexSourceDirectory(t *testing.T) {
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
func TestForkCodexWorktreeDirectory(t *testing.T) {
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
func TestForkKeepsSharedWorktree(t *testing.T) {
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
	if !strings.Contains(d.state().Notice, "same-folder") {
		t.Fatalf("shared refusal notice %q", d.state().Notice)
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
func TestForkOrphanPane(t *testing.T) {
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
func TestForkMissingLiveFolder(t *testing.T) {
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
func TestForkMenuFromChildTab(t *testing.T) {
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
