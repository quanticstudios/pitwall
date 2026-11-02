package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

// recordClosed adds tab w, which is closing, to State.Closed for tab search
// to reopen. gone are its panes already taken out of st.Panes. A chat tab has
// its own archive and a tab with no pane nothing to reopen. Callers hold d.mu.
func (d *Daemon) recordClosed(w model.Workspace, gone []model.Pane) {
	if w.Chat != "" {
		return
	}
	panes := slices.Clone(gone)
	for _, p := range d.st.Panes {
		if p.WorkspaceID == w.ID {
			panes = append(panes, d.liveCwd(p))
		}
	}
	if len(panes) == 0 {
		return
	}
	p := panes[0]
	if i := slices.IndexFunc(panes, store.Resumes); i >= 0 {
		p = panes[i]
	}
	c := model.ClosedTab{ID: newID(), Pane: p, PR: d.st.PRs[w.ID].Number, ClosedAt: time.Now()}
	w.Tabs, w.ActiveTab = nil, ""
	c.Tab = w
	if s := d.st.Session(w.SessionID); s != nil {
		c.Session = s.Name
	}
	if g := d.project(w.ProjectID); g != nil {
		c.Group = *g
	}
	c.Gone = !isDir(c.Folder())
	d.st.Closed = model.PruneClosed(append(d.st.Closed, c), c.ClosedAt)
}

// liveCwd is p with the directory its process is in now, while its handle is
// still open. Callers hold d.mu.
func (d *Daemon) liveCwd(p model.Pane) model.Pane {
	if h := d.panes[p.ID]; h != nil && !p.Exited {
		if cwd := h.Cwd(); cwd != "" {
			p.Cwd = cwd
		}
	}
	return p
}

