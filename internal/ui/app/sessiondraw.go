package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The session switcher opens over 170ms; the highlight eases between rows
// with a 60ms time constant, and the preview fades in over 140ms when the
// highlight lands on another session.
const (
	switcherOpen = 170 * time.Millisecond
	previewFade  = 140 * time.Millisecond
)

// switcherDraw is the switcher's widgets: tags, clickables and one preview
// sidebar per session, so each keeps its own expanded groups.
type switcherDraw struct {
	backdrop, card, block int
	rows                  map[string]*switcherRow
	newBtn, keep, kill    widget.Clickable
	hover                 string // the row the pointer is over
	previews              map[string]*sidebar.Sidebar
	scroll                int
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

func easeOut(t float32) float32 { t = min(max(t, 0), 1); return 1 - (1-t)*(1-t)*(1-t) }

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
	t := easeOut(float32(gtx.Now.Sub(s.openedAt)) / float32(switcherOpen))
	if t < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}

	// Backdrop: dims the window and closes the switcher on a press.
	paint.FillShape(gtx.Ops, color.NRGBA{A: uint8(0xa6 * t)}, clip.Rect{Max: size}.Op())
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.backdrop, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			s.close()
		}
	}
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.backdrop)
	bg.Pop()
	if !s.open {
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
	card := image.Rectangle{Max: image.Pt(w, h)}
	at := size.Sub(card.Size()).Div(2)

	defer paint.PushOpacity(gtx.Ops, t).Pop()
	scale := 0.985 + 0.015*t
	center := f32.Pt(float32(at.X)+float32(w)/2, float32(at.Y)+float32(h)/2)
	defer op.Affine(f32.AffineId().Scale(center, f32.Pt(scale, scale)).Offset(f32.Pt(float32(at.X), float32(at.Y)+float32(gtx.Dp(10))*(1-t)))).Push(gtx.Ops).Pop()

	r := gtx.Dp(14)
	for i, a := range []uint8{0x22, 0x1a, 0x12} {
		g := gtx.Dp(unit.Dp(6 * (i + 1)))
		paint.FillShape(gtx.Ops, color.NRGBA{A: a}, clip.UniformRRect(card.Add(image.Pt(0, gtx.Dp(8))).Inset(-g), r+g).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.1), clip.UniformRRect(card, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(card.Inset(1), r-1).Op(gtx.Ops))
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &d.card, Kinds: pointer.Press}); !ok {
			break
		}
	}
	cardArea := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, &d.card) // presses on the card stop here, short of the backdrop
	cardArea.Pop()

	footH := gtx.Dp(44)
	// Header: the title and the count, the switcher's key on the right.
	y := pad
	title := "Sessions"
	tc, tsz := textCall(gtx, th, semibold(th.UIFont), 15, th.Fg, title)
	o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	cc, _ := textCall(gtx, th, th.UIFont, 13, th.Muted, fmt.Sprint(len(st.Sessions)))
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
	u.filterField(gtx, image.Rect(pad, y, pad+listW, y+fieldH), st)
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
	viewH := listBot - listTop
	if selI >= 0 {
		top := selI * (rowH + gap)
		d.scroll = min(d.scroll, top)
		d.scroll = max(d.scroll, top+rowH-viewH)
	}
	d.scroll = max(0, min(d.scroll, n*(rowH+gap)-gap-viewH))
	lc := clip.Rect{Min: image.Pt(0, listTop), Max: image.Pt(w, listBot)}.Push(gtx.Ops)
	if selI >= 0 {
		target := float32(listTop + selI*(rowH+gap) - d.scroll)
		if s.selY < 0 || s.lastAt.IsZero() {
			s.selY = target
		} else if dt := gtx.Now.Sub(s.lastAt).Seconds(); dt > 0 {
			k := float32(1 - math.Exp(-dt/0.06))
			s.selY += (target - s.selY) * k
			if diff := target - s.selY; diff > 0.5 || diff < -0.5 {
				gtx.Execute(op.InvalidateCmd{})
			} else {
				s.selY = target
			}
		}
		hl := image.Rect(pad, int(s.selY+0.5), pad+listW, int(s.selY+0.5)+rowH)
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.065), clip.UniformRRect(hl, gtx.Dp(8)).Op(gtx.Ops))
	}
	s.lastAt = gtx.Now
	for i, x := range rows {
		ry := listTop + i*(rowH+gap) - d.scroll
		if ry+rowH < listTop || ry > listBot {
			continue
		}
		o := op.Offset(image.Pt(pad, ry)).Push(gtx.Ops)
		u.sessionRow(gtx, st, x, i, image.Pt(listW, rowH))
		o.Pop()
	}
	if s.mode == modeNew {
		ry := listTop + len(rows)*(rowH+gap) - d.scroll
		o := op.Offset(image.Pt(pad, ry)).Push(gtx.Ops)
		u.newRow(gtx, image.Pt(listW, rowH))
		o.Pop()
	}
	if selI >= 0 {
		// A ring over the rows, so it shows on a tinted one too.
		hl := image.Rect(pad, int(s.selY+0.5), pad+listW, int(s.selY+0.5)+rowH)
		ring := clip.UniformRRect(hl, gtx.Dp(8)).Path(gtx.Ops)
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.22), clip.Stroke{Path: ring, Width: float32(gtx.Dp(1))}.Op())
	}
	if len(rows) == 0 && s.mode != modeNew {
		drawText(gtx, th, image.Pt(pad+gtx.Dp(12), listTop+gtx.Dp(12)), th.UIFont, 13, th.Muted, "No session matches \""+s.filter+"\".")
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

// filterField draws the filter, with a caret while typed keys go to it.
func (u *ui) filterField(gtx gl.Context, rect image.Rectangle, st *model.State) {
	th, s := u.th, &u.sw
	rr := gtx.Dp(8)
	border := theme.Mix(th.SurfaceSecondary, th.Fg, 0.07)
	if s.filtering && s.mode == modePick {
		border = theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	x := rect.Min.X + gtx.Dp(12)
	text, col := s.filter, th.Fg
	if text == "" && !s.filtering {
		text, col = "Type to filter", theme.Mix(th.SurfaceSecondary, th.Muted, 0.75)
	}
	call, sz := textCall(gtx, th, th.UIFont, 13, col, text)
	ty := rect.Min.Y + (rect.Dy()-sz.Y)/2
	o := op.Offset(image.Pt(x, ty)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	if s.filtering && s.mode == modePick {
		cx := x
		if s.filter != "" {
			cx += sz.X + 1
		}
		u.caret(gtx, image.Rect(cx, ty+gtx.Dp(1), cx+gtx.Dp(2), ty+sz.Y-gtx.Dp(1)))
		if s.filter != "" {
			c, csz := textCall(gtx, th, th.UIFont, 12, th.Muted, fmt.Sprintf("%d of %d", len(s.rows(st)), len(st.Sessions)))
			o := op.Offset(image.Pt(rect.Max.X-gtx.Dp(12)-csz.X, rect.Min.Y+(rect.Dy()-csz.Y)/2)).Push(gtx.Ops)
			c.Add(gtx.Ops)
			o.Pop()
		}
	}
}

// caret draws a text caret that blinks once a second.
func (u *ui) caret(gtx gl.Context, r image.Rectangle) {
	phase := gtx.Now.Sub(u.sw.openedAt) % time.Second
	if phase < 600*time.Millisecond {
		paint.FillShape(gtx.Ops, u.th.Primary, clip.Rect(r).Op())
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(600*time.Millisecond - phase)})
	} else {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second - phase)})
	}
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
		base = theme.Mix(th.Surface, th.Fg, 0.065)
	}

	var unseen []model.Activity
	for _, a := range st.Activities {
		if a.Unseen && st.SessionOf(a.WorkspaceID) == x.ID {
			unseen = append(unseen, a)
		}
	}
	accent := color.NRGBA{}
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
		nc, nsz := textCall(ng, th, semibold(th.UIFont), 14, th.Fg, x.Name)
		o := op.Offset(image.Pt(nx, l1)).Push(gtx.Ops)
		nc.Add(gtx.Ops)
		o.Pop()
		if x.ID == u.nav.session && nameMax-nsz.X > gtx.Dp(60) {
			tag, tsz := textCall(gtx, th, semibold(th.UIFont), 10.5, th.Primary, "current")
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
		mc, _ := textCall(mg, th, th.UIFont, 12, th.Red, msg)
		o := op.Offset(image.Pt(nx, l2)).Push(gtx.Ops)
		mc.Add(gtx.Ops)
		o.Pop()
	case renaming && s.err != "":
		drawText(gtx, th, image.Pt(nx, l2), th.UIFont, 12, th.Red, s.err)
	case renaming:
		drawText(gtx, th, image.Pt(nx, l2), th.UIFont, 12, th.Muted, "Enter renames · Esc cancels")
	default:
		lx := nx
		part := func(c color.NRGBA, dot bool, txt string) {
			if lx > nx {
				sc, ssz := textCall(gtx, th, th.UIFont, 12, theme.Mix(base, th.Muted, 0.6), "·")
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
			tc, tsz := textCall(gtx, th, th.UIFont, 12, c, txt)
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
			rc, rsz := textCall(gtx, th, th.UIFont, 11.5, theme.Mix(base, th.Muted, 0.55), rt)
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
		drawText(gtx, th, image.Pt(nx, gtx.Dp(36)), th.UIFont, 12, th.Red, s.err)
	} else {
		drawText(gtx, th, image.Pt(nx, gtx.Dp(36)), th.UIFont, 12, th.Muted, "Enter makes it and switches · Esc cancels")
	}
}

// nameField draws the name being typed in rect, selected while it is the
// suggestion the first key replaces.
func (u *ui) nameField(gtx gl.Context, rect image.Rectangle) {
	th, s := u.th, &u.sw
	rr := gtx.Dp(6)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Primary, 0.6), clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	call, sz := textCall(gtx, th, semibold(th.UIFont), 14, th.Fg, s.field)
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
		u.caret(gtx, image.Rect(cx, y+gtx.Dp(2), cx+gtx.Dp(2), y+sz.Y-gtx.Dp(2)))
	}
}

