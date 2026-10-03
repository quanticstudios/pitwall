package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/agent"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func newDaemon(t *testing.T, o Options) *Daemon {
	t.Helper()
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.shutdown)
	return d
}

// state is a copy of d's state, taken under the lock.
func (d *Daemon) state() model.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot()
}

func (f *fakes) closed(i int) bool {
	p := f.pane(i)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func TestTabs(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir, there := t.TempDir(), t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	w := d.state().Workspaces[0]
	if len(w.Tabs) != 1 || w.ActiveTab != w.Tabs[0].ID {
		t.Fatalf("new session tabs: %+v", w)
	}
	first, shell := w.Tabs[0].ID, d.state().Panes[0].ID

	// The shell cd'd; a tab opened from it starts there.
	f.pane(0).cfg.Cwd = there
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: w.ID, FromPane: shell}))
	w = d.state().Workspaces[0]
	second := w.Tabs[1].ID
	if len(w.Tabs) != 2 || w.ActiveTab != second || f.pane(1).cfg.Cwd != there || len(f.pane(1).cfg.Cmd) != 0 {
		t.Fatalf("NewTab: %+v cfg %+v", w, f.pane(1).cfg)
	}
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: w.ID}))
	if f.pane(2).cfg.Cwd != dir {
		t.Fatalf("NewTab without a pane or cwd started in %s, want the session path", f.pane(2).cfg.Cwd)
	}
	third := d.state().Workspaces[0].Tabs[2].ID

	// OpenPane goes to the active tab, or the one named.
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: w.ID}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: w.ID, TabID: first, Target: shell, Dir: layout.Vertical}))
	st := d.state()
	w = st.Workspaces[0]
	p3, p4 := st.Panes[3].ID, st.Panes[4].ID
	if got := layout.Panes(w.Tabs[2].Layout); !slices.Equal(got, []string{st.Panes[2].ID, p3}) {
		t.Fatalf("active tab panes %v", got)
	}
	if got := layout.Panes(w.Tabs[0].Layout); !slices.Equal(got, []string{shell, p4}) {
		t.Fatalf("first tab panes %v", got)
	}
	if d.handle(ctx, proto.OpenPane{WorkspaceID: w.ID, TabID: second, Target: shell}) == nil {
		t.Fatal("split of a pane in another tab accepted")
	}

	// SetLayout checks the tab's own panes.
	two := func(a, b string) *layout.Node {
		return &layout.Node{Children: []*layout.Node{{Pane: a}, {Pane: b}}}
	}
	if d.handle(ctx, proto.SetLayout{WorkspaceID: w.ID, TabID: first, Layout: two(shell, p3)}) == nil {
		t.Fatal("layout with another tab's pane accepted")
	}
	must(t, d.handle(ctx, proto.SetLayout{WorkspaceID: w.ID, TabID: first, Layout: two(p4, shell)}))
	if got := layout.Panes(d.state().Workspaces[0].Tabs[0].Layout); !slices.Equal(got, []string{p4, shell}) {
		t.Fatalf("SetLayout: %v", got)
	}

	must(t, d.handle(ctx, proto.SelectTab{WorkspaceID: w.ID, TabID: second}))
	if d.state().Workspaces[0].ActiveTab != second || d.handle(ctx, proto.SelectTab{WorkspaceID: w.ID, TabID: "nope"}) == nil {
		t.Fatal("SelectTab")
	}

	must(t, d.handle(ctx, proto.RenameTab{Pane: p4, Name: " build "}))
	must(t, d.handle(ctx, proto.RenameTab{WorkspaceID: w.ID, TabID: second, Name: "logs"}))
	w = d.state().Workspaces[0]
	if w.Tabs[0].Name != "build" || w.Tabs[1].Name != "logs" {
		t.Fatalf("RenameTab: %+v", w.Tabs)
	}
	must(t, d.handle(ctx, proto.RenameTab{Pane: p4}))
	if d.state().Workspaces[0].Tabs[0].Name != "" || d.handle(ctx, proto.RenameTab{Pane: "gone", Name: "x"}) == nil {
		t.Fatal("RenameTab clear or unknown pane")
	}

	// Closing the active tab closes its panes and activates its neighbor.
	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: w.ID, TabID: second}))
	w = d.state().Workspaces[0]
	if len(w.Tabs) != 2 || w.ActiveTab != third || len(d.state().Panes) != 4 {
		t.Fatalf("CloseTab: %+v", w)
	}
	waitUntil(t, "closed tab's pane", func() bool { return f.closed(1) })
	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: w.ID, TabID: first}))
	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: w.ID, TabID: third}))
	if st := d.state(); len(st.Workspaces) != 0 || len(st.Panes) != 0 {
		t.Fatalf("closing the last tab left %+v", st)
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"⠋ cap":            "cap",
		"⠼ cap":            "cap",
		"✳ Fix the login":  "Fix the login",
		"✶ ✻ · topic  ":    "topic",
		"* Claude Code":    "Claude Code",
		"  vim main.go":    "vim main.go",
		"user@host: ~/src": "user@host: ~/src",
		"⠙":                "",
		"":                 "",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTabTitle(t *testing.T) {
	w := &model.Workspace{Path: "/src/repo/.worktrees/api", RepoRoot: "/src/repo"}
	panes := map[string]*model.Pane{
		"shell":  {ID: "shell", Title: "user@host: ~"},
		"claude": {ID: "claude", Title: "Fix login", Provider: model.ProviderClaude, Prompt: "fix the login"},
		"codex":  {ID: "codex", Title: "api", Provider: model.ProviderCodex, Prompt: "rename foo to bar"},
		"repo":   {ID: "repo", Title: "REPO", Provider: model.ProviderCodex},
		"fresh":  {ID: "fresh", Title: "Claude Code", Provider: model.ProviderClaude},
		"quiet":  {ID: "quiet"},
		"bare":   {ID: "bare", Provider: model.ProviderCodex},
	}
	running := map[string]string{"quiet": "make"}
	for _, c := range []struct {
		name string
		ids  []string
		want string
	}{
		{"", []string{"shell", "claude"}, "Fix login"},        // Claude topic beats its prompt and the shell
		{"", []string{"shell", "codex"}, "rename foo to bar"}, // Codex dir-name title falls to the prompt
		{"", []string{"codex", "claude"}, "Fix login"},        // any agent's topic beats any prompt
		{"", []string{"repo", "shell"}, "user@host: ~"},       // repo name is generic
		{"", []string{"fresh", "quiet"}, "make"},              // "Claude Code" is generic
		{"", []string{"bare", "quiet"}, "make"},               // no titles: foreground command
		{"", []string{"bare"}, "api"},                         // nothing: directory name
		{"deploy", []string{"claude", "shell"}, "deploy"},     // explicit name wins
	} {
		tab := &model.Tab{Name: c.name, Layout: &layout.Node{}}
		for _, id := range c.ids {
			tab.Layout.Children = append(tab.Layout.Children, &layout.Node{Pane: id})
		}
		if got := tabTitle(w, tab, panes, running); got != c.want {
			t.Errorf("tabTitle(%q, %v) = %q, want %q", c.name, c.ids, got, c.want)
		}
	}
	if user := loginName(); user != "" && !genericTitle(w, strings.ToUpper(user)) {
		t.Errorf("login name %q is not generic", user)
	}
}

func TestPromptTitle(t *testing.T) {
	for in, want := range map[string]string{
		"fix the failing auth test":                                                      "fix the failing auth test",
		"\n\n  fix   the\tbug  \nthen run the tests":                                     "fix the bug",
		"refactor the session store so that every pane keeps its prompt across restarts": "refactor the session store so that every pane…",
		strings.Repeat("x", 60):                                                          strings.Repeat("x", 47) + "…",
		strings.Repeat("ab ", 16) + "tail":                                               strings.Repeat("ab ", 15) + "ab…",
		"   ":                                                                            "",
	} {
		got := promptTitle(in)
		if got != want || utf8.RuneCountInString(got) > 48 {
			t.Errorf("promptTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The first prompt of an agent session names the tab; later prompts do not,
// and a new session starts over.
func TestPromptNamesTab(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d := newDaemon(t, o)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "cap")
	mkdir(t, dir)
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	id := d.state().Panes[0].ID
	prompt := func(sid, text string) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "session_id": sid, "transcript_path": "/t", "prompt": text})
		must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: b}))
	}
	title := func() string { return d.state().Workspaces[0].Tabs[0].Title }

	d.mu.Lock()
	d.st.Panes[0].Title = "cap" // Codex titles itself after the folder
	d.mu.Unlock()
	prompt("s1", "rename foo to bar\nand update callers")
	if got := title(); got != "rename foo to bar" {
		t.Fatalf("title %q after the first prompt", got)
	}
	if got := d.state().Workspaces[0].Label; got != "rename foo to bar" {
		t.Fatalf("label %q", got)
	}
	prompt("s1", "now run the tests")
	if got := title(); got != "rename foo to bar" {
		t.Fatalf("second prompt replaced the first: %q", got)
	}
	prompt("s2", "start over")
	if got := title(); got != "start over" {
		t.Fatalf("new session kept %q", got)
	}
}

