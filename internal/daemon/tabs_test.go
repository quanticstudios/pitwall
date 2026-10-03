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

// A tab is a session: NewTab opens one right after its source, in its group,
// in the source pane's live directory; CloseTab kills one and RenameTab names
// one.
func TestTabs(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir, there := t.TempDir(), t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	st := d.state()
	src, last, shell := st.Workspaces[0], st.Workspaces[1], st.Panes[0].ID
	must(t, d.handle(ctx, proto.NewGroup{Name: "g", WorkspaceIDs: []string{src.ID}}))
	group := d.state().Projects[0].ID

	// The shell cd'd; a tab opened from it starts there.
	f.pane(0).cfg.Cwd = there
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: src.ID, FromPane: shell}))
	st = d.state()
	nw := st.Workspaces[1]
	if len(st.Workspaces) != 3 || st.Workspaces[0].ID != src.ID || st.Workspaces[2].ID != last.ID {
		t.Fatalf("NewTab order: %+v", st.Workspaces)
	}
	if nw.ProjectID != group || nw.Path != there || nw.NameSet || !model.IsSessionName(nw.Name) || len(nw.Tabs) != 1 ||
		nw.Label != filepath.Base(there) || f.pane(2).cfg.Cwd != there || len(f.pane(2).cfg.Cmd) != 0 {
		t.Fatalf("NewTab: %+v cfg %+v", nw, f.pane(2).cfg)
	}
	// From a pane alone (pitwall tab new), and from an ungrouped session
	// without a pane, which starts in its path.
	must(t, d.handle(ctx, proto.NewTab{FromPane: shell}))
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: last.ID}))
	st = d.state()
	if w := st.Workspaces[1]; len(st.Workspaces) != 5 || w.ID == nw.ID || w.Path != there || w.ProjectID != group {
		t.Fatalf("NewTab from a pane: %+v", st.Workspaces)
	}
	if w := st.Workspaces[4]; w.Path != dir || w.ProjectID != "" || st.Workspaces[3].ID != last.ID {
		t.Fatalf("NewTab without a pane: %+v", st.Workspaces)
	}
	if d.handle(ctx, proto.NewTab{WorkspaceID: "gone"}) == nil {
		t.Fatal("NewTab of an unknown session accepted")
	}
	must(t, d.handle(ctx, proto.SelectTab{WorkspaceID: "anything"}))

	must(t, d.handle(ctx, proto.RenameTab{Pane: shell, Name: " build "}))
	if w := d.state().Workspaces[0]; w.Name != "build" || !w.NameSet || w.Label != "build" || w.Tabs[0].Title != "build" {
		t.Fatalf("RenameTab: %+v", w)
	}
	if d.handle(ctx, proto.RenameTab{WorkspaceID: nw.ID, Name: "build"}) == nil {
		t.Fatal("RenameTab to a taken name accepted")
	}
	must(t, d.handle(ctx, proto.RenameTab{Pane: shell}))
	if w := d.state().Workspaces[0]; w.NameSet || !model.IsSessionName(w.Name) || w.Label != filepath.Base(dir) {
		t.Fatalf("RenameTab clear: %+v", w)
	}
	if d.handle(ctx, proto.RenameTab{Pane: "gone", Name: "x"}) == nil {
		t.Fatal("RenameTab of an unknown pane accepted")
	}

	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: nw.ID, TabID: "ignored"}))
	if slices.ContainsFunc(d.state().Workspaces, func(w model.Workspace) bool { return w.ID == nw.ID }) || len(d.state().Panes) != 4 {
		t.Fatalf("CloseTab left %+v", d.state())
	}
	waitUntil(t, "closed tab's pane", func() bool { return f.closed(2) })
	if d.handle(ctx, proto.CloseTab{WorkspaceID: nw.ID}) == nil {
		t.Fatal("CloseTab of a closed tab accepted")
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
		"shell":  {ID: "shell", Title: "user@host: ~", Cwd: "/src/pitwall"},
		"home":   {ID: "home", Title: "user@host: ~", Cwd: homeDir()},
		"claude": {ID: "claude", Title: "Fix login", Provider: model.ProviderClaude, Prompt: "fix the login"},
		"codex":  {ID: "codex", Title: "api", Provider: model.ProviderCodex, Prompt: "rename foo to bar"},
		"repo":   {ID: "repo", Title: "REPO", Provider: model.ProviderCodex},
		"fresh":  {ID: "fresh", Title: "Claude Code", Provider: model.ProviderClaude},
		"quiet":  {ID: "quiet"},
		"vim":    {ID: "vim", Title: "main.go - NVIM"},
		"bare":   {ID: "bare", Provider: model.ProviderCodex},
	}
	running := map[string]string{"quiet": "make", "vim": "nvim"}
	for _, c := range []struct {
		ids  []string
		want string
	}{
		{[]string{"shell", "claude"}, "Fix login"},        // Claude topic beats its prompt and the shell
		{[]string{"shell", "codex"}, "rename foo to bar"}, // Codex dir-name title falls to the prompt
		{[]string{"codex", "claude"}, "Fix login"},        // any agent's topic beats any prompt
		{[]string{"shell", "repo"}, "pitwall"},            // a shell at its prompt shows its live directory
		{[]string{"home"}, "~"},                           // home is ~
		{[]string{"fresh", "quiet"}, "make"},              // "Claude Code" is generic: foreground command
		{[]string{"vim", "shell"}, "main.go - NVIM"},      // a running program's own title
		{[]string{"bare"}, "api"},                         // nothing: the session's directory name
	} {
		tab := &model.Tab{Layout: &layout.Node{}}
		for _, id := range c.ids {
			tab.Layout.Children = append(tab.Layout.Children, &layout.Node{Pane: id})
		}
		if got := tabTitle(w, tab, panes, running); got != c.want {
			t.Errorf("tabTitle(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
	named := &model.Workspace{Name: "deploy", NameSet: true}
	if got := tabTitle(named, &model.Tab{Layout: layout.Leaf("claude")}, panes, running); got != "deploy" {
		t.Errorf("a chosen name lost to %q", got)
	}
	if got := tabTitle(&model.Workspace{Name: "swift-otter"}, &model.Tab{}, panes, running); got != "swift-otter" {
		t.Errorf("empty tab title %q", got)
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

// Label is the tab's title and never empty: a bare shell shows its
// directory, an agent's topic beats it and a chosen name beats both.
func TestLabel(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "cap")
	mkdir(t, dir)
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir}))
	id := d.state().Panes[0].ID
	label := func() string { return d.state().Workspaces[0].Label }
	if got := label(); got != "cap" {
		t.Fatalf("label %q for a bare shell", got)
	}
	d.mu.Lock()
	d.st.Panes[0].Title = "Fix login"
	d.mu.Unlock()
	must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)}))
	if got := label(); got != "Fix login" {
		t.Fatalf("label %q, want the agent's topic", got)
	}
	must(t, d.handle(ctx, proto.RenameTab{Pane: id, Name: "deploy"}))
	if got := label(); got != "deploy" {
		t.Fatalf("label %q, want the chosen name", got)
	}
}

