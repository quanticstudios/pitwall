package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// tabIndex is the index of w's tab id, or -1. "" means the active tab, else
// the first.
func tabIndex(w *model.Workspace, id string) int {
	find := func(id string) int { return slices.IndexFunc(w.Tabs, func(t model.Tab) bool { return t.ID == id }) }
	if id != "" {
		return find(id)
	}
	if i := find(w.ActiveTab); i >= 0 || len(w.Tabs) == 0 {
		return i
	}
	return 0
}

// paneTab is the index of the tab holding pane, or -1.
func paneTab(w *model.Workspace, pane string) int {
	return slices.IndexFunc(w.Tabs, func(t model.Tab) bool { return slices.Contains(layout.Panes(t.Layout), pane) })
}

// addTab opens a tab with a shell in cwd and makes it active. Callers hold
// d.mu.
func (d *Daemon) addTab(w *model.Workspace, cwd string) error {
	id := newID()
	if err := d.start(id, nil, cwd); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cwd: cwd})
	w.Tabs = append(w.Tabs, model.Tab{ID: newID(), Layout: &layout.Node{Pane: id}})
	w.ActiveTab = w.Tabs[len(w.Tabs)-1].ID
	return nil
}

// removePane forgets a pane and takes it out of its tab. A tab left without
// panes goes, and so does a session left without tabs. Callers hold d.mu and
// close the returned handles outside it.
func (d *Daemon) removePane(id string) []Pane {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		return nil
	}
	ws := d.st.Panes[i].WorkspaceID
	d.st.Panes = slices.Delete(d.st.Panes, i, i+1)
	out := []Pane{d.dropPane(id)}
	w := d.workspace(ws)
	if w == nil {
		return out
	}
	if ti := paneTab(w, id); ti >= 0 {
		if w.Tabs[ti].Layout = d.o.Remove(w.Tabs[ti].Layout, id); w.Tabs[ti].Layout == nil {
			out = append(out, d.removeTab(w, ti)...)
		}
	}
	return out
}

// removeTab closes a tab's panes and drops it, and its session when it was
// the last. Callers hold d.mu.
func (d *Daemon) removeTab(w *model.Workspace, ti int) []Pane {
	var out []Pane
	for _, id := range layout.Panes(w.Tabs[ti].Layout) {
		d.st.Panes = slices.DeleteFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
		out = append(out, d.dropPane(id))
	}
	w.Tabs = slices.Delete(w.Tabs, ti, ti+1)
	if len(w.Tabs) == 0 {
		return append(out, d.removeWorkspace(w.ID)...)
	}
	if tabIndex(w, w.ActiveTab) < 0 {
		w.ActiveTab = w.Tabs[min(ti, len(w.Tabs)-1)].ID
	}
	return out
}

// removeWorkspace drops a session and its panes; it never touches the disk.
// Callers hold d.mu.
func (d *Daemon) removeWorkspace(id string) []Pane {
	var out []Pane
	for _, p := range d.st.Panes {
		if p.WorkspaceID == id {
			out = append(out, d.dropPane(p.ID))
		}
	}
	d.st.Panes = slices.DeleteFunc(d.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == id })
	d.st.Workspaces = slices.DeleteFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == id })
	delete(d.st.Stats, id)
	return out
}

// newTab opens a tab, which is a session of its own: placed right after
// m.WorkspaceID (or FromPane's session) in the same group, with a shell in
// FromPane's live directory, else Cwd, else that session's Path.
func (d *Daemon) newTab(ctx context.Context, m proto.NewTab) error {
	d.mu.Lock()
	id := m.WorkspaceID
	if id == "" {
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.FromPane }); i >= 0 {
			id = d.st.Panes[i].WorkspaceID
		}
	}
	w := d.workspace(id)
	var cwd string
	if w != nil {
		cwd = cmp.Or(m.Cwd, w.Path)
	}
	d.mu.Unlock()
	if w == nil {
		return fmt.Errorf("no tab %s", cmp.Or(m.WorkspaceID, m.FromPane))
	}
	return d.addSession(ctx, proto.NewSession{Cwd: cwd, FromPane: m.FromPane}, id)
}

