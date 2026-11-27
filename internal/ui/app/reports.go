package app

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// openReport shows the tab of worker report r, as a click on its row
// would; false when that tab closed.
func (u *ui) openReport(st *model.State, r model.Report) bool {
	if !slices.ContainsFunc(st.Workspaces, func(w model.Workspace) bool { return w.ID == r.Tab }) {
		return false
	}
	u.sidebarEvent(st, sidebar.SelectWorkspace{WorkspaceID: r.Tab, PaneID: r.Pane})
	return true
}
