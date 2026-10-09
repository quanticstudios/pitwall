package app

import (
	"fmt"
	"image"
	"image/color"
	"strings"

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
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The session switcher opens over anim.Overlay; the highlight eases between
// rows with a 60ms time constant, and the preview fades in over anim.Slide
// when the highlight lands on another session.

// switcherDraw is the switcher's widgets: tags, clickables and one preview
// sidebar per session, so each keeps its own expanded groups.
type switcherDraw struct {
	backdrop, card, block int
	rows                  map[string]*switcherRow
	newBtn, keep, kill    widget.Clickable
	hover                 string // the row the pointer is over
	previews              map[string]*sidebar.Sidebar
}

type switcherRow struct{ row, rename, kill widget.Clickable }

func (d *switcherDraw) row(id string) *switcherRow {
	if d.rows == nil {
		d.rows = map[string]*switcherRow{}
	}
	r := d.rows[id]
	if r == nil {
		r = &switcherRow{}
		d.rows[id] = r
	}
	return r
}

// sessionClicks applies the clicks from the last frame.
func (u *ui) sessionClicks(gtx gl.Context, st *model.State) {
	s, d := &u.sw, &u.sw.draw
	for id, r := range d.rows {
		if st.Session(id) == nil {
			delete(d.rows, id)
			continue
		}
		for r.row.Clicked(gtx) {
			if s.mode == modePick {
				s.close()
				u.nav.switchSession(st, id)
			}
		}
		for r.rename.Clicked(gtx) {
			s.setSel(id, gtx.Now)
			s.startRename(st)
		}
		for r.kill.Clicked(gtx) {
			s.setSel(id, gtx.Now)
			s.mode = modeKill
		}
		if r.row.Hovered() || r.rename.Hovered() || r.kill.Hovered() {
			if d.hover != id && s.mode == modePick {
				s.setSel(id, gtx.Now)
			}
			d.hover = id
		} else if d.hover == id {
			d.hover = ""
		}
	}
	for d.newBtn.Clicked(gtx) {
		s.startNew(st)
	}
	for d.keep.Clicked(gtx) {
		s.mode = modePick
	}
	for d.kill.Clicked(gtx) {
		if s.mode == modeKill {
			u.send(proto.SessionKill{SessionID: s.sel})
			s.mode = modePick
		}
	}
	for id := range d.previews {
		if st.Session(id) == nil {
			delete(d.previews, id)
		}
	}
}

// drawSessions draws the session switcher over a dimmed window: the list
// of sessions on the left, a live drawing of the highlighted one's sidebar
// on the right, the keys along the bottom.
func (u *ui) drawSessions(gtx gl.Context, st *model.State) {
	s, d, th := &u.sw, &u.sw.draw, u.th
	u.sessionClicks(gtx, st)
	if !s.open {
		return
	}
	s.fix(st, gtx.Now)
	size := gtx.Constraints.Max
	t := anim.At(gtx, s.openedAt, anim.Overlay)

	if u.backdrop(gtx, t, &d.backdrop) {
		s.close()
		return
	}

	// The preview is the sidebar at full size when it fits, so it reads as
	// the window it stands for; narrow windows scale it down, then drop it.
	pad := gtx.Dp(16)
	previewW := gtx.Dp(sidebar.Width) + 2
	w := min(gtx.Dp(400)+previewW+3*pad, size.X-gtx.Dp(48))
	h := min(gtx.Dp(560), size.Y-gtx.Dp(64))
	listW := min(gtx.Dp(400), w-2*pad)
	if previewW = w - listW - 3*pad; previewW < gtx.Dp(180) {
		listW, previewW = w-2*pad, 0
	}
	defer u.overlayCard(gtx, t, w, h, &d.card)()

	footH := gtx.Dp(44)
	// Header: the title and the count, the switcher's key on the right.
	y := pad
	title := "Sessions"
	tc, tsz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, title)
	o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	cc, _ := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, fmt.Sprint(len(st.Sessions)))
	o = op.Offset(image.Pt(pad+tsz.X+gtx.Dp(8), y+gtx.Dp(2))).Push(gtx.Ops)
	cc.Add(gtx.Ops)
	o.Pop()
	if k := firstChord(u.nav.bind().Global["session_switcher"]); k != "" {
		kc, ks := keycap(gtx, th, k)
		o := op.Offset(image.Pt(pad+listW-ks.X, y)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
	}
	y += tsz.Y + gtx.Dp(12)

	// The filter field.
	fieldH := gtx.Dp(34)
	active := s.filtering && s.mode == modePick
	u.filterField(gtx, image.Rect(pad, y, pad+listW, y+fieldH), s.filter, "Type to filter",
		fmt.Sprintf("%d of %d", len(s.rows(st)), len(st.Sessions)), active, s.openedAt)
	y += fieldH + gtx.Dp(10)

	// The list, scrolled so the highlight shows.
	newH := 0
	if s.mode != modeNew {
		newH = gtx.Dp(34) + gtx.Dp(8)
	}
	listTop, listBot := y, h-footH-pad-newH
	rows := s.rows(st)
	rowH, gap := gtx.Dp(56), gtx.Dp(4)
	n := len(rows)
	if s.mode == modeNew {
		n++
	}
	selI := -1
	for i, x := range rows {
		if x.ID == s.sel {
			selI = i
		}
	}
	if s.mode == modeNew {
		selI = len(rows)
	}
	hy := s.list.update(gtx, selI, n, rowH, gap, listTop, listBot-listTop)
	hl := image.Rect(pad, hy, pad+listW, hy+rowH)
	lc := clip.Rect{Min: image.Pt(0, listTop), Max: image.Pt(w, listBot)}.Push(gtx.Ops)
	if selI >= 0 {
		u.highlight(gtx, hl, false)
	}
	for i, x := range rows {
		ry := listTop + i*(rowH+gap) - s.list.scroll
		if ry+rowH < listTop || ry > listBot {
			continue
		}
		o := op.Offset(image.Pt(pad, ry)).Push(gtx.Ops)
		u.sessionRow(gtx, st, x, i, image.Pt(listW, rowH))
		o.Pop()
	}
	if s.mode == modeNew {
		ry := listTop + len(rows)*(rowH+gap) - s.list.scroll
		o := op.Offset(image.Pt(pad, ry)).Push(gtx.Ops)
		u.newRow(gtx, image.Pt(listW, rowH))
		o.Pop()
	}
	if selI >= 0 {
		u.highlight(gtx, hl, true)
	}
	if len(rows) == 0 && s.mode != modeNew {
		drawText(gtx, th, image.Pt(pad+gtx.Dp(12), listTop+gtx.Dp(12)), th.UIFont, th.Sp(theme.Body), th.Muted, "No session matches \""+s.filter+"\".")
	}
	lc.Pop()

	if s.mode != modeNew {
		o := op.Offset(image.Pt(pad, listBot+gtx.Dp(8))).Push(gtx.Ops)
		u.newButton(gtx)
		o.Pop()
	}

	if previewW > 0 {
		u.preview(gtx, st, image.Rect(pad+listW+pad, pad, w-pad, h-footH-pad))
	}

	// Footer: a hairline, then the keys the mode takes.
	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.08), clip.Rect{Min: image.Pt(1, h-footH), Max: image.Pt(w-1, h-footH+1)}.Op())
	o = op.Offset(image.Pt(pad, h-footH)).Push(gtx.Ops)
	u.switcherHints(gtx, footH)
	o.Pop()
}

