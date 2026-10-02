package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// FakeBackend is an in-memory Backend with a few sessions, two groups and
// panes showing static text. Tick moves agent states so the UI has
// something to redraw. Like the daemon, it drops exited panes, empty tabs
// and empty sessions.
type FakeBackend struct {
	mu      sync.Mutex
	st      model.State
	sizes   map[string][2]int // pane -> cols, rows
	scroll  map[string]int    // pane -> lines scrolled back
	sent    []any
	changed chan struct{}
	focus   chan proto.FocusSession
	ticks   int
	nextID  int
}

// fakeHome is $HOME, so the fake's paths shorten to ~ like real ones.
var fakeHome = func() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/home/me"
}()

// NewFakeBackend returns three ungrouped sessions (a terminal running
// `go test` with two more tabs, an idle shell, a working Claude), two
// groups, and two detached sessions.
func NewFakeBackend() *FakeBackend {
	f := &FakeBackend{sizes: map[string][2]int{}, scroll: map[string]int{}, changed: make(chan struct{}, 1),
		focus: make(chan proto.FocusSession, 8)}
	now := time.Now()
	f.st.Projects = []model.Project{
		{ID: "g1", Name: "agents", Kind: model.ProjectGroup, Color: "violet", Icon: "bot"},
		{ID: "g2", Name: "aide", Root: fakeHome + "/Work/aide", Kind: model.ProjectGit, Color: "green"},
	}
	ws := func(id, group, name, branch, path string, ago time.Duration, tabs ...model.Tab) model.Workspace {
		w := model.Workspace{ID: id, ProjectID: group, Name: name, Branch: branch, Path: path, RepoRoot: path,
			UpdatedAt: now.Add(-ago), Tabs: tabs}
		if len(tabs) > 0 {
			w.ActiveTab = tabs[0].ID
		}
		return w
	}
	tab := func(id, title string, root *layout.Node) model.Tab {
		return model.Tab{ID: id, Title: title, Layout: root}
	}
	split := func(d layout.Dir, kids ...*layout.Node) *layout.Node {
		r := make([]float64, len(kids))
		for i := range r {
			r[i] = 1 / float64(len(kids))
		}
		return &layout.Node{Dir: d, Ratios: r, Children: kids}
	}
	leaf := func(id string) *layout.Node { return &layout.Node{Pane: id} }
	pitwall := ws("w1", "", "pitwall", "main", fakeHome+"/Work/pitwall", 20*time.Second,
		tab("t1", "go test", split(layout.Horizontal, leaf("a"), split(layout.Vertical, leaf("b"), leaf("c")))),
		tab("t2", "claude", leaf("i")),
		model.Tab{ID: "t3", Name: "logs", Title: "tail", Layout: leaf("j")})
	otter := ws("w7", "", "swift-otter-tangerine-harbor", "main", fakeHome+"/Work/pitwall", time.Hour, tab("t7", "claude", leaf("k")))
	otter.Detached = true
	heron := ws("w8", "", "calm-heron", "", "/tmp", 5*time.Hour, tab("t8", "zsh", leaf("l")))
	heron.Detached = true
	worktree := ws("w4", "g1", "fix flicker", "fix-flicker", fakeHome+"/Work/aide/.worktrees/fix-flicker", 5*time.Minute, tab("t4", "claude", leaf("g")))
	worktree.WorktreeRoot = fakeHome + "/Work/aide"
	f.st.Workspaces = []model.Workspace{
		pitwall,
		ws("w2", "", "me", "", fakeHome, 3*time.Hour, tab("t5", "zsh", leaf("d"))),
		ws("w3", "", "scratch", "", "/tmp/scratch", 2*time.Minute, tab("t6", "claude", split(layout.Horizontal, leaf("e"), leaf("f")))),
		worktree,
		ws("w5", "g1", "empty", "", fakeHome+"/notes", 26*time.Hour),
		ws("w6", "g2", "release", "release/1.4", fakeHome+"/Work/aide", 9*time.Minute, tab("t9", "codex", leaf("h"))),
		otter, heron,
	}
	f.st.Stats = map[string]model.BranchStats{
		"w1": {Additions: 412, Deletions: 38},
		"w4": {Additions: 18, Deletions: 44, MergeStatus: model.MergeConflicts},
		"w6": {Additions: 6, Deletions: 6},
	}
	agents := map[string]model.Provider{"a": model.ProviderTerminal, "e": model.ProviderClaude, "g": model.ProviderClaude,
		"h": model.ProviderCodex, "i": model.ProviderClaude, "k": model.ProviderClaude}
	for _, w := range f.st.Workspaces {
		for _, t := range w.Tabs {
			for _, p := range panesOf(t.Layout) {
				f.st.Panes = append(f.st.Panes, model.Pane{ID: p, WorkspaceID: w.ID, Cwd: w.Path, Provider: agents[p]})
			}
		}
	}
	f.setActivities()
	f.st.Version = 1
	return f
}

