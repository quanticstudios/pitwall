package daemon

import (
	"fmt"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// moveSession regroups a session and places it. Into a group it moves in
// State.Workspaces before the session Before of that group; at the top
// level it moves in State.Order before the group or ungrouped tab Before.
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
	if m.GroupID == "" {
		if m.Before != "" && !d.topLevel(m.Before) {
			return fmt.Errorf("no top-level group or tab %s", m.Before)
		}
		d.st.Workspaces[i].ProjectID = ""
		d.st.PlaceTop(m.WorkspaceID, m.Before)
		d.changed()
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

// moveGroup places a group in State.Order before the group or ungrouped
// tab Before, or last.
func (d *Daemon) moveGroup(m proto.MoveGroup) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.project(m.GroupID) == nil {
		return fmt.Errorf("no group %s", m.GroupID)
	}
	if m.Before == m.GroupID {
		return nil
	}
	if m.Before != "" && !d.topLevel(m.Before) {
		return fmt.Errorf("no top-level group or tab %s", m.Before)
	}
	d.st.PlaceTop(m.GroupID, m.Before)
	d.changed()
	return nil
}

// topLevel reports whether id is a group or an ungrouped tab. Callers hold
// d.mu.
func (d *Daemon) topLevel(id string) bool {
	return slices.Contains(d.st.TopOrder(), id)
}

// firstTop is the first top-level item, in order, that is one of ids, or
// "". Callers hold d.mu.
func (d *Daemon) firstTop(ids []string) string {
	for _, id := range d.st.TopOrder() {
		if slices.Contains(ids, id) {
			return id
		}
	}
	return ""
}