// sessionRow draws session x as row i: its number, name, a "current" tag
// for the window's session and its agents' marks; then its tab count, live
// working and needs-you counts and when it last saw activity. A session
// with unseen attention gets the sidebar's accent bar.
func (u *ui) sessionRow(gtx gl.Context, st *model.State, x model.Session, i int, size image.Point) {
	th, s, d := u.th, &u.sw, &u.sw.draw
	r := d.row(x.ID)
	sum := st.Summary(x.ID)
	sel := x.ID == s.sel
	killing := sel && s.mode == modeKill
	renaming := sel && s.mode == modeRename
	rect := image.Rectangle{Max: size}
	rr := gtx.Dp(8)
	base := th.Surface
	if sel {
		base = th.Hover
	}

	var unseen []model.Activity
	for _, a := range st.Activities {
		if a.Unseen && st.SessionOf(a.WorkspaceID) == x.ID {
			unseen = append(unseen, a)
		}
	}
	accent := theme.Transparent
	if a := model.Aggregate(unseen); a != nil {
		accent = sidebar.StateColor(th, a.State)
		paint.FillShape(gtx.Ops, theme.Mix(base, accent, 0.09), clip.UniformRRect(rect, rr).Op(gtx.Ops))
		bar := image.Rect(gtx.Dp(2), gtx.Dp(12), gtx.Dp(5), size.Y-gtx.Dp(12))
		paint.FillShape(gtx.Ops, accent, clip.UniformRRect(bar, bar.Dx()/2).Op(gtx.Ops))
		base = theme.Mix(base, accent, 0.09)
	}
	if killing {
		paint.FillShape(gtx.Ops, theme.Mix(base, th.Red, 0.1), clip.UniformRRect(rect, rr).Op(gtx.Ops))
		base = theme.Mix(base, th.Red, 0.1)
	}

	g := gtx
	g.Constraints = gl.Exact(size)
	r.row.Layout(g, func(gtx gl.Context) gl.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})

	px := gtx.Dp(12)
	l1, l2 := gtx.Dp(9), gtx.Dp(31)
	// The number keycap, 1-9.
	numW := gtx.Dp(20)
	if i < 9 && !s.filtering {
		kc, ks := keycap(gtx, th, fmt.Sprint(i+1))
		o := op.Offset(image.Pt(px, l1)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		numW = ks.X
	}
	nx := px + numW + gtx.Dp(12)
	right := size.X - px

	// Agent marks on the right of line one.
	ax := right
	for _, p := range sum.Agents {
		ax -= gtx.Dp(15)
		o := op.Offset(image.Pt(ax, l1+gtx.Dp(3))).Push(gtx.Ops)
		sidebar.AgentMark(gtx, p, gtx.Dp(15), th.Fg)
		o.Pop()
		ax -= gtx.Dp(6)
	}

	nameMax := ax - nx - gtx.Dp(8)
	if renaming {
		u.nameField(gtx, image.Rect(nx-gtx.Dp(6), l1-gtx.Dp(3), ax-gtx.Dp(4), l1+gtx.Dp(23)))
	} else {
		ng := gtx
		ng.Constraints.Max.X = max(0, nameMax)
		nc, nsz := textCall(ng, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, x.Name)
		o := op.Offset(image.Pt(nx, l1)).Push(gtx.Ops)
		nc.Add(gtx.Ops)
		o.Pop()
		if x.ID == u.nav.session && nameMax-nsz.X > gtx.Dp(60) {
			tag, tsz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Primary, "current")
			tp := image.Pt(gtx.Dp(5), gtx.Dp(1))
			box := image.Rectangle{Max: tsz.Add(tp.Mul(2))}
			o := op.Offset(image.Pt(nx+nsz.X+gtx.Dp(8), l1+(nsz.Y-box.Dy())/2)).Push(gtx.Ops)
			paint.FillShape(gtx.Ops, theme.Mix(base, th.Primary, 0.16), clip.UniformRRect(box, gtx.Dp(4)).Op(gtx.Ops))
			o2 := op.Offset(tp).Push(gtx.Ops)
			tag.Add(gtx.Ops)
			o2.Pop()
			o.Pop()
		}
	}

	// Line two: counts, or the kill prompt or a rename problem.
	switch {
	case killing:
		msg := "Kill " + plural(sum.Tabs+sum.Detached, "tab") + " and their processes?"
		bx := right
		for _, b := range []struct {
			c      *widget.Clickable
			label  string
			danger bool
		}{{&d.kill, "Kill", true}, {&d.keep, "Keep", false}} {
			bc, bsz := u.smallButton(gtx, b.c, b.label, b.danger)
			bx -= bsz.X
			o := op.Offset(image.Pt(bx, l2-gtx.Dp(4))).Push(gtx.Ops)
			bc.Add(gtx.Ops)
			o.Pop()
			bx -= gtx.Dp(6)
		}
		mg := gtx
		mg.Constraints.Max.X = max(0, bx-nx-gtx.Dp(6))
		mc, _ := textCall(mg, th, th.UIFont, th.Sp(theme.Small), th.Red, msg)
		o := op.Offset(image.Pt(nx, l2)).Push(gtx.Ops)
		mc.Add(gtx.Ops)
		o.Pop()
	case renaming && s.err != "":
		drawText(gtx, th, image.Pt(nx, l2), th.UIFont, th.Sp(theme.Small), th.Red, s.err)
	case renaming:
		drawText(gtx, th, image.Pt(nx, l2), th.UIFont, th.Sp(theme.Small), th.Muted, "Enter renames · Esc cancels")
	default:
		lx := nx
		part := func(c color.NRGBA, dot bool, txt string) {
			if lx > nx {
				sc, ssz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), theme.Mix(base, th.Muted, 0.6), "·")
				o := op.Offset(image.Pt(lx+gtx.Dp(5), l2)).Push(gtx.Ops)
				sc.Add(gtx.Ops)
				o.Pop()
				lx += ssz.X + gtx.Dp(10)
			}
			if dot {
				dd := gtx.Dp(6)
				paint.FillShape(gtx.Ops, c, clip.Ellipse{Min: image.Pt(lx, l2+gtx.Dp(6)), Max: image.Pt(lx+dd, l2+gtx.Dp(6)+dd)}.Op(gtx.Ops))
				lx += dd + gtx.Dp(5)
			}
			tc, tsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), c, txt)
			o := op.Offset(image.Pt(lx, l2)).Push(gtx.Ops)
			tc.Add(gtx.Ops)
			o.Pop()
			lx += tsz.X
		}
		part(theme.Mix(base, th.Muted, 0.9), false, plural(sum.Tabs, "tab"))
		if sum.Working > 0 {
			part(th.Blue, true, fmt.Sprintf("%d working", sum.Working))
		}
		if sum.NeedsYou > 0 {
			c := th.Yellow
			if accent.A > 0 {
				c = accent
			}
			part(c, true, fmt.Sprintf("%d need%s you", sum.NeedsYou, map[bool]string{true: "s"}[sum.NeedsYou == 1]))
		}
		if rt := sidebar.RelTime(gtx.Now, sum.Active); rt != "" && right-lx > gtx.Dp(70) {
			rc, rsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), theme.Mix(base, th.Muted, 0.55), rt)
			o := op.Offset(image.Pt(right-rsz.X, l2+gtx.Dp(1))).Push(gtx.Ops)
			rc.Add(gtx.Ops)
			o.Pop()
		}
	}

	// Rename and kill buttons while the pointer is over the row.
	if d.hover == x.ID && s.mode == modePick {
		bs := gtx.Dp(26)
		bx := right - 2*bs - gtx.Dp(4)
		paint.FillShape(gtx.Ops, base, clip.UniformRRect(image.Rect(bx-gtx.Dp(8), gtx.Dp(4), size.X-gtx.Dp(2), size.Y-gtx.Dp(4)), gtx.Dp(6)).Op(gtx.Ops))
		for j, b := range []struct {
			c    *widget.Clickable
			icon string
			hot  color.NRGBA
		}{{&r.rename, "pencil", th.Fg}, {&r.kill, "trash", th.Red}} {
			o := op.Offset(image.Pt(bx+j*(bs+gtx.Dp(4)), (size.Y-bs)/2)).Push(gtx.Ops)
			bg := gtx
			bg.Constraints = gl.Exact(image.Pt(bs, bs))
			b.c.Layout(bg, func(gtx gl.Context) gl.Dimensions {
				col := th.Muted
				if b.c.Hovered() {
					col = b.hot
					paint.FillShape(gtx.Ops, theme.Mix(base, th.Fg, 0.08), clip.UniformRRect(image.Rect(0, 0, bs, bs), gtx.Dp(6)).Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				o := op.Offset(image.Pt((bs-gtx.Dp(14))/2, (bs-gtx.Dp(14))/2)).Push(gtx.Ops)
				sidebar.Icon(gtx, b.icon, gtx.Dp(14), col)
				o.Pop()
				return gl.Dimensions{Size: image.Pt(bs, bs)}
			})
			o.Pop()
		}
	}
}

// newRow is the row a new session's name is typed in.
func (u *ui) newRow(gtx gl.Context, size image.Point) {
	th, s := u.th, &u.sw
	px := gtx.Dp(12)
	o := op.Offset(image.Pt(px, gtx.Dp(9)+gtx.Dp(3))).Push(gtx.Ops)
	sidebar.Icon(gtx, "plus", gtx.Dp(16), th.Primary)
	o.Pop()
	nx := px + gtx.Dp(20) + gtx.Dp(12)
	u.nameField(gtx, image.Rect(nx-gtx.Dp(6), gtx.Dp(6), size.X-px, gtx.Dp(32)))
	if s.err != "" {
		drawText(gtx, th, image.Pt(nx, gtx.Dp(36)), th.UIFont, th.Sp(theme.Small), th.Red, s.err)
	} else {
		drawText(gtx, th, image.Pt(nx, gtx.Dp(36)), th.UIFont, th.Sp(theme.Small), th.Muted, "Enter makes it and switches · Esc cancels")
	}
}

// nameField draws the name being typed in rect, selected while it is the
// suggestion the first key replaces.
func (u *ui) nameField(gtx gl.Context, rect image.Rectangle) {
	th, s := u.th, &u.sw
	rr := gtx.Dp(6)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Primary, 0.6), clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	call, sz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, s.field)
	x, y := rect.Min.X+gtx.Dp(6), rect.Min.Y+(rect.Dy()-sz.Y)/2
	if s.fresh && s.field != "" {
		sel := image.Rect(x-gtx.Dp(2), y, x+sz.X+gtx.Dp(2), y+sz.Y)
		paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Primary, 0.3), clip.UniformRRect(sel, gtx.Dp(3)).Op(gtx.Ops))
	}
	o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	if !s.fresh || s.field == "" {
		cx := x + sz.X + 1
		u.caret(gtx, image.Rect(cx, y+gtx.Dp(2), cx+gtx.Dp(2), y+sz.Y-gtx.Dp(2)), s.openedAt)
	}
}