// Label is the title of the tab with the most urgent agent, else of the
// active tab, and "" when that is only the directory name.
func TestLabelFollowsAttention(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	ws := d.state().Workspaces[0].ID
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: ws}))
	st := d.state()
	first, second := st.Panes[0].ID, st.Panes[1].ID
	if got := st.Workspaces[0].Label; got != "" {
		t.Fatalf("label %q for a bare shell", got)
	}
	must(t, d.handle(ctx, proto.RenameTab{Pane: first, Name: "build"}))
	must(t, d.handle(ctx, proto.RenameTab{Pane: second, Name: "docs"}))
	if got := d.state().Workspaces[0].Label; got != "docs" {
		t.Fatalf("label %q, want the active tab's", got)
	}
	must(t, d.handle(ctx, proto.AgentEvent{Pane: first, Provider: model.ProviderClaude, Payload: []byte(model.StatePendingApproval)}))
	must(t, d.handle(ctx, proto.AgentEvent{Pane: second, Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)}))
	if got := d.state().Workspaces[0].Label; got != "build" {
		t.Fatalf("label %q, want the tab waiting for approval", got)
	}
}

// NameSet tells names a person chose from generated ones.
func TestNameSet(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Name: "api"}))
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	pid := d.state().Projects[0].ID
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: pid, Name: "feat"}))
	got := map[string]bool{}
	for _, w := range d.state().Workspaces {
		got[w.Name] = w.NameSet
	}
	gen := d.state().Workspaces[0]
	if !model.IsSessionName(gen.Name) || got[gen.Name] || !got["api"] || got["repo"] || got["workspace-1"] || !got["feat"] {
		t.Fatalf("NameSet by name: %v", got)
	}
	must(t, d.handle(ctx, proto.RenameWorkspace{WorkspaceID: gen.ID, Name: "web"}))
	if w := d.state().Workspaces[0]; !w.NameSet {
		t.Fatalf("rename left NameSet false: %+v", w)
	}
}