// A shell at its prompt titles its tab with the directory it is in now,
// read on the liveness poll.
func TestShellTitleFollowsCwd(t *testing.T) {
	d, lp, _ := openLive(t, 200)
	label := func() string { d.mu.Lock(); defer d.mu.Unlock(); return d.st.Workspaces[0].Label }
	there := filepath.Join(t.TempDir(), "aide")
	lp.setCwd(there)
	waitUntil(t, "label aide", func() bool { return label() == "aide" })
	if got := d.state().Panes[0].Cwd; got != there {
		t.Fatalf("pane cwd %q", got)
	}
	lp.setCwd(homeDir())
	waitUntil(t, "label ~", func() bool { return label() == "~" })
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

	dirTitle := tabTitle()
	setTitle(0, "user@host: ~")
	waitUntil(t, "shell title", func() bool { return d.state().Panes[0].Title == "user@host: ~" })
	if tabTitle() != dirTitle {
		t.Fatalf("tab title %q from a shell at its prompt, want %q", tabTitle(), dirTitle)
	}
	setTitle(1, "⠋ cap")
	waitUntil(t, "pane title", func() bool { return d.state().Panes[1].Title == "cap" })
	if tabTitle() != dirTitle {
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

// A pane whose process exits leaves its tab; the tab's last pane takes the
// session with it.
func TestPaneExitClosesPaneTabSession(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	ws := d.state().Workspaces[0].ID
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws, Target: d.state().Panes[0].ID}))

	f.pane(1).Close()
	waitUntil(t, "exited pane removed", func() bool { return len(d.state().Panes) == 1 })
	if w := d.state().Workspaces[0]; len(w.Tabs) != 1 || layout.Panes(w.Tabs[0].Layout)[0] != d.state().Panes[0].ID {
		t.Fatalf("after one exit: %+v", w)
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

// Branch and stats follow the shell into another repo, not the folder the
// tab started in.
func TestStatsFollowCwd(t *testing.T) {
	d, lp, _ := openLive(t, 200)
	repo := filepath.Join(t.TempDir(), "repo-aide")
	mkdir(t, repo)
	lp.setCwd(repo)
	waitUntil(t, "branch of the new cwd", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.st.Workspaces[0]
		_, ok := d.st.Stats[w.ID]
		return w.Branch == "repo-aide" && ok
	})
	st := d.state()
	if got := st.LivePath(st.Workspaces[0]); got != repo {
		t.Fatalf("LivePath %q, want %q", got, repo)
	}
}
