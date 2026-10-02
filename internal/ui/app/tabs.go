package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// tabStripH matches the sidebar header, so the two read as one top bar
// (aide's TopBar is h-14 with a bottom border).
const tabStripH = unit.Dp(56)

type tabBtn struct {
	click, close widget.Clickable
	mid          int // tag for the middle-click close
}

// tabStrip is aide's TopBar surface strip, one entry per tab of the shown
// session.
type tabStrip struct {
	btns map[string]*tabBtn
	add  widget.Clickable

	ws, renaming string // the tab being renamed inline
	focusEditor  bool
	editor       widget.Editor
}

// tabLabel is what a tab shows: its name, else its title, else "tab N".
func tabLabel(t model.Tab, i int) string {
	switch {
	case t.Name != "":
		return t.Name
	case t.Title != "":
		return t.Title
	}
	return fmt.Sprintf("tab %d", i+1)
}

func (s *tabStrip) btn(id string) *tabBtn {
	if s.btns == nil {
		s.btns = map[string]*tabBtn{}
	}
	b := s.btns[id]
	if b == nil {
		b = &tabBtn{}
		s.btns[id] = b
	}
	return b
}

func (s *tabStrip) startRename(ws, tab, name string) {
	s.ws, s.renaming, s.focusEditor = ws, tab, true
	s.editor.SingleLine, s.editor.Submit = true, true
	s.editor.SetText(name)
	n := utf8.RuneCountInString(name)
	s.editor.SetCaret(n, 0)
}

// showTabs reports whether the strip shows: with two tabs or more, or while
// tab mode or a tab rename needs it. A lone tab would only repeat the
// session name above every terminal.
func (u *ui) showTabs(w *model.Workspace) bool {
	return w != nil && len(w.Tabs) > 0 && (len(w.Tabs) > 1 || u.nav.tabMode || u.tabs.renaming != "")
}

// tabActivity is the activity a tab's dot shows, nil when none of its panes
// has one.
func tabActivity(st *model.State, t model.Tab) *model.Activity {
	var acts []model.Activity
	for _, p := range panesOf(t.Layout) {
		for _, a := range st.Activities {
			if a.PaneID == p {
				acts = append(acts, a)
			}
		}
	}
	return model.Aggregate(acts)
}

// tabUpdate handles last frame's clicks on the strip.
func (u *ui) tabUpdate(gtx gl.Context, st *model.State, w *model.Workspace) {
	s := &u.tabs
	for _, t := range w.Tabs {
		b := s.btn(t.ID)
		for {
			c, ok := b.click.Update(gtx)
			if !ok {
				break
			}
			if c.NumClicks >= 2 {
				i := 0
				for j := range w.Tabs {
					if w.Tabs[j].ID == t.ID {
						i = j
					}
				}
				s.startRename(w.ID, t.ID, tabLabel(t, i))
			} else if msg := u.nav.selectTab(st, t.ID); msg != nil {
				u.send(msg)
			}
		}
		for b.close.Clicked(gtx) {
			u.send(proto.CloseTab{WorkspaceID: w.ID, TabID: t.ID})
		}
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &b.mid, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonTertiary {
				u.send(proto.CloseTab{WorkspaceID: w.ID, TabID: t.ID})
			}
		}
	}
	for s.add.Clicked(gtx) {
		delete(u.nav.pick, w.ID)
		u.send(proto.NewTab{WorkspaceID: w.ID, FromPane: u.nav.focused()})
	}
	if s.renaming == "" {
		return
	}
	for {
		ev, ok := s.editor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			// An empty name goes back to the automatic title.
			u.send(proto.RenameTab{WorkspaceID: s.ws, TabID: s.renaming, Name: strings.TrimSpace(s.editor.Text())})
			s.renaming = ""
		}
	}
	for {
		if _, ok := gtx.Event(key.Filter{Focus: &s.editor, Name: key.NameEscape}); !ok {
			break
		}
		s.renaming = ""
	}
	if s.renaming != "" && (s.ws != w.ID || !hasTab(w, s.renaming) || !s.focusEditor && !gtx.Focused(&s.editor)) {
		s.renaming = ""
	}
}

func hasTab(w *model.Workspace, id string) bool {
	for _, t := range w.Tabs {
		if t.ID == id {
			return true
		}
	}
	return false
}