// A pane's emulator title reaches the pane and its tab; a spinner turning
// pushes no state.
func TestTitleFollowsAgentPane(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	ws := d.state().Workspaces[0].ID
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws, Cmd: []string{"codex"}}))
	agent := d.state().Panes[1].ID
	setTitle := func(i int, title string) {
		p := f.pane(i)
		p.mu.Lock()
		p.title = title
		p.mu.Unlock()
		p.dirty <- struct{}{}
	}
	tabTitle := func() string { return d.state().Workspaces[0].Tabs[0].Title }

	setTitle(0, "user@host: ~")
	waitUntil(t, "shell title", func() bool { return tabTitle() == "user@host: ~" })
	setTitle(1, "⠋ cap")
	waitUntil(t, "pane title", func() bool { return d.state().Panes[1].Title == "cap" })
	if tabTitle() != "user@host: ~" {
		t.Fatalf("tab title %q before the pane is known as an agent", tabTitle())
	}
	must(t, d.handle(ctx, proto.AgentEvent{Pane: agent, Provider: model.ProviderCodex, Payload: []byte("working")}))
	if tabTitle() != "cap" {
		t.Fatalf("tab title %q, want the agent's", tabTitle())
	}

	v := d.state().Version
	setTitle(1, "⠙ cap")
	time.Sleep(2 * titlePoll)
	if got := d.state().Version; got != v {
		t.Fatalf("a spinner frame pushed state: version %d -> %d", v, got)
	}
}

// A pane whose process exits leaves its tab; an emptied tab leaves the
// session, and an emptied session goes.
func TestPaneExitClosesPaneTabSession(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	ws := d.state().Workspaces[0].ID
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: ws}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws, Target: d.state().Panes[1].ID}))
	first := d.state().Workspaces[0].Tabs[0].ID

	f.pane(2).Close()
	waitUntil(t, "exited pane removed", func() bool { return len(d.state().Panes) == 2 })
	if w := d.state().Workspaces[0]; len(w.Tabs) != 2 || layout.Panes(w.Tabs[1].Layout)[0] != d.state().Panes[1].ID {
		t.Fatalf("after one exit: %+v", w)
	}
	f.pane(1).Close()
	waitUntil(t, "emptied tab removed", func() bool { return len(d.state().Workspaces[0].Tabs) == 1 })
	if w := d.state().Workspaces[0]; w.ActiveTab != first {
		t.Fatalf("active tab %s, want %s", w.ActiveTab, first)
	}
	f.pane(0).Close()
	waitUntil(t, "emptied session removed", func() bool { return len(d.state().Workspaces) == 0 })
}

