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
	session := d.st.Workspaces[i].SessionID
	if m.GroupID != "" && d.project(m.GroupID).SessionID != session {
		return fmt.Errorf("group %s is in another session", m.GroupID)
	}
	if m.GroupID == "" {
		if m.Before != "" && !d.topLevel(session, m.Before) {
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
	if m.Before != "" && !d.topLevel(d.project(m.GroupID).SessionID, m.Before) {
		return fmt.Errorf("no top-level group or tab %s", m.Before)
	}
	d.st.PlaceTop(m.GroupID, m.Before)
	d.changed()
	return nil
}

// topLevel reports whether id is a group or an ungrouped tab of session.
// Callers hold d.mu.
func (d *Daemon) topLevel(session, id string) bool {
	return slices.Contains(d.st.TopOrder(session), id)
}

// firstTop is the first top-level item, in order, that is one of ids, or
// "". ids are of one session. Callers hold d.mu.
func (d *Daemon) firstTop(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	for _, id := range d.st.TopOrder(d.st.SessionOf(ids[0])) {
		if slices.Contains(ids, id) {
			return id
		}
	}
	return ""
}

// setTabOptions pins or colors a tab. Pinning or unpinning moves it to
// the end of the pinned tabs at the top of its level, as a browser does:
// the session's top level for an ungrouped tab, its group for a grouped
// one. A worker stays under the tab that started it.
func (d *Daemon) setTabOptions(m proto.SetTabOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	moved := w.Pinned != m.Pinned && w.Parent == ""
	w.Pinned, w.Color = m.Pinned, m.Color
	if moved {
		d.placePinned(*w)
	}
	d.changed()
	return nil
}

// placePinned moves w before the first tab of its level, other than
// itself, that is not pinned. Callers hold d.mu.
func (d *Daemon) placePinned(w model.Workspace) {
	pinned := func(x *model.Workspace) bool { return x != nil && x.ID != w.ID && x.Pinned }
	if d.topLevel(w.SessionID, w.ID) {
		before := ""
		for _, id := range d.st.TopOrder(w.SessionID) {
			if id != w.ID && !pinned(d.workspace(id)) {
				before = id
				break
			}
		}
		d.st.PlaceTop(w.ID, before)
		return
	}
	rest := slices.DeleteFunc(slices.Clone(d.st.Workspaces), func(x model.Workspace) bool { return x.ID == w.ID })
	at := slices.IndexFunc(rest, func(x model.Workspace) bool {
		return x.ProjectID == w.ProjectID && x.Parent == "" && !pinned(&x)
	})
	if at < 0 {
		at = len(rest)
	}
	d.st.Workspaces = slices.Insert(rest, at, w)
}
