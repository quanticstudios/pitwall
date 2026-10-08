package app

import (
	"fmt"
	"image"

	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// paletteDraw is the palette's tags and a clickable per row.
type paletteDraw struct {
	backdrop, card int
	rows           []widget.Clickable
}

// drawPalette draws the command palette over a dimmed window, as the
// session switcher is drawn: a filter, then every action that matches it
// with its group and keys, the keys the palette takes along the bottom.
// A click on a row runs it.
func (u *ui) drawPalette(gtx gl.Context, st *model.State) {
	p, d, th := &u.pal, &u.pal.draw, u.th
	if !p.open {
		return
	}
	b := u.nav.bind()
	all := paletteEntries(b)
	rows := rank(all, p.query)
	for i := range min(len(rows), len(d.rows)) {
		if d.rows[i].Clicked(gtx) {
			p.close()
			u.runAction(st, rows[i].action)
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	p.sel = min(p.sel, max(len(rows)-1, 0))
	t := easeOut(float32(gtx.Now.Sub(p.openedAt)) / float32(switcherOpen))
	if t < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
	if backdrop(gtx, t, &d.backdrop) {
		p.close()
		return
	}
	size := gtx.Constraints.Max
	w := min(gtx.Dp(600), size.X-gtx.Dp(48))
	h := min(gtx.Dp(520), size.Y-gtx.Dp(64))
	defer u.overlayCard(gtx, t, w, h, &d.card)()
	pad, footH := gtx.Dp(16), gtx.Dp(44)
	inner := w - 2*pad

	// Header: the title and the count, the palette's key on the right.
	y := pad
	tc, tsz := textCall(gtx, th, semibold(th.UIFont), 15, th.Fg, "Commands")
	o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	cc, _ := textCall(gtx, th, th.UIFont, 13, th.Muted, fmt.Sprint(len(all)))
	o = op.Offset(image.Pt(pad+tsz.X+gtx.Dp(8), y+gtx.Dp(2))).Push(gtx.Ops)
	cc.Add(gtx.Ops)
	o.Pop()
	if k := firstChord(b.Global["command_palette"]); k != "" {
		kc, ks := keycap(gtx, th, k)
		o := op.Offset(image.Pt(pad+inner-ks.X, y)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
	}
	y += tsz.Y + gtx.Dp(12)

	fieldH := gtx.Dp(34)
	u.filterField(gtx, image.Rect(pad, y, pad+inner, y+fieldH), p.query, "Type an action, a group or a key",
		fmt.Sprintf("%d of %d", len(rows), len(all)), true, p.openedAt)
	y += fieldH + gtx.Dp(10)

	listTop, listBot := y, h-footH-gtx.Dp(8)
	rowH, gap := gtx.Dp(34), gtx.Dp(2)
	sel := p.sel
	if len(rows) == 0 {
		sel = -1
	}
	hy := p.list.update(gtx, sel, len(rows), rowH, gap, listTop, listBot-listTop)
	hl := image.Rect(pad, hy, pad+inner, hy+rowH)
	lc := clip.Rect{Min: image.Pt(0, listTop), Max: image.Pt(w, listBot)}.Push(gtx.Ops)
	if sel >= 0 {
		u.highlight(gtx, hl, false)
	}
	for len(d.rows) < len(rows) {
		d.rows = append(d.rows, widget.Clickable{})
	}
	for i, e := range rows {
		ry := listTop + i*(rowH+gap) - p.list.scroll
		if ry+rowH < listTop || ry > listBot {
			continue
		}
		o := op.Offset(image.Pt(pad, ry)).Push(gtx.Ops)
		u.paletteRow(gtx, &d.rows[i], e, i == sel, image.Pt(inner, rowH))
		o.Pop()
	}
	if sel >= 0 {
		u.highlight(gtx, hl, true)
	}
	if len(rows) == 0 {
		drawText(gtx, th, image.Pt(pad+gtx.Dp(12), listTop+gtx.Dp(12)), th.UIFont, 13, th.Muted, "No action matches \""+p.query+"\".")
	}
	lc.Pop()

	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.08), clip.Rect{Min: image.Pt(1, h-footH), Max: image.Pt(w-1, h-footH+1)}.Op())
	o = op.Offset(image.Pt(pad, h-footH)).Push(gtx.Ops)
	u.drawHints(gtx, footH, [][2]string{{"↑↓", "move"}, {"Enter", "run"}, {"Esc", "close"}})
	o.Pop()
}

// paletteRow draws one action: its title, its group, then its keys on the
// right, at most two.
func (u *ui) paletteRow(gtx gl.Context, c *widget.Clickable, e paletteEntry, sel bool, size image.Point) {
	th := u.th
	g := gtx
	g.Constraints = gl.Exact(size)
	c.Layout(g, func(gtx gl.Context) gl.Dimensions {
		if c.Hovered() && !sel {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.035), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
	px := gtx.Dp(12)
	kx := size.X - px
	keys := e.keys[:min(len(e.keys), 2)]
	for i := len(keys) - 1; i >= 0; i-- {
		kc, ks := keycap(gtx, th, keys[i])
		kx -= ks.X
		o := op.Offset(image.Pt(kx, (size.Y-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		kx -= gtx.Dp(6)
	}
	gc, gsz := textCall(gtx, th, th.UIFont, 12, th.Muted, e.action.Group)
	tg := gtx
	tg.Constraints.Max.X = max(0, kx-px-gtx.Dp(12)-gsz.X-gtx.Dp(10))
	tc, tsz := textCall(tg, th, medium(th.UIFont), 13, th.Fg, e.action.Title())
	o := op.Offset(image.Pt(px, (size.Y-tsz.Y)/2)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(px+tsz.X+gtx.Dp(10), (size.Y-gsz.Y)/2)).Push(gtx.Ops)
	gc.Add(gtx.Ops)
	o.Pop()
}
