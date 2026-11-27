package app

import (
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// chatInset draws chat id's live inset of its agent's screen over the
// transcript, right-aligned in the composer's column, while the agent works and has a screen (a
// pane chat's); open shows that screen.
func (u *ui) chatInset(gtx gl.Context, st *model.State, id string, env chatin.Env, open func()) {
	c := findChat(st, id)
	if c == nil {
		return
	}
	if u.insets == nil {
		u.insets = map[string]*panel.Inset{}
	}
	in := u.insets[id]
	if in == nil {
		in = new(panel.Inset)
		u.insets[id] = in
	}
	frame := func() (vt.Grid, bool) {
		g, _, ok := u.b.Frame(id)
		return g, ok
	}
	if in.Layout(gtx, u.th, sidebar.AgentName(c.Provider), c.State == session.StateRunning, panel.InsetColumn(gtx, env), frame) {
		open()
	}
}
