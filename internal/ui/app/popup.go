package app

import (
	"cmp"
	"image"
	"image/color"
	"path/filepath"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// popupRect is where a popup of w and h goes in area, centred: each side
// a percentage of area or a number of cells of cell, plus extra for the
// frame and the terminal's padding, and never past area.
func popupRect(pp model.Popup, area layout.Rect, cell image.Point, extra int) layout.Rect {
	side := func(spec, fallback string, extent, cell, floor int) int {
		cell = max(cell, 1)
		n := model.ResolvePopupSize(spec, fallback, max((extent-extra)/cell, 1), floor)
		return min(n*cell+extra, extent)
	}
	w := side(pp.Width, model.PopupWidth, area.W, cell.X, model.PopupMinWidth)
	h := side(pp.Height, model.PopupHeight, area.H, cell.Y, model.PopupMinHeight)
	return layout.Rect{X: area.X + (area.W-w)/2, Y: area.Y + (area.H-h)/2, W: w, H: h}
}

// popupTitle is what a popup's border shows: its title, else its
// command's name, else shell.
func popupTitle(pp model.Popup, pn *model.Pane) string {
	if pp.Title != "" {
		return pp.Title
	}
	if pn != nil && len(pn.Cmd) > 0 {
		return filepath.Base(pn.Cmd[0])
	}
	return "shell"
}

// layoutPopup draws the shown tab's popup over area: a backdrop that dims
// the splits and takes the clicks meant for them, then the popup's pane
// in a pane frame with a shadow, its title and the key that closes it on
// the top border. The caller marks the pane drawn.
func (u *ui) layoutPopup(gtx gl.Context, st *model.State, pp model.Popup, area layout.Rect, focused, findFocus bool) {
	th := u.th
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &u.popupBack, Kinds: pointer.Press}); !ok {
			break
		}
	}
	dim := th.Scrim
	dim.A /= 3
	back := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	paint.ColorOp{Color: dim}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	event.Op(gtx.Ops, &u.popupBack)
	back.Pop()

	p := u.paneView(st, pp.Pane)
	extra := 2*gtx.Dp(12) + 2 // the terminal's padding and the frame's border
	r := popupRect(pp, area, p.view.CellSize(gtx, th), extra)
	rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	kit.Shadow(gtx, rect, gtx.Dp(10), kit.Modal)
	u.layoutPane(gtx, p, pp.Pane, r, focused, findFocus, false, nil)

	// The title and the close key cut into the top border.
	pad := gtx.Dp(6)
	label := func(s string, c color.NRGBA) (op.CallOp, image.Rectangle) {
		call, size := textCall(gtx, th, th.UIFont, th.Sp(theme.Caption), c, s)
		return call, image.Rectangle{Max: size.Add(image.Pt(2*pad, 0))}.Add(image.Pt(0, rect.Min.Y-size.Y/2))
	}
	draw := func(call op.CallOp, box image.Rectangle) {
		paint.FillShape(gtx.Ops, th.TermBg, clip.Rect(box).Op())
		o := op.Offset(box.Min.Add(image.Pt(pad, 0))).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
	}
	call, title := label(popupTitle(pp, findPane(st, pp.Pane)), th.Fg)
	title = title.Add(image.Pt(rect.Min.X+gtx.Dp(14), 0))
	draw(call, title)
	if k := firstChord(u.nav.bind().Global["close_pane"]); k != "" {
		call, hint := label(k+" closes", th.Muted)
		hint = hint.Add(image.Pt(rect.Max.X-gtx.Dp(14)-hint.Dx(), 0))
		if hint.Min.X > title.Max.X+pad {
			draw(call, hint)
		}
	}
}

// popupKey opens the [[popups]] entry whose key e is, over the shown tab,
// and reports whether e was one. A tab with a popup open already ignores
// it: its key reaches the window, not the popup's program.
func (u *ui) popupKey(st *model.State, e key.Event) bool {
	pp, ok := u.cfg.PopupFor(e)
	if !ok {
		return false
	}
	if e.State != key.Press {
		return true
	}
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil || ws.Chat != "" || u.nav.popup() != "" || shownTab(ws) == nil {
		return true
	}
	u.send(openPopup(ws.ID, pp))
	return true
}

// openPopup is the request that opens pp over tab ws.
func openPopup(ws string, pp config.Popup) proto.OpenPopup {
	return proto.OpenPopup{WorkspaceID: ws, Cmd: pp.Command, Width: pp.Width, Height: pp.Height, Title: cmp.Or(pp.Title, pp.Name)}
}

// popupFilters are the [[popups]] keys, which the window takes before
// any pane sees them.
func (u *ui) popupFilters() []key.Filter {
	var fs []key.Filter
	for _, pp := range u.cfg.Popups {
		fs = append(fs, key.Filter{Name: pp.Chord.Name, Required: pp.Chord.Mods})
	}
	return fs
}
