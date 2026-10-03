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

// FakeBackend is an in-memory Backend with a few tabs, two groups and
// panes showing static text. Tick moves agent states so the UI has
// something to redraw. Like the daemon, every workspace is one tab (one
// model.Tab of panes), and it drops exited panes and the tabs they empty.
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

// fakeNames are the handles the fake gives new tabs, like the daemon's.
var fakeNames = []string{"quick-lynx", "keen-wren", "soft-moth", "wise-crab", "pale-newt", "deep-carp"}

// NewFakeBackend returns five ungrouped tabs (a terminal running `go test`
// split three ways, a Claude, a named log tail, an idle shell, a working
// Claude), two groups, and two detached tabs.
func NewFakeBackend() *FakeBackend {
	f := &FakeBackend{sizes: map[string][2]int{}, scroll: map[string]int{}, changed: make(chan struct{}, 1),
		focus: make(chan proto.FocusSession, 8)}
	now := time.Now()
	f.st.Projects = []model.Project{
		{ID: "g1", Name: "agents", Kind: model.ProjectGroup, Color: "violet", Icon: "bot"},
		{ID: "g2", Name: "aide", Root: fakeHome + "/src/web-app", Kind: model.ProjectGit, Color: "green"},
	}
	ws := func(id, group, name, label, branch, path string, ago time.Duration, tab, title string, root *layout.Node) model.Workspace {
		w := model.Workspace{ID: id, ProjectID: group, Name: name, Label: label, Branch: branch, Path: path, RepoRoot: path,
			UpdatedAt: now.Add(-ago)}
		if tab != "" {
			w.Tabs, w.ActiveTab = []model.Tab{{ID: tab, Title: title, Layout: root}}, tab
		}
		return w
	}
	split := func(d layout.Dir, kids ...*layout.Node) *layout.Node {
		r := make([]float64, len(kids))
		for i := range r {
			r[i] = 1 / float64(len(kids))
		}
		return &layout.Node{Dir: d, Ratios: r, Children: kids}
	}
	leaf := func(id string) *layout.Node { return &layout.Node{Pane: id} }
	pw := fakeHome + "/Work/pitwall"
	logs := ws("w1c", "", "logs", "tail -f daemon.log", "main", pw, 4*time.Minute, "t3", "tail", leaf("j"))
	logs.NameSet = true
	scratch := ws("w3", "", "scratch", "Sketch the switcher", "", "/tmp/scratch", 2*time.Minute, "t6", "claude", split(layout.Horizontal, leaf("e"), leaf("f")))
	scratch.NameSet = true
	otter := ws("w7", "", "swift-otter", "Resume the store", "main", pw, time.Hour, "t7", "claude", leaf("k"))
	otter.Detached = true
	heron := ws("w8", "", "calm-heron", "tmp", "", "/tmp", 5*time.Hour, "t8", "zsh", leaf("l"))
	heron.Detached = true
	worktree := ws("w4", "g1", "brave-ant", "Fix the resize flicker", "fix-flicker", fakeHome+"/src/web-app/.worktrees/fix-flicker", 5*time.Minute, "t4", "claude", leaf("g"))
	worktree.WorktreeRoot = fakeHome + "/src/web-app"
	f.st.Workspaces = []model.Workspace{
		ws("w1", "", "fast-bee", "go test", "main", pw, 20*time.Second, "t1", "go test", split(layout.Horizontal, leaf("a"), split(layout.Vertical, leaf("b"), leaf("c")))),
		ws("w1b", "", "bold-fox", "Port the sidebar drag", "main", pw, 40*time.Second, "t2", "claude", leaf("i")),
		logs,
		ws("w2", "", "warm-elk", "~", "", fakeHome, 3*time.Hour, "t5", "zsh", leaf("d")),
		scratch,
		worktree,
		ws("w5", "g1", "tidy-yak", "notes", "", fakeHome+"/notes", 26*time.Hour, "", "", nil),
		ws("w6", "g2", "lazy-cod", "Cut release 1.4", "release/1.4", fakeHome+"/src/web-app", 9*time.Minute, "t9", "codex", leaf("h")),
		otter, heron,
	}
	f.st.Stats = map[string]model.BranchStats{
		"w1": {Additions: 412, Deletions: 38}, "w1b": {Additions: 412, Deletions: 38}, "w1c": {Additions: 412, Deletions: 38},
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

// newWorkspace makes a one-tab workspace with a shell in cwd and adds its
// pane. Called with mu held.
func (f *FakeBackend) newWorkspace(group, cwd string) model.Workspace {
	f.nextID++
	id := fmt.Sprintf("ns%d", f.nextID)
	label := filepath.Base(cwd)
	if cwd == fakeHome {
		label = "~"
	}
	f.st.Panes = append(f.st.Panes, model.Pane{ID: id + "p", WorkspaceID: id, Cwd: cwd})
	return model.Workspace{ID: id, ProjectID: group, Name: fakeNames[(f.nextID-1)%len(fakeNames)], Label: label,
		Path: cwd, RepoRoot: cwd, UpdatedAt: time.Now(), ActiveTab: id + "t",
		Tabs: []model.Tab{{ID: id + "t", Title: "zsh", Layout: &layout.Node{Pane: id + "p"}}}}
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
		// A new workspace right after WorkspaceID in its group, with a
		// shell where FromPane's shell is.
		i := slices.IndexFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
		if i < 0 {
			return fmt.Errorf("no workspace %q", m.WorkspaceID)
		}
		src := f.st.Workspaces[i]
		cwd := src.Path
		for _, p := range f.st.Panes {
			if p.ID == m.FromPane && p.Cwd != "" {
				cwd = p.Cwd
			}
		}
		w := f.newWorkspace(src.ProjectID, cwd)
		w.Branch = src.Branch
		f.st.Workspaces = slices.Insert(f.st.Workspaces, i+1, w)
	case proto.CloseTab:
		f.st.Workspaces = slices.DeleteFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
		f.st.Panes = slices.DeleteFunc(f.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == m.WorkspaceID })
		f.setActivities()
	case proto.RenameTab:
		if w := ws(m.WorkspaceID); w != nil {
			if m.Name != "" {
				w.Name = m.Name
			}
			w.NameSet = m.Name != ""
		}
	case proto.MoveSession:
		i := slices.IndexFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
		if i < 0 || m.Before == m.WorkspaceID {
			return nil
		}
		w := f.st.Workspaces[i]
		w.ProjectID = m.GroupID
		f.st.Workspaces = slices.Delete(f.st.Workspaces, i, i+1)
		j := slices.IndexFunc(f.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.Before })
		if j < 0 {
			j = len(f.st.Workspaces)
		}
		f.st.Workspaces = slices.Insert(f.st.Workspaces, j, w)
	case proto.MoveGroup:
		i := slices.IndexFunc(f.st.Projects, func(p model.Project) bool { return p.ID == m.GroupID })
		if i < 0 || m.Before == m.GroupID {
			return nil
		}
		p := f.st.Projects[i]
		f.st.Projects = slices.Delete(f.st.Projects, i, i+1)
		j := slices.IndexFunc(f.st.Projects, func(p model.Project) bool { return p.ID == m.Before })
		if j < 0 {
			j = len(f.st.Projects)
		}
		f.st.Projects = slices.Insert(f.st.Projects, j, p)
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
		cwd := m.Cwd
		if cwd == "" {
			cwd = fakeHome
		}
		for _, p := range f.st.Panes {
			if p.ID == m.FromPane && p.Cwd != "" {
				cwd = p.Cwd
			}
		}
		w := f.newWorkspace(m.GroupID, cwd)
		if m.Name != "" {
			w.Name, w.NameSet = m.Name, true
		}
		f.st.Workspaces = append(f.st.Workspaces, w)
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
		f.st.Workspaces = append(f.st.Workspaces, model.Workspace{ID: id, ProjectID: m.ProjectID, Name: name, Label: name, Branch: name, UpdatedAt: time.Now()})
	case proto.RenameWorkspace:
		if w := ws(m.WorkspaceID); w != nil {
			w.Name, w.NameSet = m.Name, true
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
