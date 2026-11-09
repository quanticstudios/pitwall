package app

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// pipView is picture in picture: nav.pip's screen drawn small and read
// only in a corner of the pane area, over the tabs that do not show it.
type pipView struct {
	view   term.View // its own, so the pane's view keeps its size and selection
	close  widget.Clickable
	corner pipCorner
	grab   int             // the card's pointer tag
	down   bool            // a press on the card is held
	from   f32.Point       // where it was, in pane area pixels
	drag   image.Point     // how far it has moved since
	card   image.Rectangle // where the card was last drawn, before drag
}

// pipCorner is the corner of the pane area the card sits in.
type pipCorner uint8

const (
	pipBottomRight pipCorner = iota
	pipBottomLeft
	pipTopRight
	pipTopLeft
)

// pipFont is the card's text size against the pane's.
// ponytail: a fixed scale crops a wide pane to its left; fit the scale to
// the pane's columns if that hides too much.
const pipFont = 0.6

// pipPlace is the rect of a card of size in corner c of area, margin in
// from its edges.
func pipPlace(area image.Rectangle, size image.Point, c pipCorner, margin int) image.Rectangle {
	x, y := area.Max.X-margin-size.X, area.Max.Y-margin-size.Y
	if c == pipBottomLeft || c == pipTopLeft {
		x = area.Min.X + margin
	}
	if c == pipTopRight || c == pipTopLeft {
		y = area.Min.Y + margin
	}
	return image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+size.X, y+size.Y)}
}

// pipNearest is the corner of area nearest to p, where a dropped card goes.
func pipNearest(area image.Rectangle, p image.Point) pipCorner {
	mid := area.Min.Add(area.Max).Div(2)
	switch left, top := p.X < mid.X, p.Y < mid.Y; {
	case left && top:
		return pipTopLeft
	case top:
		return pipTopRight
	case left:
		return pipBottomLeft
	}
	return pipBottomRight
}

// pipLastRow is the lower of the cursor's row and the last row with text
// on it: the card's rows end there, so a shell that printed ten lines on a
// fifty-row pane shows them, not the blanks below.
// Adapted from tuios (MIT): internal/app/pip.go
func pipLastRow(g *vt.Grid) int {
	for y := g.Rows - 1; y > g.Cursor.Y; y-- {
		for x := range g.Cols {
			if c := g.At(x, y).Content; c != "" && c != " " {
				return y
			}
		}
	}
	return max(g.Cursor.Y, 0)
}

// pipShift is how far up a screen full pixels tall moves in a window
// shown pixels tall so that its first want pixels, down to the cursor's
// row, show.
func pipShift(full, shown, want int) int {
	return max(0, min(want-shown, full-shown))
}