// renameTab names the session of m.Pane or m.WorkspaceID: a tab and its
// session are one thing. An empty name goes back to a generated one.
func (d *Daemon) renameTab(m proto.RenameTab) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := m.WorkspaceID
	if m.Pane != "" {
		i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
		if i < 0 {
			return fmt.Errorf("no pane %s", m.Pane)
		}
		id = d.st.Panes[i].WorkspaceID
	}
	w := d.workspace(id)
	if w == nil {
		return fmt.Errorf("no tab %s", id)
	}
	switch name := strings.TrimSpace(m.Name); {
	case name == "":
		if w.NameSet {
			w.Name, w.NameSet = d.freshName(), false
		}
	case d.nameTaken(name, w.ID):
		return fmt.Errorf("a tab is already named %s", name)
	default:
		w.Name, w.NameSet = name, true
	}
	d.changed()
	return nil
}

func (d *Daemon) killSession(m proto.KillSession) error {
	d.mu.Lock()
	if d.workspace(m.WorkspaceID) == nil {
		d.mu.Unlock()
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	closing := d.removeWorkspace(m.WorkspaceID)
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	return nil
}

// focusSession brings a session back and opens it on TabID, then asks every
// GUI to show it.
func (d *Daemon) focusSession(m proto.FocusSession) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	if m.TabID != "" {
		if tabIndex(w, m.TabID) < 0 {
			return fmt.Errorf("no tab %s in session %s", m.TabID, w.ID)
		}
		w.ActiveTab = m.TabID
	}
	w.Detached = false
	d.changed()
	for c := range d.clients {
		c.queue(m)
	}
	return nil
}

func (d *Daemon) renameWorkspace(m proto.RenameWorkspace) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("tab name is empty")
	}
	if d.nameTaken(m.Name, w.ID) {
		return fmt.Errorf("a tab is already named %s", m.Name)
	}
	w.Name, w.NameSet = m.Name, true
	d.changed()
	return nil
}

// nameTaken reports whether a session other than except has name. Callers
// hold d.mu.
func (d *Daemon) nameTaken(name, except string) bool {
	return slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.Name == name && w.ID != except })
}

// groupByFolder puts every ungrouped, attached session with the same
// RepoRoot as m.WorkspaceID into the project rooted there, made if needed.
func (d *Daemon) groupByFolder(ctx context.Context, m proto.GroupByFolder) error {
	d.mu.Lock()
	w := d.workspace(m.WorkspaceID)
	var root string
	if w != nil {
		root = cmp.Or(w.RepoRoot, w.Path)
	}
	d.mu.Unlock()
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	kind := model.ProjectFolder
	if _, git := d.o.RepoRoot(ctx, root); git {
		kind = model.ProjectGit
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	pi := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.Root == root })
	if pi < 0 {
		d.st.Projects = append(d.st.Projects, model.Project{ID: newID(), Name: filepath.Base(root), Root: root, Kind: kind, Color: "neutral"})
		pi = len(d.st.Projects) - 1
	}
	for i := range d.st.Workspaces {
		if w := &d.st.Workspaces[i]; w.ProjectID == "" && !w.Detached && cmp.Or(w.RepoRoot, w.Path) == root {
			w.ProjectID = d.st.Projects[pi].ID
		}
	}
	d.changed()
	return nil
}

// projectAt is the project whose Root holds path, the deepest one when
// several do, or "". Callers hold d.mu.
func (d *Daemon) projectAt(path string) string {
	id, best := "", ""
	for _, p := range d.st.Projects {
		if p.Root != "" && len(p.Root) > len(best) && (path == p.Root || strings.HasPrefix(path, strings.TrimSuffix(p.Root, "/")+"/")) {
			id, best = p.ID, p.Root
		}
	}
	return id
}

// repoRoot is the git toplevel of path, or path outside a repo.
func (d *Daemon) repoRoot(ctx context.Context, path string) string {
	if root, ok := d.o.RepoRoot(ctx, path); ok && root != "" {
		return root
	}
	return path
}

// setTitle records a pane's title, cleaned, and pushes state only when the
// cleaned title changed: a spinner turning is no change.
func (d *Daemon) setTitle(id string, p Pane, raw string) {
	t := cleanTitle(raw)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.panes[id] != p {
		return
	}
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id })
	if i < 0 || d.st.Panes[i].Title == t {
		return
	}
	d.st.Panes[i].Title = t
	d.changed()
}

