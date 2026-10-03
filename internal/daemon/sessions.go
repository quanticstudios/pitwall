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

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// newSession opens a tab holding a shell in m.Cwd ("" means $HOME), named
// m.Name or unnamed, in m.SessionID (see proto.NewSession). Without a
// group, it joins the session's project whose folder holds its directory;
// ungrouped, it goes last.
func (d *Daemon) newSession(ctx context.Context, m proto.NewSession) error {
	return d.addSession(ctx, m, "", nil)
}

// addSession is newSession, placing the tab right after the tab after, in
// its group or, when after is ungrouped, at the top level. With create set
// the tab goes into a new session named *create (generated when "").
func (d *Daemon) addSession(ctx context.Context, m proto.NewSession, after string, create *string) error {
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
	root := d.repoRoot(ctx, path)

	d.mu.Lock()
	defer d.mu.Unlock()
	at, sessions := len(d.st.Workspaces), len(d.st.Sessions)
	switch {
	case create != nil:
		if *create != "" && d.st.SessionNamed(*create) != nil {
			return fmt.Errorf("a session is already named %s", *create)
		}
		m.SessionID, m.GroupID = d.makeSession(*create), ""
	case after != "":
		i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == after })
		if i < 0 {
			return fmt.Errorf("no workspace %s", after)
		}
		at, m.GroupID, m.SessionID = i+1, d.st.Workspaces[i].ProjectID, d.st.Workspaces[i].SessionID
	default:
		if m.GroupID != "" {
			m.SessionID = d.st.SessionOf(m.GroupID)
		}
		if m.SessionID, err = d.targetSession(m.SessionID, m.FromPane); err != nil {
			return err
		}
		if m.GroupID == "" {
			m.GroupID = d.projectAt(m.SessionID, path)
		}
	}
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if m.Name != "" && d.nameTaken(m.SessionID, m.Name, "") {
		return fmt.Errorf("a tab is already named %s", m.Name)
	}
	w := model.Workspace{ID: newID(), SessionID: m.SessionID, ProjectID: m.GroupID, Name: m.Name, NameSet: m.Name != "", Branch: branch, Path: path, RepoRoot: root, UpdatedAt: time.Now()}
	if err := d.addTab(&w, path); err != nil {
		d.st.Sessions = d.st.Sessions[:sessions] // drop one made for this tab
		return err
	}
	d.st.Workspaces = slices.Insert(d.st.Workspaces, at, w)
	if after != "" && m.GroupID == "" {
		d.st.PlaceTopAfter(w.ID, after)
	}
	d.changed()
	if branch != "" {
		go d.refreshStats(ctx, w.ID)
	}
	return nil
}

func (d *Daemon) newGroup(m proto.NewGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	session := ""
	for i, id := range m.WorkspaceIDs {
		w := d.workspace(id)
		if w == nil {
			return fmt.Errorf("no workspace %s", id)
		}
		if i > 0 && w.SessionID != session {
			return errors.New("a group's tabs must be in one session")
		}
		session = w.SessionID
	}
	if session == "" {
		if s := d.st.Recent(); s != nil {
			session = s.ID
		}
	}
	name := m.Name
	for n := 1; name == ""; n++ {
		if s := fmt.Sprintf("Group %d", n); !slices.ContainsFunc(d.st.Projects, func(p model.Project) bool { return p.SessionID == session && p.Name == s }) {
			name = s
		}
	}
	// ponytail: the UI's color list is unexported in internal/ui/theme, so
	// groups start "neutral"; the user picks a color with SetProjectAppearance.
	g := model.Project{ID: newID(), SessionID: session, Name: name, Kind: model.ProjectGroup, Color: "neutral"}
	d.st.Projects = append(d.st.Projects, g)
	d.st.PlaceTop(g.ID, d.firstTop(d.topSlots(m.WorkspaceIDs)))
	for _, id := range m.WorkspaceIDs {
		d.workspace(id).ProjectID = g.ID
	}
	d.changed()
	return nil
}

// setSessionGroup moves a session into a group, last, or ungroups it right
// after the group it was in.
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
	if m.GroupID != "" && d.project(m.GroupID).SessionID != w.SessionID {
		return fmt.Errorf("group %s is in another session", m.GroupID)
	}
	old := w.ProjectID
	w.ProjectID = m.GroupID
	if m.GroupID == "" && old != "" {
		d.st.PlaceTopAfter(w.ID, old)
	}
	d.changed()
	return nil
}

// topSlots maps each session to the top-level item it shows under: itself
// when ungrouped, else its group. Callers hold d.mu.
func (d *Daemon) topSlots(ids []string) []string {
	var out []string
	for _, id := range ids {
		if w := d.workspace(id); w != nil && w.ProjectID != "" && d.project(w.ProjectID) != nil {
			out = append(out, w.ProjectID)
		} else {
			out = append(out, id)
		}
	}
	return out
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

// deleteGroup ungroups the group's sessions into the group's place, in
// their order, and drops the group; their panes and directories stay.
func (d *Daemon) deleteGroup(m proto.DeleteGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	d.st.DeleteGroup(m.GroupID)
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
