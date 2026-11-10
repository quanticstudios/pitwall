package phone

import (
	"slices"
	"time"

	pwlayout "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// State is the model.State the sidebar draws for tabs, the list the
// stream sends in the sidebar's order: a session for each session name, a
// group for each group name in it, and each tab with its one pane and
// that pane's activity. Session and group ids come from their names, so
// a group stays open across lists.
//
// why: the list already holds what a row shows, redacted as the page
// redacts it, so the app needs no endpoint of its own.
func State(tabs []remote.Tab) model.State {
	var st model.State
	for _, t := range tabs {
		sid := "s:" + t.Session
		s := st.Session(sid)
		if s == nil {
			st.Sessions = append(st.Sessions, model.Session{ID: sid, Name: t.Session})
			s = &st.Sessions[len(st.Sessions)-1]
		}
		top := t.ID
		gid := ""
		if t.Group != "" {
			gid = sid + "/g:" + t.Group
			top = gid
			if !slices.ContainsFunc(st.Projects, func(p model.Project) bool { return p.ID == gid }) {
				st.Projects = append(st.Projects, model.Project{ID: gid, SessionID: sid, Name: t.Group, Kind: model.ProjectGroup})
			}
		}
		if !slices.Contains(s.Order, top) {
			s.Order = append(s.Order, top)
		}
		var at time.Time
		if t.At != 0 {
			at = time.Unix(0, t.At)
		}
		prov := model.Provider(t.Agent)
		st.Workspaces = append(st.Workspaces, model.Workspace{
			ID: t.ID, SessionID: sid, ProjectID: gid, Name: t.Tab, Label: t.Tab, Branch: t.Branch, UpdatedAt: at,
			Tabs: []model.Tab{{Layout: &pwlayout.Node{Pane: t.Pane}}},
		})
		st.Panes = append(st.Panes, model.Pane{ID: t.Pane, WorkspaceID: t.ID, Provider: prov})
		if t.State != "" {
			st.Activities = append(st.Activities, model.Activity{PaneID: t.Pane, WorkspaceID: t.ID, Provider: prov,
				State: model.AgentState(t.State), Detail: t.Detail, UpdatedAt: at})
		}
	}
	return st
}

// NeedsYou counts the tabs of session (a State session id) that wait on
// the user.
func NeedsYou(st *model.State, session string) int {
	n := 0
	for _, a := range st.Activities {
		if i := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.ID == a.WorkspaceID }); i >= 0 &&
			st.Workspaces[i].SessionID == session && model.NeedsYou(a.State) {
			n++
		}
	}
	return n
}