// drawPiP draws the picture in picture card over the pane area gtx fills,
// unless its pane is on screen already. A click on it goes to the pane's
// tab, a drag moves it to the nearest corner, its × closes it.
func (u *ui) drawPiP(gtx gl.Context, st *model.State) {
	id := u.nav.pip
	pn := findPane(st, id)
	if pn == nil {
		return
	}
	if p := u.panes[id]; p != nil && !p.rect.Empty() {
		return
	}
	ws := findWorkspace(st, pn.WorkspaceID)
	if ws == nil {
		return
	}
	pv := &u.pip
	if pv.close.Clicked(gtx) {
		u.nav.pip = ""
		return
	}
	area := image.Rectangle{Max: gtx.Constraints.Max}
	if u.pipPointer(gtx, st, pn, ws, area) {
		return
	}
	g, m, ok := u.b.Frame(id)
	if !ok {
		return
	}
	th := u.th

	// The screen at pipFont of the pane's size, cropped to its left and to
	// the rows down to pipLastRow when that is more than 2/5 of the area:
	// an agent's prompt is there.
	sgtx := gtx
	sgtx.Metric.PxPerSp *= pipFont
	sgtx.Metric.PxPerDp *= pipFont
	cell := pv.view.CellSize(sgtx, th)
	pad := sgtx.Dp(12) // term's padding
	full := image.Pt(g.Cols*cell.X+2*pad, g.Rows*cell.Y+2*pad)
	head := gtx.Dp(28)
	screen := image.Pt(min(full.X, area.Dx()*2/5), min(full.Y, area.Dy()*2/5-head))
	if screen.X < gtx.Dp(120) || screen.Y < gtx.Dp(40) {
		return // no room
	}
	card := pipPlace(area, image.Pt(screen.X, head+screen.Y), pv.corner, gtx.Dp(16))
	pv.card = card
	card = card.Add(pv.drag)
	r := gtx.Dp(theme.RadiusPopover)
	kit.Surface(gtx, card.Inset(-1), r+1, kit.Floating, th.BorderStrong, th.SurfaceSecondary)

	// The screen under the header, its bottom corners rounded.
	sr := image.Rectangle{Min: image.Pt(card.Min.X, card.Min.Y+head), Max: card.Max}
	sc := clip.RRect{Rect: sr, SE: r, SW: r}.Push(gtx.Ops)
	so := op.Offset(image.Pt(sr.Min.X, sr.Min.Y-pipShift(full.Y, screen.Y, 2*pad+(pipLastRow(&g)+1)*cell.Y))).Push(gtx.Ops)
	sgtx.Ops = gtx.Ops
	sgtx.Constraints = gl.Exact(full)
	pv.view.Layout(sgtx, th, &g, m, false)
	so.Pop()
	sc.Pop()
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(sr.Min.X, sr.Min.Y-1), Max: image.Pt(sr.Max.X, sr.Min.Y)}.Op())

	// Taken before the × is drawn, so its presses are the ×'s.
	cl := clip.UniformRRect(card, r).Push(gtx.Ops)
	pointer.CursorPointer.Add(gtx.Ops)
	event.Op(gtx.Ops, &pv.grab)
	cl.Pop()

	// The header: the marks, the tab's name and ×.
	px, is := gtx.Dp(10), gtx.Dp(14)
	x, _ := u.paneMarks(gtx, st, pn, image.Pt(card.Min.X+px, card.Min.Y), head, th.SurfaceSecondary)
	xr := card.Max.X - px - is
	tgtx := gtx
	tgtx.Constraints.Max.X = max(0, xr-gtx.Dp(8)-x)
	call, sz := textCall(tgtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Fg, tabTitle(*ws))
	o := op.Offset(image.Pt(x, card.Min.Y+(head-sz.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(xr, card.Min.Y+(head-is)/2)).Push(gtx.Ops)
	bgtx := gtx
	bgtx.Constraints = gl.Exact(image.Pt(is, is))
	pv.close.Layout(bgtx, func(gtx gl.Context) gl.Dimensions {
		c := th.Muted
		if pv.close.Hovered() {
			c = th.Fg
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return sidebar.Icon(gtx, "x", is, c)
	})
	o.Pop()
}

// pipPointer runs the card's presses and drags; true when a click went to
// the pane's tab, which then shows the pane in place of the card.
func (u *ui) pipPointer(gtx gl.Context, st *model.State, pn *model.Pane, ws *model.Workspace, area image.Rectangle) bool {
	pv := &u.pip
	clicked := false
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &pv.grab, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			if e.Buttons == pointer.ButtonPrimary {
				pv.down, pv.from, pv.drag = true, e.Position, image.Point{}
			}
		case pointer.Drag:
			if pv.down {
				pv.drag = e.Position.Sub(pv.from).Round()
			}
		case pointer.Release:
			if !pv.down {
				break
			}
			if d := pv.drag; max(d.X, -d.X, d.Y, -d.Y) < gtx.Dp(4) {
				clicked = true
			} else {
				c := pv.card.Add(pv.drag)
				pv.corner = pipNearest(area, c.Min.Add(c.Max).Div(2))
			}
			pv.down, pv.drag = false, image.Point{}
		case pointer.Cancel:
			pv.down, pv.drag = false, image.Point{}
		}
	}
	if !clicked {
		return false
	}
	u.settings.Hide()
	u.review.hide()
	u.leaveSidebar() // the keys go to the pane, as after a click on it
	if ws.Detached {
		u.send(proto.DetachSession{WorkspaceID: ws.ID, Detached: false})
		u.nav.attachSession(st, ws.ID)
	}
	u.nav.selectWorkspace(st, ws.ID, pn.ID)
	return true
}

// paneMarks draws pane pn's agent mark and its state glyph from at along
// a strip h tall on bg, in the 14dp and 12dp slots the sidebar uses, and
// returns the x after them and pn's activity, nil for none. A shell shows
// one of the two, as its sidebar row does.
func (u *ui) paneMarks(gtx gl.Context, st *model.State, pn *model.Pane, at image.Point, h int, bg color.NRGBA) (int, *model.Activity) {
	th := u.th
	mark, gs := gtx.Dp(14), gtx.Dp(12)
	var act *model.Activity
	for i := range st.Activities {
		if st.Activities[i].PaneID == pn.ID {
			act = &st.Activities[i]
			break
		}
	}
	x := at.X
	if model.IsAgent(pn.Provider) || act == nil {
		x += drawIconAt(gtx, image.Pt(x, at.Y+(h-mark)/2), func(gtx gl.Context) {
			if model.IsAgent(pn.Provider) {
				sidebar.AgentMark(gtx, pn.Provider, mark, th.Fg)
			} else {
				sidebar.Icon(gtx, "terminal", mark, th.Muted)
			}
		}) + gtx.Dp(6)
	}
	if act != nil {
		drawIconAt(gtx, image.Pt(x, at.Y+(h-gs)/2), func(gtx gl.Context) {
			sidebar.StateGlyph(gtx, th, act.State, bg)
		})
		x += gs + gtx.Dp(8)
	}
	return x, act
}