// newButton is "New session" under the list.
func (u *ui) newButton(gtx gl.Context) {
	th, d := u.th, &u.sw.draw
	h := gtx.Dp(34)
	label, lsz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Muted, "New session")
	kc, ks := keycap(gtx, th, "N")
	w := gtx.Dp(12) + gtx.Dp(14) + gtx.Dp(8) + lsz.X + gtx.Dp(10) + ks.X + gtx.Dp(10)
	g := gtx
	g.Constraints = gl.Exact(image.Pt(w, h))
	d.newBtn.Layout(g, func(gtx gl.Context) gl.Dimensions {
		col := th.Muted
		if d.newBtn.Hovered() {
			col = th.Fg
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		x := gtx.Dp(12)
		o := op.Offset(image.Pt(x, (h-gtx.Dp(14))/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, "plus", gtx.Dp(14), col)
		o.Pop()
		x += gtx.Dp(14) + gtx.Dp(8)
		if col != th.Muted {
			label, _ = textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), col, "New session")
		}
		o = op.Offset(image.Pt(x, (h-lsz.Y)/2)).Push(gtx.Ops)
		label.Add(gtx.Ops)
		o.Pop()
		x += lsz.X + gtx.Dp(10)
		o = op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		return gl.Dimensions{Size: image.Pt(w, h)}
	})
}

