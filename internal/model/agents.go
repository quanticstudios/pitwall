package model

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/layout"
)

// AgentPane is one agent pane of a tab and its activity, nil when the
// pane has none.
type AgentPane struct {
	Pane     Pane
	Activity *Activity
}

// AgentPanes lists tab ws's agent panes, the ones whose pane or activity
// names one of Agents, in the order its layout shows them; panes the
// layout lacks follow in state order. Shells and panes running a terminal
// command are left out.
func (s *State) AgentPanes(ws Workspace) []AgentPane {
	var order []string
	for _, t := range ws.Tabs {
		order = append(order, layout.Panes(t.Layout)...)
	}
	for _, p := range s.Panes {
		if p.WorkspaceID == ws.ID && !slices.Contains(order, p.ID) {
			order = append(order, p.ID)
		}
	}
	var out []AgentPane
	for _, id := range order {
		i := slices.IndexFunc(s.Panes, func(p Pane) bool { return p.ID == id && p.WorkspaceID == ws.ID })
		if i < 0 {
			continue
		}
		ap := AgentPane{Pane: s.Panes[i]}
		if j := slices.IndexFunc(s.Activities, func(a Activity) bool { return a.PaneID == id }); j >= 0 {
			a := s.Activities[j]
			ap.Activity = &a
		}
		if IsAgent(ap.Pane.Provider) || ap.Activity != nil && IsAgent(ap.Activity.Provider) {
			out = append(out, ap)
		}
	}
	return out
}
