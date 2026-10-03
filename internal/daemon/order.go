package daemon

import (
	"fmt"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// moveSession regroups a session and moves it in State.Workspaces, whose
// order is the sidebar's order.
func (d *Daemon) moveSession(m proto.MoveSession) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.GroupID != "" && d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == m.WorkspaceID })
	if i < 0 {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	if m.Before == m.WorkspaceID {
		return nil
	}
	w := d.st.Workspaces[i]
	w.ProjectID = m.GroupID
	rest := slices.Delete(slices.Clone(d.st.Workspaces), i, i+1)
	at := len(rest)
	if m.Before != "" {
		if at = slices.IndexFunc(rest, func(x model.Workspace) bool { return x.ID == m.Before }); at < 0 {
			return fmt.Errorf("no workspace %s", m.Before)
		}
	}
	d.st.Workspaces = slices.Insert(rest, at, w)
	d.changed()
	return nil
}

// moveGroup moves a group in State.Projects, whose order is the sidebar's.
func (d *Daemon) moveGroup(m proto.MoveGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == m.GroupID })
	if i < 0 {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if m.Before == m.GroupID {
		return nil
	}
	p := d.st.Projects[i]
	rest := slices.Delete(slices.Clone(d.st.Projects), i, i+1)
	at := len(rest)
	if m.Before != "" {
		if at = slices.IndexFunc(rest, func(x model.Project) bool { return x.ID == m.Before }); at < 0 {
			return fmt.Errorf("no group %s", m.Before)
		}
	}
	d.st.Projects = slices.Insert(rest, at, p)
	d.changed()
	return nil
}
