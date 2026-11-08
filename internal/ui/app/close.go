package app

import (
	"cmp"
	"strings"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/timelog"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// runningClose names only agents whose unfinished turns msg would stop.
func runningClose(st *model.State, msg any) (*model.Workspace, []string) {
	var ws, pane string
	switch m := msg.(type) {
	case proto.CloseTab:
		ws = m.WorkspaceID
	case proto.KillSession:
		ws = m.WorkspaceID
	case proto.ClosePane:
		if p := findPane(st, m.Pane); p != nil {
			ws, pane = p.WorkspaceID, p.ID
			if !st.IsPopup(p.ID) && st.PaneCount(ws) == 1 {
				pane = ""
			}
		}
	}
	w := st.Workspace(ws)
	if w == nil {
		return nil, nil
	}
	var agents []string
	if w.Chat != "" {
		for _, a := range st.Activities {
			if a.PaneID == w.Chat && timelog.Running(a.State) {
				agents = append(agents, sidebar.AgentName(a.Provider))
			}
		}
	}
	for _, ap := range st.AgentPanes(*w) {
		if (pane == "" || ap.Pane.ID == pane) && !ap.Pane.Exited && ap.Activity != nil && timelog.Running(ap.Activity.State) {
			agents = append(agents, cmp.Or(sidebar.AgentName(ap.Pane.Provider), sidebar.AgentName(ap.Activity.Provider)))
		}
	}
	return w, agents
}

func (u *ui) closeBody(gtx gl.Context, ws *model.Workspace) gl.Dimensions {
	th := u.th
	title := "Close \"" + tabTitle(*ws) + "\"?"
	if _, ok := u.modal.then.(proto.ClosePane); ok {
		title = "Close pane in \"" + tabTitle(*ws) + "\"?"
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, title)
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "Agents mid-turn: "+strings.Join(u.modal.agents, ", ")+". Closing stops them and their unfinished work.")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Close anyway", kit.Danger)
		}),
	)
}
