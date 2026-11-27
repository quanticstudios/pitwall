package app

import (
	"image"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// workRailMin is the narrowest chat that keeps room for the work rail.
const workRailMin = 640

// layoutWorkRail draws the work rail at the right of chat id when its tab
// ws started workers and the chat is wide enough, opens a worker's tab
// on a click, and returns gtx narrowed to the room left for the chat.
func (u *ui) layoutWorkRail(gtx gl.Context, st *model.State, id string, ws *model.Workspace) gl.Context {
	if u.workRails == nil {
		u.workRails = map[string]*panel.WorkRail{}
	}
	r := u.workRails[id]
	if r == nil {
		r = new(panel.WorkRail)
		u.workRails[id] = r
	}
	var host func(model.Workspace) string
	if h, ok := u.b.(Hoster); ok {
		at := func(w model.Workspace) string {
			if s := h.HostOf(w.ID); s != "" {
				return s
			}
			return h.SessionHost(w.SessionID)
		}
		own := at(*ws)
		host = func(w model.Workspace) string {
			if s := at(w); s != own {
				return s
			}
			return ""
		}
	}
	workers := st.WorkRail(ws.ID, host)
	w := gtx.Dp(panel.WorkRailWidth)
	if !r.Shown(workers) || gtx.Constraints.Max.X < gtx.Dp(workRailMin) {
		return gtx
	}
	rg := gtx
	rg.Constraints = gl.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	t := op.Offset(image.Pt(gtx.Constraints.Max.X-w, 0)).Push(gtx.Ops)
	open := r.Layout(rg, u.th, workers, gtx.Now)
	t.Pop()
	if open != "" {
		u.nav.selectWorkspace(st, open, "")
	}
	gtx.Constraints.Max.X -= w
	gtx.Constraints.Min.X = min(gtx.Constraints.Min.X, gtx.Constraints.Max.X)
	return gtx
}
