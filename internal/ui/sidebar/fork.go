package sidebar

import (
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
)

// forkPane picks the focused agent, else the first agent in layout order.
func (s *Sidebar) forkPane(st *model.State, ws model.Workspace) string {
	panes := st.AgentPanes(ws)
	for _, ap := range panes {
		if ap.Pane.ID == s.Pane {
			panes = []model.AgentPane{ap}
			break
		}
	}
	if len(panes) > 0 {
		if _, err := store.ForkCmd(panes[0].Pane); err == nil {
			return panes[0].Pane.ID
		}
	}
	return ""
}