var fakeCycle = []model.AgentState{
	model.StateWorking, model.StatePendingApproval, model.StateWorking,
	model.StateAwaitingInput, model.StatePlanReady, model.StateCompleted, model.StateError,
}

// setActivities gives each agent pane its state for this tick. The
// terminal keeps running `go test` and the Claude in w3 keeps working; the
// grouped agents walk fakeCycle.
func (f *FakeBackend) setActivities() {
	f.st.Activities = nil
	for i, p := range f.st.Panes {
		a := model.Activity{PaneID: p.ID, WorkspaceID: p.WorkspaceID, Provider: p.Provider, SessionID: "s-" + p.ID, UpdatedAt: time.Now()}
		switch {
		case p.Provider == "":
			continue
		case p.Provider == model.ProviderTerminal:
			a.State, a.Detail, a.SessionID = model.StateTerminalRunning, "go", ""
		case p.WorkspaceID == "w3":
			a.State = model.StateWorking
		default:
			a.State = fakeCycle[(f.ticks+i*2)%len(fakeCycle)]
		}
		f.st.Activities = append(f.st.Activities, a)
	}
}

// Tick advances every agent to its next state.
func (f *FakeBackend) Tick() {
	f.mu.Lock()
	f.ticks++
	f.setActivities()
	f.bump()
	f.mu.Unlock()
}

// Run ticks every d until stop is closed.
func (f *FakeBackend) Run(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			f.Tick()
		case <-stop:
			return
		}
	}
}

// bump must be called with mu held.
func (f *FakeBackend) bump() {
	f.st.Version++
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *FakeBackend) State() model.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.st
	s.Projects = append([]model.Project(nil), s.Projects...)
	s.Panes = append([]model.Pane(nil), s.Panes...)
	s.Activities = append([]model.Activity(nil), s.Activities...)
	s.Workspaces = append([]model.Workspace(nil), s.Workspaces...)
	for i := range s.Workspaces {
		w := &s.Workspaces[i]
		w.Tabs = append([]model.Tab(nil), w.Tabs...)
		for j := range w.Tabs {
			w.Tabs[j].Layout = cloneNode(w.Tabs[j].Layout)
		}
	}
	return s
}

func (f *FakeBackend) Changed() <-chan struct{} { return f.changed }

// Focus implements Focuser.
func (f *FakeBackend) Focus() <-chan proto.FocusSession { return f.focus }

// RequestFocus stands in for `pitwall attach`: the daemon un-detaches the
// session and tells GUIs to show it. The fake leaves Detached for a later
// state, so the window has to show the session on its own first.
func (f *FakeBackend) RequestFocus(fs proto.FocusSession) { f.focus <- fs }

// tabFor is w's tab id, or its active one for "", creating a first tab for a
// session that has none. Called with mu held.
func (f *FakeBackend) tabFor(w *model.Workspace, id string) *model.Tab {
	if id == "" {
		id = w.ActiveTab
	}
	for i := range w.Tabs {
		if w.Tabs[i].ID == id {
			return &w.Tabs[i]
		}
	}
	if len(w.Tabs) > 0 {
		return &w.Tabs[0]
	}
	f.nextID++
	w.Tabs = []model.Tab{{ID: fmt.Sprintf("nt%d", f.nextID)}}
	w.ActiveTab = w.Tabs[0].ID
	return &w.Tabs[0]
}

// prune drops empty tabs and the sessions they leave empty, as the daemon
// does, moving ActiveTab to a neighbour. Called with mu held.
func (f *FakeBackend) prune(id string) {
	i := slices.IndexFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == id })
	if i < 0 || len(f.st.Workspaces[i].Tabs) == 0 {
		return
	}
	w := &f.st.Workspaces[i]
	if j := slices.IndexFunc(w.Tabs, func(t model.Tab) bool { return t.Layout == nil }); j >= 0 {
		w.Tabs = slices.Delete(w.Tabs, j, j+1)
		if len(w.Tabs) > 0 && !slices.ContainsFunc(w.Tabs, func(t model.Tab) bool { return t.ID == w.ActiveTab }) {
			w.ActiveTab = w.Tabs[min(j, len(w.Tabs)-1)].ID
		}
	}
	if len(w.Tabs) == 0 {
		f.st.Workspaces = slices.Delete(f.st.Workspaces, i, i+1)
	}
	live := map[string]bool{}
	for _, w := range f.st.Workspaces {
		for _, t := range w.Tabs {
			for _, p := range panesOf(t.Layout) {
				live[p] = true
			}
		}
	}
	f.st.Panes = slices.DeleteFunc(f.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == id && !live[p.ID] })
	f.setActivities()
}