// closedTitle names closed tab w in an error: its name, else what it showed.
func closedTitle(w model.Workspace) string {
	if w.NameSet {
		return w.Name
	}
	return cmp.Or(w.Label, w.Name, filepath.Base(w.Path))
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// checkClosed works out which closed tabs' folders are gone, outside d.mu,
// and drops the records past PruneClosed's bounds.
func (d *Daemon) checkClosed() {
	d.mu.Lock()
	dirs := map[string]string{}
	for _, c := range d.st.Closed {
		dirs[c.ID] = c.Folder()
	}
	d.mu.Unlock()
	gone := map[string]bool{}
	for id, dir := range dirs {
		gone[id] = !isDir(dir)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.st.Closed)
	d.st.Closed = model.PruneClosed(d.st.Closed, time.Now())
	moved := n != len(d.st.Closed)
	for i := range d.st.Closed {
		if c := &d.st.Closed[i]; c.Gone != gone[c.ID] && dirs[c.ID] != "" {
			c.Gone, moved = gone[c.ID], true
		}
	}
	if moved {
		d.changed()
	}
}

// reopenTab opens closed tab m.ID again; see proto.ReopenTab.
func (d *Daemon) reopenTab(ctx context.Context, m proto.ReopenTab) error {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Closed, func(c model.ClosedTab) bool { return c.ID == m.ID })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no closed tab %s", m.ID)
	}
	c := d.st.Closed[i]
	d.mu.Unlock()
	dir := c.Folder()
	if m.AtRoot {
		dir = c.Root()
	}
	if dir == "" || !isDir(dir) {
		d.checkClosed()
		if m.AtRoot {
			return fmt.Errorf("the repository of %q is gone too", closedTitle(c.Tab))
		}
		return fmt.Errorf("%s is gone", dir)
	}
	// why: the tab keeps its own folder, the worktree root Archive removes,
	// and only the pane goes back to the subfolder it was in.
	path := dir
	if !m.AtRoot && isDir(c.Tab.Path) {
		path = c.Tab.Path
	}
	branch, _ := d.o.Branch(ctx, path)
	root := d.repoRoot(ctx, path)
	worktree := c.Tab.WorktreeRoot != "" && path == c.Tab.Path
	var base, step int
	if worktree && d.o.Worktrees != nil {
		wt, _ := d.o.Worktrees(c.Tab.WorktreeRoot)
		base, step = wt.PortBase, wt.PortStep
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if i = slices.IndexFunc(d.st.Closed, func(r model.ClosedTab) bool { return r.ID == m.ID }); i < 0 {
		return fmt.Errorf("no closed tab %s", m.ID) // reopened by another window meanwhile
	}
	w := c.Tab
	w.ID, w.SessionID = c.ID, d.reopenSession(c)
	w.ProjectID = d.reopenGroup(c, w.SessionID)
	w.Path, w.Branch, w.RepoRoot = path, branch, root
	w.Detached, w.Pinned, w.Chat, w.Status, w.Lane = false, false, "", "", model.Lane{}
	w.Settled, w.SnoozedUntil, w.SnoozedAt = time.Time{}, time.Time{}, time.Time{}
	w.UpdatedAt = time.Now()
	if d.workspace(w.Parent) == nil {
		w.Parent = ""
	}
	if w.NameSet && d.nameTaken(w.SessionID, w.Name, "") {
		w.Name, w.NameSet = "", false
	}
	w.WorktreeRoot, w.Ports = "", model.PortBlock{}
	if worktree {
		w.WorktreeRoot, w.Ports = c.Tab.WorktreeRoot, portBlock(d.st.Workspaces, base, step)
	}
	p := c.Pane
	p.ID, p.WorkspaceID, p.Cwd = newID(), w.ID, dir
	p.Exited, p.ExitCode, p.ExitUnknown = false, 0, false
	if !store.Resumes(p) {
		// why: a command that is not an agent's resume, such as gh pr merge,
		// must not run again; the tab comes back as a shell in its folder.
		p = model.Pane{ID: p.ID, WorkspaceID: w.ID, Cwd: dir, Env: p.Env}
	}
	w.Tabs = []model.Tab{{ID: newID(), Layout: &layout.Node{Pane: p.ID}}}
	w.ActiveTab = w.Tabs[0].ID
	d.st.Workspaces = append(d.st.Workspaces, w)
	d.st.Panes = append(d.st.Panes, p)
	if err := d.restorePane(p, nil); err != nil {
		d.st.Panes = d.st.Panes[:len(d.st.Panes)-1]
		d.st.Workspaces = d.st.Workspaces[:len(d.st.Workspaces)-1]
		d.endSession(w.SessionID)
		d.changed()
		return errors.Join(fmt.Errorf("reopen %q", closedTitle(c.Tab)), err)
	}
	d.st.Closed = slices.Delete(d.st.Closed, i, i+1)
	d.changed()
	if branch != "" {
		go d.refreshStats(ctx, w.ID)
	}
	return nil
}

// reopenSession is the session closed tab c reopens in: its own, else one
// with its name, else a new one with its name. Callers hold d.mu.
func (d *Daemon) reopenSession(c model.ClosedTab) string {
	if d.st.Session(c.Tab.SessionID) != nil {
		return c.Tab.SessionID
	}
	if s := d.st.SessionNamed(c.Session); s != nil && c.Session != "" {
		return s.ID
	}
	return d.makeSession(c.Session)
}

// reopenGroup is the group closed tab c rejoins in session: its own, else
// the session's group with its name, else that group made again. "" for a
// tab that had none. Callers hold d.mu.
func (d *Daemon) reopenGroup(c model.ClosedTab, session string) string {
	if c.Group.ID == "" {
		return ""
	}
	if g := d.project(c.Group.ID); g != nil && g.SessionID == session {
		return g.ID
	}
	for _, g := range d.st.Projects {
		if g.SessionID == session && g.Name == c.Group.Name {
			return g.ID
		}
	}
	g := c.Group
	g.ID, g.SessionID = newID(), session
	d.st.Projects = append(d.st.Projects, g)
	return g.ID
}

// forgetTab drops closed tab m.ID.
func (d *Daemon) forgetTab(m proto.ForgetTab) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Closed, func(c model.ClosedTab) bool { return c.ID == m.ID })
	if i < 0 {
		return fmt.Errorf("no closed tab %s", m.ID)
	}
	d.st.Closed = slices.Delete(d.st.Closed, i, i+1)
	d.changed()
	return nil
}