// smallButton records a 24dp button with 6dp corners: red for a danger
// action, quiet otherwise.
func (u *ui) smallButton(gtx gl.Context, c *widget.Clickable, label string, danger bool) (op.CallOp, image.Point) {
	k := kit.Secondary
	if danger {
		k = kit.Danger
	}
	m := op.Record(gtx.Ops)
	d := kit.Button(gtx, u.th, c, k, kit.Small, label)
	return m.Stop(), d.Size
}

// preview draws the highlighted session's sidebar, live and scaled to fit
// rect, behind an area that keeps the pointer off it.
func (u *ui) preview(gtx gl.Context, st *model.State, rect image.Rectangle) {
	th, s, d := u.th, &u.sw, &u.sw.draw
	rr := gtx.Dp(10)
	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Fg, 0.08), clip.UniformRRect(rect, rr).Op(gtx.Ops))
	inner := rect.Inset(1)
	paint.FillShape(gtx.Ops, th.Sidebar, clip.UniformRRect(inner, rr-1).Op(gtx.Ops))
	if st.Session(s.sel) == nil {
		return
	}
	if d.previews == nil {
		d.previews = map[string]*sidebar.Sidebar{}
	}
	sb := d.previews[s.sel]
	if sb == nil {
		sb = &sidebar.Sidebar{ExpandAll: true, TreeOnly: true}
		d.previews[s.sel] = sb
	}
	active := u.nav.lastWS[s.sel]
	if s.sel == u.nav.session {
		active = u.nav.workspace
	}
	fade := anim.At(gtx, s.selAt, anim.Slide)
	defer clip.UniformRRect(inner, rr-1).Push(gtx.Ops).Pop()
	sw := gtx.Dp(sidebar.Width)
	scale := min(1, float32(inner.Dx())/float32(sw))
	sh := int(float32(inner.Dy()) / scale)
	off := float32(inner.Dx()) - float32(sw)*scale // centre a narrower drawing
	o := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(f32.Pt(float32(inner.Min.X)+off/2, float32(inner.Min.Y)))).Push(gtx.Ops)
	fo := paint.PushOpacity(gtx.Ops, 0.35+0.65*fade)
	pg := gtx
	pg.Constraints = gl.Exact(image.Pt(sw, sh))
	drawSidebar(pg, sb, th, st, s.sel, active) // its events are the preview's own
	fo.Pop()
	o.Pop()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &d.block, Kinds: pointer.Press | pointer.Move | pointer.Scroll}); !ok {
			break
		}
	}
	block := clip.Rect(inner).Push(gtx.Ops)
	event.Op(gtx.Ops, &d.block)
	block.Pop()
}

// switcherHints are the keys the switcher's mode takes, as keycaps.
func (u *ui) switcherHints(gtx gl.Context, h int) {
	s := &u.sw
	var hints [][2]string
	switch s.mode {
	case modeNew:
		hints = [][2]string{{"Enter", "make and switch"}, {"Esc", "cancel"}}
	case modeRename:
		hints = [][2]string{{"Enter", "rename"}, {"Esc", "cancel"}}
	case modeKill:
		hints = [][2]string{{"Y", "kill"}, {"N", "keep"}}
	default:
		hints = [][2]string{{"↑↓", "move"}, {"Enter", "switch"}}
		if !s.filtering {
			hints = append(hints, [2]string{"1-9", "jump"}, [2]string{"N", "new"}, [2]string{"R", "rename"}, [2]string{"X", "kill"})
		}
		hints = append(hints, [2]string{"Esc", map[bool]string{true: "clear filter", false: "close"}[s.filtering]})
	}
	u.drawHints(gtx, h, hints)
}

// plural is "1 tab", "3 tabs".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, strings.TrimSuffix(word, "s"))
}