// fakeScrollback is how many lines of history every fake pane has.
const fakeScrollback = 200

// Scroll implements Scroller.
func (f *FakeBackend) Scroll(pane string) (offset, max int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scroll[pane], fakeScrollback
}

// Sent returns every message passed to Send, oldest first.
func (f *FakeBackend) Sent() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.sent...)
}

func (f *FakeBackend) Frame(pane string) (vt.Grid, vt.Modes, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p *model.Pane
	for i := range f.st.Panes {
		if f.st.Panes[i].ID == pane {
			p = &f.st.Panes[i]
		}
	}
	if p == nil {
		return vt.Grid{}, vt.Modes{}, false
	}
	sz, ok := f.sizes[pane]
	if !ok {
		sz = [2]int{80, 24}
	}
	g := vt.Grid{Cols: sz[0], Rows: sz[1], Cells: make([]vt.Cell, sz[0]*sz[1]), Cursor: vt.Cursor{Visible: true}}
	who := string(p.Provider)
	if who == "" {
		who = "shell"
	}
	lines := []string{
		fmt.Sprintf("pane %s (%s) in %s", p.ID, who, p.WorkspaceID),
		fmt.Sprintf("%dx%d", sz[0], sz[1]),
		"$ " + strings.Repeat("~", 3),
	}
	for i := range g.Cells {
		g.Cells[i] = vt.Cell{Content: " ", Width: 1}
	}
	for y, l := range lines {
		for x, r := range l {
			if x < g.Cols && y < g.Rows {
				g.Cells[y*g.Cols+x].Content = string(r)
			}
		}
	}
	return g, vt.Modes{}, true
}