// retitle sets every tab's Title and every session's Label. Callers hold
// d.mu.
func (d *Daemon) retitle() {
	panes := make(map[string]*model.Pane, len(d.st.Panes))
	for i := range d.st.Panes {
		panes[d.st.Panes[i].ID] = &d.st.Panes[i]
	}
	running := map[string]string{}
	for _, a := range d.st.Activities {
		if a.State == model.StateTerminalRunning {
			running[a.PaneID] = a.Detail
		}
	}
	for i := range d.st.Workspaces {
		w := &d.st.Workspaces[i]
		for j := range w.Tabs {
			w.Tabs[j].Title = tabTitle(w, &w.Tabs[j], panes, running)
		}
		if ti := tabIndex(w, ""); ti >= 0 {
			w.Label = w.Tabs[ti].Title
		} else {
			w.Label = tabTitle(w, &model.Tab{}, panes, running)
		}
	}
}

// tabTitle is the tab's title by the priority model.Tab.Title documents.
// It is never empty.
func tabTitle(w *model.Workspace, t *model.Tab, panes map[string]*model.Pane, running map[string]string) string {
	if w.NameSet {
		return w.Name
	}
	ids := layout.Panes(t.Layout)
	agent := func(p *model.Pane) bool {
		return p.Provider == model.ProviderClaude || p.Provider == model.ProviderCodex
	}
	for _, id := range ids {
		if p := panes[id]; p != nil && agent(p) && !genericTitle(w, p.Title) {
			return p.Title
		}
	}
	for _, id := range ids {
		if p := panes[id]; p != nil && p.Prompt != "" {
			return p.Prompt
		}
	}
	// A shell at its prompt titles itself user@host:dir, which the
	// directory says better; a running program's own title counts.
	for _, id := range ids {
		if p := panes[id]; p != nil && running[id] != "" && !genericTitle(w, p.Title) {
			return p.Title
		}
	}
	for _, id := range ids {
		if c := running[id]; c != "" {
			return c
		}
	}
	if len(ids) > 0 && panes[ids[0]] != nil && panes[ids[0]].Cwd != "" {
		return dirName(panes[ids[0]].Cwd)
	}
	return cmp.Or(dirName(w.Path), w.Name)
}

// dirName is how a title shows a directory: "~" for home, else its base name.
func dirName(dir string) string {
	switch dir {
	case "":
		return ""
	case homeDir():
		return "~"
	}
	return filepath.Base(dir)
}

// genericTitle reports a title that says nothing about the work: empty, an
// agent's own name, or the name of the session's directory, repo or user.
func genericTitle(w *model.Workspace, title string) bool {
	switch strings.ToLower(title) {
	case "", "~", "claude", "claude code", "codex":
		return true
	}
	for _, s := range []string{filepath.Base(w.Path), filepath.Base(w.RepoRoot), loginName()} {
		if s != "" && s != "." && s != "/" && strings.EqualFold(title, s) {
			return true
		}
	}
	return false
}

var loginName = sync.OnceValue(func() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
})

// promptTitle is a prompt's first non-blank line with whitespace collapsed,
// cut at a word boundary to at most promptRunes runes ending in "…".
func promptTitle(prompt string) string {
	line := ""
	for l := range strings.Lines(prompt) {
		if line = strings.Join(strings.Fields(l), " "); line != "" {
			break
		}
	}
	r := []rune(line)
	if len(r) <= promptRunes {
		return line
	}
	cut := string(r[:promptRunes-1])
	if r[promptRunes-1] != ' ' {
		if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i]
		}
	}
	return strings.TrimRight(cut, " ") + "…"
}

const promptRunes = 48

// cleanTitle strips the spinner and status glyphs agents put before their
// title (Codex a braille spinner, Claude Code ✳ and its kin) and spaces.
func cleanTitle(s string) string {
	return strings.TrimSpace(strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x2800 && r <= 0x28ff) || strings.ContainsRune("✳✶✻✽✢✺·•*●○◐◓◑◒⏺", r)
	}))
}