// layoutTabs draws the strip across gtx's width and returns its height.
func (u *ui) layoutTabs(gtx gl.Context, st *model.State, w *model.Workspace) int {
	th, s := u.th, &u.tabs
	width, h := gtx.Constraints.Max.X, gtx.Dp(tabStripH)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(width, h)}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(width, h)}.Op())
	defer clip.Rect{Max: image.Pt(width, h-1)}.Push(gtx.Ops).Pop()

	px, gap, itemH := gtx.Dp(12), gtx.Dp(4), gtx.Dp(32)
	right := width - px
	if u.nav.tabMode {
		call, sz := modePill(gtx, th)
		right -= sz.X
		o := op.Offset(image.Pt(right, (h-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		right -= gtx.Dp(12)
	}
	x := px
	// The session name leads the strip, as aide's workspace heading does.
	name, nsz := textCall(gtx, th, semibold(th.UIFont), 13, th.Fg, w.Name)
	if nsz.X > gtx.Dp(160) {
		ng := gtx
		ng.Constraints.Max.X = gtx.Dp(160)
		name, nsz = textCall(ng, th, semibold(th.UIFont), 13, th.Fg, w.Name)
	}
	o := op.Offset(image.Pt(x, (h-nsz.Y)/2)).Push(gtx.Ops)
	name.Add(gtx.Ops)
	o.Pop()
	x += nsz.X + gtx.Dp(16)

	animate := false
	addW := itemH
	for i, t := range w.Tabs {
		avail := right - addW - gap - x
		if avail < gtx.Dp(48) {
			break // no room left; tab mode's numbers still reach them
		}
		a := tabActivity(st, t)
		animate = animate || model.Pulses(a)
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(min(avail, gtx.Dp(220)), itemH)}
		o := op.Offset(image.Pt(x, (h-itemH)/2)).Push(gtx.Ops)
		d := u.tabItem(g, s.btn(t.ID), w, t, i, a)
		o.Pop()
		x += d.X + gap
	}
	o = op.Offset(image.Pt(x, (h-itemH)/2)).Push(gtx.Ops)
	ag := gtx
	ag.Constraints = gl.Exact(image.Pt(itemH, itemH))
	ghostButton(ag, th, &s.add, func(gtx gl.Context, col color.NRGBA) {
		glyph(gtx, itemH, gtx.Dp(14), col, true)
	})
	o.Pop()
	if animate {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	}
	return h
}

// tabItem is aide's SurfaceTab: a numbered badge, the label, the agent dot,
// and a close button that shows on hover. It returns its size.
func (u *ui) tabItem(gtx gl.Context, b *tabBtn, w *model.Workspace, t model.Tab, i int, a *model.Activity) image.Point {
	th, s := u.th, &u.tabs
	h := gtx.Constraints.Max.Y
	active := t.ID == u.nav.tab
	renaming := s.renaming == t.ID && s.ws == w.ID
	hovered := b.click.Hovered() || b.close.Hovered()

	fg := th.Muted
	if active || hovered || renaming {
		fg = th.Fg
	}
	badge, dot := gtx.Dp(20), gtx.Dp(8)
	pl, pr, g := gtx.Dp(10), gtx.Dp(32), gtx.Dp(8)
	fixed := pl + badge + g + pr
	if a != nil {
		fixed += g + dot
	}
	var label op.CallOp
	var lsz image.Point
	if !renaming {
		lg := gtx
		lg.Constraints.Max.X = max(0, gtx.Constraints.Max.X-fixed)
		label, lsz = textCall(lg, th, semibold(th.UIFont), 12, fg, tabLabel(t, i))
	} else {
		lsz = image.Pt(min(gtx.Dp(140), max(0, gtx.Constraints.Max.X-fixed)), gtx.Dp(22))
	}
	size := image.Pt(fixed+lsz.X, h)
	rect := image.Rectangle{Max: size}
	r := gtx.Dp(6)
	switch {
	case active:
		paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(rect, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
	case hovered:
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(rect, r).Op(gtx.Ops))
	}

	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &b.mid)
	cg := gtx
	cg.Constraints = gl.Exact(size)
	b.click.Layout(cg, func(gtx gl.Context) gl.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		x := pl
		// size-5 rounded-full border bg-surface-tertiary, 10px muted digit
		bo := op.Offset(image.Pt(x, (h-badge)/2)).Push(gtx.Ops)
		paint.FillShape(gtx.Ops, th.Border, clip.Ellipse{Max: image.Pt(badge, badge)}.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.Ellipse{Min: image.Pt(1, 1), Max: image.Pt(badge-1, badge-1)}.Op(gtx.Ops))
		num, nsz := textCall(gtx, th, semibold(th.UIFont), 10, th.Muted, fmt.Sprint(i+1))
		no := op.Offset(image.Pt(badge, badge).Sub(nsz).Div(2)).Push(gtx.Ops)
		num.Add(gtx.Ops)
		no.Pop()
		bo.Pop()
		x += badge + g
		if !renaming {
			lo := op.Offset(image.Pt(x, (h-lsz.Y)/2)).Push(gtx.Ops)
			label.Add(gtx.Ops)
			lo.Pop()
		}
		x += lsz.X
		if a != nil {
			col := stateColor(th, a.State)
			bg := th.Bg
			if active {
				bg = th.SurfaceSecondary
			}
			if model.Pulses(a) {
				col = theme.Mix(bg, col, pulseAt(gtx.Now))
			}
			do := op.Offset(image.Pt(x+g, (h-dot)/2)).Push(gtx.Ops)
			paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(dot, dot)}.Op(gtx.Ops))
			do.Pop()
		}
		return gl.Dimensions{Size: size}
	})
	area.Pop()

	if renaming {
		eo := op.Offset(image.Pt(pl+badge+g, (h-lsz.Y)/2)).Push(gtx.Ops)
		u.tabEditor(gtx, lsz)
		eo.Pop()
	}
	// absolute right-1 size-6 ghost button with a 14dp x
	cb := gtx.Dp(24)
	co := op.Offset(image.Pt(size.X-gtx.Dp(4)-cb, (h-cb)/2)).Push(gtx.Ops)
	xg := gtx
	xg.Constraints = gl.Exact(image.Pt(cb, cb))
	if hovered && !renaming {
		ghostButton(xg, th, &b.close, func(gtx gl.Context, col color.NRGBA) {
			glyph(gtx, cb, gtx.Dp(10), col, false)
		})
	} else {
		b.close.Layout(xg, func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(cb, cb)} })
	}
	co.Pop()
	return size
}