// newButton is "New session" under the list.
func (u *ui) newButton(gtx gl.Context) {
	th, d := u.th, &u.sw.draw
	h := gtx.Dp(34)
	label, lsz := textCall(gtx, th, medium(th.UIFont), 13, th.Muted, "New session")
	kc, ks := keycap(gtx, th, "N")
	w := gtx.Dp(12) + gtx.Dp(14) + gtx.Dp(8) + lsz.X + gtx.Dp(10) + ks.X + gtx.Dp(10)
	g := gtx
	g.Constraints = gl.Exact(image.Pt(w, h))
	d.newBtn.Layout(g, func(gtx gl.Context) gl.Dimensions {
		col := th.Muted
		if d.newBtn.Hovered() {
			col = th.Fg
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.065), clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		x := gtx.Dp(12)
		o := op.Offset(image.Pt(x, (h-gtx.Dp(14))/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, "plus", gtx.Dp(14), col)
		o.Pop()
		x += gtx.Dp(14) + gtx.Dp(8)
		if col != th.Muted {
			label, _ = textCall(gtx, th, medium(th.UIFont), 13, col, "New session")
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
	th := u.th
	m := op.Record(gtx.Ops)
	fg, bg := th.Fg, th.SurfaceSecondary
	if danger {
		fg, bg = theme.Hex("#ffffff"), th.Red
	}
	call, sz := textCall(gtx, th, medium(th.UIFont), 12, fg, label)
	box := image.Pt(sz.X+gtx.Dp(20), gtx.Dp(24))
	g := gtx
	g.Constraints = gl.Exact(box)
	c.Layout(g, func(gtx gl.Context) gl.Dimensions {
		b := bg
		if c.Hovered() {
			b = theme.Mix(bg, th.Fg, 0.1)
		}
		paint.FillShape(gtx.Ops, b, clip.UniformRRect(image.Rectangle{Max: box}, gtx.Dp(6)).Op(gtx.Ops))
		pointer.CursorPointer.Add(gtx.Ops)
		o := op.Offset(box.Sub(sz).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return gl.Dimensions{Size: box}
	})
	return m.Stop(), box
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
		sb = &sidebar.Sidebar{ExpandAll: true}
		d.previews[s.sel] = sb
	}
	active := u.nav.lastWS[s.sel]
	if s.sel == u.nav.session {
		active = u.nav.workspace
	}
	fade := easeOut(float32(gtx.Now.Sub(s.selAt)) / float32(previewFade))
	if fade < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
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
	th, s := u.th, &u.sw
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
	x := 0
	for _, k := range hints {
		kc, ks := keycap(gtx, th, k[0])
		o := op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		x += ks.X + gtx.Dp(6)
		tc, tsz := textCall(gtx, th, th.UIFont, 12, th.Muted, k[1])
		o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X + gtx.Dp(16)
	}
}

// plural is "1 tab", "3 tabs".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, strings.TrimSuffix(word, "s"))
}