func (f *FakeBackend) Send(msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	ws := func(id string) *model.Workspace {
		for i := range f.st.Workspaces {
			if f.st.Workspaces[i].ID == id {
				return &f.st.Workspaces[i]
			}
		}
		return nil
	}
	switch m := msg.(type) {
	case proto.Resize:
		f.sizes[m.Pane] = [2]int{m.Cols, m.Rows}
	case proto.Scroll:
		f.scroll[m.Pane] = min(max(f.scroll[m.Pane]+m.Lines, 0), fakeScrollback)
	case proto.SetLayout:
		if w := ws(m.WorkspaceID); w != nil {
			f.tabFor(w, m.TabID).Layout = cloneNode(m.Layout)
		}
	case proto.OpenPane:
		w := ws(m.WorkspaceID)
		if w == nil {
			return fmt.Errorf("no workspace %q", m.WorkspaceID)
		}
		f.nextID++
		id := fmt.Sprintf("n%d", f.nextID)
		t := f.tabFor(w, m.TabID)
		t.Layout = splitTree(t.Layout, m.Target, id, m.Dir)
		f.st.Panes = append(f.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: m.Cmd})
	case proto.ClosePane:
		for _, p := range f.st.Panes {
			if w := ws(p.WorkspaceID); p.ID == m.Pane && w != nil {
				if t := tabOf(w, p.ID); t != nil {
					t.Layout = removeTree(t.Layout, p.ID)
				}
				f.prune(w.ID)
				break
			}
		}
	case proto.NewTab:
		w := ws(m.WorkspaceID)
		if w == nil {
			return fmt.Errorf("no workspace %q", m.WorkspaceID)
		}
		f.nextID++
		id := fmt.Sprintf("nt%d", f.nextID)
		w.Tabs = append(w.Tabs, model.Tab{ID: id, Title: "zsh", Layout: layout.Leaf(id + "p")})
		w.ActiveTab = id
		f.st.Panes = append(f.st.Panes, model.Pane{ID: id + "p", WorkspaceID: w.ID, Cwd: w.Path})
	case proto.CloseTab:
		if w := ws(m.WorkspaceID); w != nil {
			for i := range w.Tabs {
				if w.Tabs[i].ID == m.TabID {
					w.Tabs[i].Layout = nil
				}
			}
			f.prune(w.ID)
		}
	case proto.RenameTab:
		if w := ws(m.WorkspaceID); w != nil {
			for i := range w.Tabs {
				if w.Tabs[i].ID == m.TabID {
					w.Tabs[i].Name = m.Name
				}
			}
		}
	case proto.SelectTab:
		if w := ws(m.WorkspaceID); w != nil {
			w.ActiveTab = m.TabID
		}
	case proto.DetachSession:
		if w := ws(m.WorkspaceID); w != nil {
			w.Detached = m.Detached
		}
	case proto.KillSession:
		f.st.Workspaces = slices.DeleteFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
		f.st.Panes = slices.DeleteFunc(f.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == m.WorkspaceID })
		f.setActivities()
	case proto.GroupByFolder:
		w := ws(m.WorkspaceID)
		if w == nil || w.RepoRoot == "" {
			return nil
		}
		root, group := w.RepoRoot, ""
		for _, p := range f.st.Projects {
			if p.Root == root {
				group = p.ID
			}
		}
		if group == "" {
			f.nextID++
			group = fmt.Sprintf("ng%d", f.nextID)
			f.st.Projects = append(f.st.Projects, model.Project{ID: group, Name: filepath.Base(root), Root: root, Kind: model.ProjectFolder, Color: "neutral"})
		}
		for i := range f.st.Workspaces {
			x := &f.st.Workspaces[i]
			if x.RepoRoot == root && !slices.ContainsFunc(f.st.Projects, func(p model.Project) bool { return p.ID == x.ProjectID }) {
				x.ProjectID = group
			}
		}
	case proto.NewSession:
		f.nextID++
		id := fmt.Sprintf("ns%d", f.nextID)
		cwd := m.Cwd
		if cwd == "" {
			cwd = fakeHome
		}
		f.st.Workspaces = append(f.st.Workspaces, model.Workspace{ID: id, ProjectID: m.GroupID, Name: filepath.Base(cwd),
			Path: cwd, RepoRoot: cwd, UpdatedAt: time.Now(), ActiveTab: id + "t",
			Tabs: []model.Tab{{ID: id + "t", Title: "zsh", Layout: &layout.Node{Pane: id + "p"}}}})
		f.st.Panes = append(f.st.Panes, model.Pane{ID: id + "p", WorkspaceID: id, Cwd: cwd})
	case proto.SetSessionGroup:
		if w := ws(m.WorkspaceID); w != nil {
			w.ProjectID = m.GroupID
		}
	case proto.NewGroup:
		f.nextID++
		id := fmt.Sprintf("ng%d", f.nextID)
		f.st.Projects = append(f.st.Projects, model.Project{ID: id, Name: m.Name, Kind: model.ProjectGroup, Color: "neutral"})
		for _, w := range m.WorkspaceIDs {
			if w := ws(w); w != nil {
				w.ProjectID = id
			}
		}
	case proto.RenameGroup:
		for i := range f.st.Projects {
			if f.st.Projects[i].ID == m.GroupID {
				f.st.Projects[i].Name = m.Name
			}
		}
	case proto.DeleteGroup:
		f.st.Projects = slices.DeleteFunc(f.st.Projects, func(p model.Project) bool { return p.ID == m.GroupID })
		for i := range f.st.Workspaces {
			if f.st.Workspaces[i].ProjectID == m.GroupID {
				f.st.Workspaces[i].ProjectID = ""
			}
		}
	case proto.NewWorkspace:
		f.nextID++
		id := fmt.Sprintf("nw%d", f.nextID)
		name := m.Name
		if name == "" {
			name = "workspace " + id
		}
		f.st.Workspaces = append(f.st.Workspaces, model.Workspace{ID: id, ProjectID: m.ProjectID, Name: name, Branch: name, UpdatedAt: time.Now()})
	case proto.RenameWorkspace:
		if w := ws(m.WorkspaceID); w != nil {
			w.Name = m.Name
		}
	case proto.DeleteWorkspace:
		f.st.Workspaces = slices.DeleteFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
		f.st.Panes = slices.DeleteFunc(f.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == m.WorkspaceID })
		f.setActivities()
	case proto.AddProject:
		f.nextID++
		f.st.Projects = append(f.st.Projects, model.Project{ID: fmt.Sprintf("np%d", f.nextID), Name: filepath.Base(m.Path), Root: m.Path, Kind: model.ProjectFolder})
	case proto.SetProjectAppearance:
		for i := range f.st.Projects {
			if f.st.Projects[i].ID == m.ProjectID {
				f.st.Projects[i].Icon, f.st.Projects[i].Color = m.Icon, m.Color
			}
		}
	default:
		return nil // Input and the rest change nothing here
	}
	f.bump()
	return nil
}