func (u *ui) tabEditor(gtx gl.Context, size image.Point) {
	s, th := &u.tabs, u.th
	if s.focusEditor {
		gtx.Execute(key.FocusCmd{Tag: &s.editor})
		s.focusEditor = false
	}
	r := gtx.Dp(4)
	rect := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Primary, 0.6), clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
	eg := gtx
	eg.Constraints = gl.Exact(image.Pt(size.X-gtx.Dp(12), size.Y))
	o := op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops)
	gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return s.editor.Layout(gtx, th.Shaper, semibold(th.UIFont), 12, colorCall(gtx, th.Fg), colorCall(gtx, theme.Mix(th.Surface, th.Primary, 0.35)))
	})
	o.Pop()
}

// modePill is the tab-mode indicator: a "TAB" chip and the keys it takes.
func modePill(gtx gl.Context, th *theme.Theme) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	x, h := 0, gtx.Dp(28)
	tag, tsz := textCall(gtx, th, semibold(th.UIFont), 11, th.Primary, "TAB")
	cw := tsz.X + gtx.Dp(16)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Primary, 0.16), clip.UniformRRect(image.Rect(0, 0, cw, h), h/2).Op(gtx.Ops))
	o := op.Offset(image.Pt(gtx.Dp(8), (h-tsz.Y)/2)).Push(gtx.Ops)
	tag.Add(gtx.Ops)
	o.Pop()
	x += cw + gtx.Dp(10)
	for _, k := range [][2]string{{"N", "new"}, {"X", "close"}, {"R", "rename"}, {"H/L", "move"}, {"1-9", "go"}, {"Esc", "exit"}} {
		kc, ks := keycap(gtx, th, k[0])
		o := op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		x += ks.X + gtx.Dp(5)
		tc, tsz := textCall(gtx, th, th.UIFont, 12, th.Muted, k[1])
		o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X + gtx.Dp(12)
	}
	return m.Stop(), image.Pt(x-gtx.Dp(12), h)
}

// ghostButton is aide's ghost icon button: muted glyph, a surface fill and
// the foreground color on hover.
func ghostButton(gtx gl.Context, th *theme.Theme, c *widget.Clickable, draw func(gtx gl.Context, col color.NRGBA)) {
	size := gtx.Constraints.Max
	c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		col := th.Muted
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
			col = th.Fg
		}
		draw(gtx, col)
		return gl.Dimensions{Size: size}
	})
}

// glyph strokes a centered "+" or "x" of side g in a box of side box.
func glyph(gtx gl.Context, box, g int, col color.NRGBA, plus bool) {
	c, r := float32(box)/2, float32(g)/2
	var p clip.Path
	p.Begin(gtx.Ops)
	if plus {
		p.MoveTo(f32.Pt(c-r, c))
		p.LineTo(f32.Pt(c+r, c))
		p.MoveTo(f32.Pt(c, c-r))
		p.LineTo(f32.Pt(c, c+r))
	} else {
		p.MoveTo(f32.Pt(c-r, c-r))
		p.LineTo(f32.Pt(c+r, c+r))
		p.MoveTo(f32.Pt(c+r, c-r))
		p.LineTo(f32.Pt(c-r, c+r))
	}
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(1.5))}.Op())
}

// stateColor matches the sidebar's dot and pill colors.
func stateColor(th *theme.Theme, s model.AgentState) color.NRGBA {
	_, fg := chipColors(th, s)
	return fg
}

// pulseAt is animate-pulse: 1 -> .5 -> 1 every 2s.
func pulseAt(t time.Time) float32 {
	return float32(0.75 + 0.25*math.Cos(float64(t.UnixMilli()%2000)/1000*math.Pi))
}
