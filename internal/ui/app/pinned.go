package app

import (
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pinnedClose is the tab msg would close when that tab is pinned: a
// CloseTab, a KillSession, or a ClosePane of its last pane. The daemon
// refuses each of them, so the window asks to unpin first.
func pinnedClose(st *model.State, msg any) *model.Workspace {
	var id string
	switch m := msg.(type) {
	case proto.CloseTab:
		id = m.WorkspaceID
	case proto.KillSession:
		id = m.WorkspaceID
	case proto.ClosePane:
		if p := findPane(st, m.Pane); p != nil && st.PaneCount(p.WorkspaceID) == 1 {
			id = p.WorkspaceID
		}
	}
	if w := st.Workspace(id); w != nil && w.Pinned {
		return w
	}
	return nil
}

// sendOrAsk sends msg, or opens the pinned dialog when msg would close a
// pinned tab; its Unpin and close sends msg after the unpin.
func (u *ui) sendOrAsk(st *model.State, msg any) {
	if w := pinnedClose(st, msg); w != nil {
		u.modal.open(modalPinned, w.ID)
		u.modal.then = msg
		return
	}
	u.send(msg)
}

func (u *ui) confirmPinned(st *model.State) {
	m := &u.modal
	if w := st.Workspace(m.ws); w != nil {
		u.send(proto.SetTabOptions{WorkspaceID: w.ID, Color: w.Color})
		u.send(m.then)
	}
	m.close()
}

func (u *ui) pinnedBody(gtx gl.Context, ws *model.Workspace) gl.Dimensions {
	th := u.th
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "\""+tabTitle(*ws)+"\" is pinned")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "Unpin it to close it. Closing stops its terminals and agents.")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Cancel", "Unpin and close", kit.Danger)
		}),
	)
}
