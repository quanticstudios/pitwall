package app

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
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
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type paneMenu struct {
	pane               string
	at                 image.Point
	outside            int
	edit               widget.Clickable
	requested, focused bool
}

func (m *paneMenu) close() { m.pane, m.requested, m.focused = "", false, false }

func (u *ui) layoutPaneMenu(gtx gl.Context, st *model.State) {
	m := &u.paneMenu
	if m.pane == "" {
		return
	}
	if p := findPane(st, m.pane); p == nil || p.WorkspaceID != u.nav.workspace {
		m.close()
		return
	}
	if !m.requested {
		gtx.Execute(key.FocusCmd{Tag: m})
		m.requested = true
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.outside, Kinds: pointer.Press}, key.FocusFilter{Target: m}, key.Filter{Focus: m, Name: key.NameEscape}, key.Filter{Focus: m, Name: key.NameReturn})
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			if m.focused && !e.Focus {
				m.close()
				return
			}
			m.focused = e.Focus
			continue
		case key.Event:
			if e.State != key.Press {
				continue
			}
			if e.Name == key.NameReturn {
				u.editScrollback(st, m.pane)
			}
		}
		m.close()
		return
	}
	if m.pane == "" {
		return
	}
	if m.edit.Clicked(gtx) {
		u.editScrollback(st, m.pane)
		m.close()
		return
	}
	// The full-area catcher keeps presses outside the menu out of the pane.
	c := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.outside)
	c.Pop()
	p, h := gtx.Dp(4), gtx.Dp(32)
	size := image.Pt(gtx.Dp(288), h+2*p)
	at := kit.Place(image.Rectangle{Min: m.at, Max: m.at}, size, image.Rectangle{Max: gtx.Constraints.Max}, kit.BelowStart, 0, gtx.Dp(8))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	r := gtx.Dp(theme.RadiusPopover)
	rect := image.Rectangle{Max: size}
	kit.Surface(gtx, rect.Inset(-1), r+1, kit.Floating, u.th.BorderStrong, u.th.SurfaceSecondary)
	c = clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, m)
	c.Pop()
	defer op.Offset(image.Pt(p, p)).Push(gtx.Ops).Pop()
	row := image.Pt(size.X-2*p, h)
	gtx.Constraints = gl.Exact(row)
	m.edit.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		if m.edit.Hovered() {
			paint.FillShape(gtx.Ops, u.th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: row}, gtx.Dp(8)).Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		x := gtx.Dp(8)
		x += drawIconAt(gtx, image.Pt(x, (h-gtx.Dp(14))/2), func(gtx gl.Context) { sidebar.Icon(gtx, "pencil", gtx.Dp(14), u.th.Muted) }) + gtx.Dp(8)
		call, sz := textCall(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Fg, "Open scrollback in editor")
		off := op.Offset(image.Pt(x, (h-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		return gl.Dimensions{Size: row}
	})
}

func (u *ui) editScrollback(st *model.State, pane string) {
	u.nav.setFocus(pane)
	u.nav.expectPane(st)
	u.send(proto.EditScrollback{Pane: pane})
}
