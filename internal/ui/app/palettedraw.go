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

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
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
	u.updateSearchAll()
	b := u.nav.bind()
	all := paletteRows(b, "", nil)
	rows := paletteRows(b, p.query, u.gui.Recent)
	title, hint := "Commands", "Type an action, a group or a key"
	count := fmt.Sprintf("%d of %d", len(rows), len(all))
	if p.search {
		title, hint = "Search all sessions", "Type to search every session"
		rows = make([]paletteEntry, len(u.all.matches))
		if u.all.preview != nil {
			title = "Transcript match"
			if u.all.preview.Hidden {
				title = "Scrollback match"
			}
			rows = make([]paletteEntry, 1)
		}
		count = u.searchStatus()
	}
	for i := range min(len(rows), len(d.rows)) {
		if d.rows[i].Clicked(gtx) {
			if p.search && u.all.preview == nil {
				u.pickSearchAll(st, u.all.matches[i])
			} else if !p.search {
				if r := p.pick(rows, i); r != nil {
					u.runEntry(st, *r)
				}
			}
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	p.sel = min(p.sel, max(len(rows)-1, 0))
	t := anim.At(gtx, p.openedAt, anim.Overlay)
	if u.backdrop(gtx, t, &d.backdrop) {
		p.close()
		u.stopSearchAll()
		return
	}
	size := gtx.Constraints.Max
	w := min(gtx.Dp(600), size.X-gtx.Dp(48))
	pad, footH := gtx.Dp(16), gtx.Dp(44)
	inner := w - 2*pad
	tc, tsz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, title)
	fieldH, rowH, gap := gtx.Dp(34), gtx.Dp(34), gtx.Dp(2)
	if p.search {
		fieldH, rowH = gtx.Dp(56), gtx.Dp(72)
		if u.all.preview != nil {
			rowH = gtx.Dp(144)
		}
	}
	// The card fits its rows, up to 420dp, and eases to a new height; its
	// top stays where the tallest card's would be, so the field holds still.
	most := min(gtx.Dp(420), size.Y-gtx.Dp(64))
	chrome := pad + tsz.Y + gtx.Dp(12) + fieldH + gtx.Dp(10) - gap + gtx.Dp(8) + footH
	p.height.Set(gtx.Now, float32(paletteHeight(len(rows), rowH+gap, chrome, most)), anim.Menu)
	h := int(p.height.Get(gtx) + 0.5)
	defer u.overlayCardAt(gtx, t, w, h, (size.Y-most)/2, &d.card)()

	// Header: the title and the count, the palette's key on the right.
	y := pad
	o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	label := fmt.Sprint(len(all))
	if p.search {
		label = ""
	}
	cc, _ := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, label)
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

	if p.search {
		u.filterField(gtx, image.Rect(pad, y, pad+inner, y+gtx.Dp(34)), p.query, hint, "", u.all.preview == nil, p.openedAt)
		drawText(gtx, th, image.Pt(pad+gtx.Dp(8), y+gtx.Dp(38)), th.UIFont, th.Sp(theme.Small), th.Muted, count)
	} else {
		u.filterField(gtx, image.Rect(pad, y, pad+inner, y+fieldH), p.query, hint, count, true, p.openedAt)
	}
	y += fieldH + gtx.Dp(10)

	listTop, listBot := y, h-footH-gtx.Dp(8)
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
		if p.search {
			m := u.all.preview
			first := true
			if m == nil {
				m = &u.all.matches[i]
				first = i == 0 || u.all.matches[i-1].Workspace != m.Workspace
			}
			u.searchRow(gtx, st, &d.rows[i], *m, first, image.Pt(inner, rowH))
		} else {
			u.paletteRow(gtx, &d.rows[i], e, p.query, u.nav.reviewBlocked(st, e.action.Name), i == sel, image.Pt(inner, rowH))
		}
		o.Pop()
	}
	if sel >= 0 {
		u.highlight(gtx, hl, true)
	}
	if len(rows) == 0 {
		msg := "Nothing matches \"" + p.query + "\". Try part of a name, a group like Panes, or a key like Ctrl+T."
		if p.search {
			msg = "No matches"
			if p.query == "" || !u.all.done {
				msg = ""
			}
		} else if c, ok := config.KeyQuery(p.query); ok {
			msg = "Nothing uses " + c.String() + "."
		}
		drawText(gtx, th, image.Pt(pad+gtx.Dp(12), listTop+(rowH-gtx.Dp(18))/2), th.UIFont, th.Sp(theme.Body), th.Muted, msg)
	}
	lc.Pop()

	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.08), clip.Rect{Min: image.Pt(1, h-footH), Max: image.Pt(w-1, h-footH+1)}.Op())
	o = op.Offset(image.Pt(pad, h-footH)).Push(gtx.Ops)
	verb := "run"
	if p.search {
		verb = "open match"
		if u.all.preview != nil {
			verb = "close"
		}
	}
	u.drawHints(gtx, footH, [][2]string{{"↑↓", "move"}, {"Enter", verb}, {"Esc", "close"}})
	o.Pop()
}

// paletteRow draws one action: its title, its group, then its keys on the
// right, at most two. The title's characters query matched are semibold
// and the rest muted. An action that cannot run now, why not "", is muted,
// with why in place of its group.
func (u *ui) paletteRow(gtx gl.Context, c *widget.Clickable, e paletteEntry, query, why string, sel bool, size image.Point) {
	th := u.th
	if _, ok := config.KeyQuery(query); ok {
		query = "" // keys match no letters of the title
	}
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
	group, fg := e.action.Group, th.Fg
	if why != "" {
		group, fg = why, th.Muted
	}
	gc, gsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, group)
	room := max(0, kx-px-gtx.Dp(12)-gsz.X-gtx.Dp(10))
	x := px
	for _, s := range hitSpans(e.action.Title(), query) {
		f, col := medium(th.UIFont), fg
		switch {
		case s.hit:
			f = semibold(th.UIFont)
		case query != "":
			col = th.Muted
		}
		tg := gtx
		tg.Constraints.Max.X = room - (x - px)
		if tg.Constraints.Max.X <= 0 {
			break
		}
		tc, tsz := textCall(tg, th, f, th.Sp(theme.Body), col, s.s)
		o := op.Offset(image.Pt(x, (size.Y-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X
	}
	o := op.Offset(image.Pt(x+gtx.Dp(10), (size.Y-gsz.Y)/2)).Push(gtx.Ops)
	gc.Add(gtx.Ops)
	o.Pop()
}