// A resumed agent that fails right after a restart becomes a shell in its
// directory instead of closing the session.
func TestFailedResumeFallsBackToShell(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	dir := t.TempDir()
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "w", Path: dir, Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Cmd: []string{"claude"}, Cwd: dir, Provider: model.ProviderClaude, SessionID: "old"}},
	}
	d := newDaemon(t, f.options())
	if c := f.pane(0).cfg; !slices.Equal(c.Cmd, []string{"claude", "--resume", "old"}) {
		t.Fatalf("restored with %v", c.Cmd)
	}
	f.pane(0).code = 1
	f.pane(0).Close()
	waitUntil(t, "shell started", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.panes) == 2 })
	if c := f.pane(1).cfg; c.ID != "p" || c.Cwd != dir || len(c.Cmd) != 0 {
		t.Fatalf("fallback shell %+v", c)
	}
	st := d.state()
	if len(st.Workspaces) != 1 || len(st.Panes) != 1 || st.Panes[0].SessionID != "" || st.Panes[0].Cmd != nil {
		t.Fatalf("after fallback: %+v", st)
	}
	// The shell is an ordinary pane: its exit closes the session.
	f.pane(1).Close()
	waitUntil(t, "session closed", func() bool { return len(d.state().Workspaces) == 0 })
}

func TestDetachAndKill(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	removed := 0
	o.RemoveWorktree = func(context.Context, string, string, bool) error { removed++; return nil }
	d := newDaemon(t, o)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feat"}))
	ws := d.state().Workspaces[0].ID
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws}))

	must(t, d.handle(ctx, proto.DetachSession{WorkspaceID: ws, Detached: true}))
	if !d.state().Workspaces[0].Detached || f.closed(0) {
		t.Fatal("detach should hide the session and keep its pane running")
	}
	if d.handle(ctx, proto.ArchiveWorkspace{WorkspaceID: ws, Archived: true}) == nil {
		t.Fatal("ArchiveWorkspace still handled")
	}
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: ws}))
	if st := d.state(); len(st.Workspaces) != 0 || len(st.Panes) != 0 || removed != 0 {
		t.Fatalf("kill left %+v, RemoveWorktree ran %d times", st, removed)
	}
	waitUntil(t, "killed pane closed", func() bool { return f.closed(0) })
}

func TestGeneratedNames(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := t.TempDir()
	for range 40 {
		must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	}
	seen := map[string]bool{}
	for _, w := range d.state().Workspaces {
		if !model.IsSessionName(w.Name) || seen[w.Name] {
			t.Fatalf("name %q: not adjective-noun or repeated", w.Name)
		}
		seen[w.Name] = true
	}
	for _, name := range []string{"api", "swift", "swift-otter-x", "otter-swift", "fix-auth"} {
		if model.IsSessionName(name) {
			t.Fatalf("IsSessionName(%q)", name)
		}
	}
	if !model.IsSessionName("swift-otter-104") {
		t.Fatal("a numbered name is generated too")
	}

	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Name: "api"}))
	if d.handle(ctx, proto.NewSession{Cwd: dir, Name: "api"}) == nil {
		t.Fatal("duplicate NewSession name accepted")
	}
	ws := d.state().Workspaces
	a, b := ws[0].ID, ws[len(ws)-1].ID
	if d.handle(ctx, proto.RenameWorkspace{WorkspaceID: a, Name: "api"}) == nil {
		t.Fatal("duplicate rename accepted")
	}
	must(t, d.handle(ctx, proto.RenameWorkspace{WorkspaceID: b, Name: "api"})) // its own name
	must(t, d.handle(ctx, proto.RenameWorkspace{WorkspaceID: a, Name: "web"}))
	if d.state().Workspaces[0].Name != "web" {
		t.Fatal("rename")
	}
}

