package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// firstSession opens a session in cwd when none is open, so a GUI that
// connects to an empty daemon lands in a shell. helloMu keeps two GUIs that
// connect at once from opening two.
func (d *Daemon) firstSession(ctx context.Context, cwd string) error {
	d.helloMu.Lock()
	defer d.helloMu.Unlock()
	d.mu.Lock()
	open := slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return !w.Archived })
	d.mu.Unlock()
	if open {
		return nil
	}
	if fi, err := os.Stat(cwd); cwd == "" || err != nil || !fi.IsDir() {
		cwd = ""
	}
	return d.newSession(ctx, proto.NewSession{Cwd: cwd})
}

// newSession opens a session with a shell pane in m.Cwd ("" means $HOME),
// named after the directory.
func (d *Daemon) newSession(ctx context.Context, m proto.NewSession) error {
	home := homeDir()
	path := m.Cwd
	if m.FromPane != "" {
		d.mu.Lock()
		p := d.panes[m.FromPane]
		d.mu.Unlock()
		if p != nil {
			if c := p.Cwd(); c != "" {
				path = c
			}
		}
	}
	if path == "" {
		path = home
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(path); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	branch, _ := d.o.Branch(ctx, path)
	base := filepath.Base(path)
	if path == home {
		base = "~"
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	name := base
	for n := 2; slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.Name == name }); n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	w := model.Workspace{ID: newID(), ProjectID: m.GroupID, Name: name, Branch: branch, Path: path, UpdatedAt: time.Now()}
	id := newID()
	if err := d.start(id, nil, path); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cwd: path})
	w.Layout = &layout.Node{Pane: id}
	d.st.Workspaces = append(d.st.Workspaces, w)
	d.changed()
	if branch != "" {
		go d.refreshStats(ctx, w.ID)
	}
	return nil
}

func (d *Daemon) newGroup(m proto.NewGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range m.WorkspaceIDs {
		if d.workspace(id) == nil {
			return fmt.Errorf("no workspace %s", id)
		}
	}
	name := m.Name
	for n := 1; name == ""; n++ {
		if s := fmt.Sprintf("Group %d", n); !slices.ContainsFunc(d.st.Projects, func(p model.Project) bool { return p.Name == s }) {
			name = s
		}
	}
	// ponytail: the UI's color list is unexported in internal/ui/theme, so
	// groups start "neutral"; the user picks a color with SetProjectAppearance.
	g := model.Project{ID: newID(), Name: name, Kind: model.ProjectGroup, Color: "neutral"}
	d.st.Projects = append(d.st.Projects, g)
	for _, id := range m.WorkspaceIDs {
		d.workspace(id).ProjectID = g.ID
	}
	d.changed()
	return nil
}

func (d *Daemon) setSessionGroup(m proto.SetSessionGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	w.ProjectID = m.GroupID
	d.changed()
	return nil
}

func (d *Daemon) renameGroup(m proto.RenameGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.project(m.GroupID)
	if p == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("group name is empty")
	}
	p.Name = m.Name
	d.changed()
	return nil
}

// deleteGroup ungroups the group's sessions and drops the group; their panes
// and directories stay.
func (d *Daemon) deleteGroup(m proto.DeleteGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	for i := range d.st.Workspaces {
		if d.st.Workspaces[i].ProjectID == m.GroupID {
			d.st.Workspaces[i].ProjectID = ""
		}
	}
	d.st.Projects = slices.DeleteFunc(d.st.Projects, func(p model.Project) bool { return p.ID == m.GroupID })
	d.changed()
	return nil
}

// project returns a pointer into d.st; callers hold d.mu.
func (d *Daemon) project(id string) *model.Project {
	if i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == id }); i >= 0 {
		return &d.st.Projects[i]
	}
	return nil
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// gitBranch reports whether path is in a git work tree and its branch there,
// "" for a detached HEAD.
func gitBranch(ctx context.Context, path string) (string, bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", path, "symbolic-ref", "--short", "-q", "HEAD").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out)), true
	case errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil:
		return "", true // detached
	}
	return "", false
}