func TestGroupByFolder(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	repo := filepath.Join(t.TempDir(), "repo")
	other := t.TempDir()
	o.RepoRoot = func(_ context.Context, path string) (string, bool) {
		if path == repo || strings.HasPrefix(path, repo+"/") {
			return repo, true
		}
		return path, false
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	for _, p := range []string{repo, filepath.Join(repo, "sub"), filepath.Join(repo, "sub2")} {
		mkdir(t, p)
	}
	for _, p := range []string{repo, filepath.Join(repo, "sub"), other, repo, repo} {
		must(t, d.handle(ctx, proto.NewSession{Cwd: p}))
	}
	ws := d.state().Workspaces
	if ws[1].RepoRoot != repo || ws[2].RepoRoot != other {
		t.Fatalf("RepoRoot: %+v", ws)
	}
	must(t, d.handle(ctx, proto.DetachSession{WorkspaceID: ws[3].ID, Detached: true}))
	must(t, d.handle(ctx, proto.NewGroup{Name: "mine", WorkspaceIDs: []string{ws[4].ID}}))

	must(t, d.handle(ctx, proto.GroupByFolder{WorkspaceID: ws[0].ID}))
	st := d.state()
	if len(st.Projects) != 2 {
		t.Fatalf("projects %+v", st.Projects)
	}
	p := st.Projects[1]
	if p.Root != repo || p.Kind != model.ProjectGit || p.Name != "repo" || p.Color != "neutral" {
		t.Fatalf("project %+v", p)
	}
	var groups []string
	for _, w := range st.Workspaces {
		groups = append(groups, w.ProjectID)
	}
	if want := []string{p.ID, p.ID, "", "", st.Projects[0].ID}; !slices.Equal(groups, want) {
		t.Fatalf("groups %v, want %v", groups, want)
	}

	// Again with the project there: no second project.
	must(t, d.handle(ctx, proto.SetSessionGroup{WorkspaceID: ws[1].ID}))
	must(t, d.handle(ctx, proto.GroupByFolder{WorkspaceID: ws[1].ID}))
	if st := d.state(); len(st.Projects) != 2 || st.Workspaces[1].ProjectID != p.ID {
		t.Fatalf("regroup: %+v", st)
	}

	// A new session inside the project's folder joins it.
	must(t, d.handle(ctx, proto.NewSession{Cwd: filepath.Join(repo, "sub2")}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: other}))
	ws = d.state().Workspaces
	if ws[5].ProjectID != p.ID || ws[6].ProjectID != "" {
		t.Fatalf("auto-join: %+v", ws[5:])
	}
}

func TestProjectAt(t *testing.T) {
	d := &Daemon{st: model.State{Projects: []model.Project{
		{ID: "g", Kind: model.ProjectGroup},
		{ID: "r", Root: "/src/repo"},
		{ID: "n", Root: "/src/repo/nested"},
	}}}
	for path, want := range map[string]string{
		"/src/repo": "r", "/src/repo/a": "r", "/src/repo/nested/x": "n", "/src/repo2": "", "/src": "",
	} {
		if got := d.projectAt(path); got != want {
			t.Errorf("projectAt(%s) = %q, want %q", path, got, want)
		}
	}
}

// Sync answers with state once the requests before it are handled, for a CLI
// that gets no pushes.
func TestSyncAfterRequests(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	cli := dial(t, sock, "cli")
	cli.send(proto.NewSession{Cwd: t.TempDir(), Name: "cli-made"})
	cli.send(proto.Sync{})
	m := <-cli.in
	s, ok := m.(proto.StateMsg)
	if !ok || len(s.State.Workspaces) != 1 || s.State.Workspaces[0].Name != "cli-made" {
		t.Fatalf("first reply %#v, want state with the new session", m)
	}
	select {
	case m := <-cli.in:
		t.Fatalf("cli got an unasked %T", m)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFocusSession(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	gui := dial(t, sock, "gui")
	st := gui.waitState("first session", func(s model.State) bool { return len(s.Workspaces) == 1 })
	ws := st.Workspaces[0]
	gui.send(proto.NewTab{WorkspaceID: ws.ID})
	gui.send(proto.DetachSession{WorkspaceID: ws.ID, Detached: true})
	gui.waitState("detached", func(s model.State) bool { return s.Workspaces[0].Detached })

	dial(t, sock, "cli").send(proto.FocusSession{WorkspaceID: ws.ID, TabID: ws.Tabs[0].ID})
	// The state goes out before the forwarded message.
	gui.waitState("attached on the tab", func(s model.State) bool {
		return !s.Workspaces[0].Detached && s.Workspaces[0].ActiveTab == ws.Tabs[0].ID
	})
	gui.waitFor("FocusSession", func(m any) bool {
		f, ok := m.(proto.FocusSession)
		return ok && f.WorkspaceID == ws.ID && f.TabID == ws.Tabs[0].ID
	})
}
