package settings

import (
	"fmt"
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const (
	// navCollapse is the page width under which the category column gives
	// up its place for a dropdown in a bar on top.
	navCollapse unit.Dp = 760
	// minLabel is the narrowest a row's label column gets beside its
	// control; narrower, the control goes under the label.
	minLabel unit.Dp = 220
	// catMenu is p.dd while the category dropdown is open.
	catMenu = "cat"
)

// collapsed reports whether a page w pixels wide puts its categories in
// a dropdown.
func collapsed(gtx gl.Context, w int) bool { return w < gtx.Dp(navCollapse) }

// stacks reports whether a row w pixels wide, with a control control
// pixels wide, puts the control under its label.
func stacks(gtx gl.Context, w, control int) bool {
	return w-control-gtx.Dp(theme.SpaceL) < gtx.Dp(minLabel)
}

// themeCols is how many theme cards go in a row w pixels wide: four when
// they fit at 140dp, else two, so four themes never leave one alone.
func themeCols(gtx gl.Context, w int) int {
	if w >= 4*gtx.Dp(140)+3*gtx.Dp(theme.SpaceM) {
		return 4
	}
	return 2
}

// columns lays out the page's two parts: the category column beside the
// content, or in a narrow page, a bar with a category dropdown and the
// search field above it.
func (p *Page) columns(gtx gl.Context, q string) {
	th, size := p.th, gtx.Constraints.Max
	if !collapsed(gtx, size.X) {
		if p.dd == catMenu {
			p.dd = ""
		}
		navW := min(gtx.Dp(224), size.X/3)
		paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(navW, size.Y)}.Op())
		paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(navW, 0), Max: image.Pt(navW+1, size.Y)}.Op())
		ng := gtx
		ng.Constraints = gl.Exact(image.Pt(navW, size.Y))
		p.nav(ng, q)
		cg := gtx
		cg.Constraints = gl.Exact(image.Pt(size.X-navW-1, size.Y))
		o := op.Offset(image.Pt(navW+1, 0)).Push(gtx.Ops)
		p.content(cg, q)
		o.Pop()
		return
	}
	pad, fh := gtx.Dp(theme.SpaceL), gtx.Dp(32)
	barH := fh + 2*gtx.Dp(theme.SpaceM)
	cg := gtx
	cg.Constraints = gl.Exact(image.Pt(size.X, max(0, size.Y-barH-1)))
	o := op.Offset(image.Pt(0, barH+1)).Push(gtx.Ops)
	p.content(cg, q)
	o.Pop()

	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(size.X, barH)}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, barH), Max: image.Pt(size.X, barH+1)}.Op())
	tw := min(gtx.Dp(220), (size.X-3*pad)/2)
	trigger := image.Rect(pad, gtx.Dp(theme.SpaceM), pad+tw, gtx.Dp(theme.SpaceM)+fh)
	o = op.Offset(trigger.Min).Push(gtx.Ops)
	p.catTrigger(gtx, q, trigger.Size())
	o.Pop()
	sx := trigger.Max.X + gtx.Dp(theme.SpaceS)
	sg := gtx
	sg.Constraints = gl.Exact(image.Pt(max(0, size.X-sx-pad), fh))
	o = op.Offset(image.Pt(sx, trigger.Min.Y)).Push(gtx.Ops)
	p.searchField(sg)
	o.Pop()
	if p.dd == catMenu {
		p.catPopover(gtx, q, trigger, size)
	}
}

// catTrigger is the narrow page's category dropdown button, sz big.
func (p *Page) catTrigger(gtx gl.Context, q string, sz image.Point) {
	th := p.th
	c := p.btn("dd:" + catMenu)
	for c.Clicked(gtx) {
		if p.dd == catMenu {
			p.dd = ""
		} else {
			p.dd, p.conflict, p.rec = catMenu, nil, slot{}
		}
	}
	name := categories[p.cat].name
	if q != "" {
		name = "Search"
	}
	gtx.Constraints = gl.Exact(sz)
	c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		r := gtx.Dp(theme.RadiusControl)
		rect := image.Rectangle{Max: sz}
		if gtx.Focused(c) {
			kit.FocusRing(gtx, th, rect, r)
		}
		border := th.Border
		if c.Hovered() || p.dd == catMenu {
			border = theme.Mix(th.Border, th.Fg, 0.25)
		}
		rrect(gtx, border, rect, r)
		rrect(gtx, th.SurfaceElevated, rect.Inset(1), r-1)
		g := gtx
		g.Constraints.Max.X = sz.X - gtx.Dp(40)
		m := op.Record(gtx.Ops)
		td := p.text(g, weight(th.UIFont, font.Medium), th.Sp(theme.Body), th.Fg, name)
		call := m.Stop()
		o := op.Offset(image.Pt(gtx.Dp(10), (sz.Y-td.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		is := gtx.Dp(14)
		o = op.Offset(image.Pt(sz.X-gtx.Dp(10)-is, (sz.Y-is)/2)).Push(gtx.Ops)
		icon(gtx, th.Muted, is, chevronGlyph)
		o.Pop()
		defer clip.Rect(rect).Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: sz}
	})
}

// catPopover is the open category dropdown under trigger, inside a page
// of size. A press outside it closes it.
func (p *Page) catPopover(gtx gl.Context, q string, trigger image.Rectangle, size image.Point) {
	th := p.th
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.ddTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			p.dd = ""
		}
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &p.ddPanel, Kinds: pointer.Press}); !ok {
			break
		}
	}
	counts := p.counts(q)
	pad, rowH := gtx.Dp(theme.SpaceXS), gtx.Dp(30)+gtx.Dp(theme.Space2XS)
	panel := image.Pt(max(trigger.Dx(), gtx.Dp(220)), 2*pad+len(categories)*rowH)
	at := kit.Place(trigger, panel, image.Rectangle{Max: size}, kit.BelowStart, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceS))

	m := op.Record(gtx.Ops)
	bg := clip.Rect{Min: image.Pt(-1<<14, -1<<14), Max: image.Pt(1<<14, 1<<14)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddTag)
	bg.Pop()
	o := op.Offset(at).Push(gtx.Ops)
	rect := image.Rectangle{Max: panel}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	pa := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddPanel)
	pa.Pop()
	for i := range categories {
		ro := op.Offset(image.Pt(pad, pad+i*rowH)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(panel.X-2*pad, gtx.Dp(30)))
		p.catRow(g, i, q, counts)
		ro.Pop()
	}
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

// counts is how many rows of each category match q, nil without one.
func (p *Page) counts(q string) []int {
	counts := make([]int, len(categories))
	if q != "" {
		for i := range categories {
			for _, sec := range p.sections(i) {
				counts[i] += len(filterRows(sec.rows, q))
			}
		}
	}
	return counts
}

// catRow is category i's button in the nav or the dropdown: raised when
// it is open, with how many rows match q while searching.
func (p *Page) catRow(gtx gl.Context, i int, q string, counts []int) gl.Dimensions {
	th := p.th
	b := &p.cats[i]
	for b.Clicked(gtx) {
		p.cat, p.list.Position, p.dd, p.conflict = i, gl.Position{}, "", nil
		p.focusKeys, p.keyRec = i == catKeys, false
		p.search.SetText("")
	}
	return b.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		h, w := gtx.Dp(30), gtx.Constraints.Max.X
		rect, r := image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)
		sel := q == "" && p.cat == i
		fg := th.Muted
		f := th.UIFont
		switch {
		case sel:
			rrect(gtx, th.SelectedBg, rect, r)
			fg, f = th.Fg, weight(f, font.Medium)
		case b.Hovered():
			hover := th.Hover
			if p.dd == catMenu { // Hover is the dropdown's own fill
				hover = th.Pressed
			}
			rrect(gtx, hover, rect, r)
			fg = th.Fg
		case q != "" && counts[i] == 0:
			fg = theme.Mix(th.Bg, th.Muted, 0.5)
		}
		if gtx.Focused(b) {
			kit.FocusRing(gtx, th, rect, r)
		}
		m := op.Record(gtx.Ops)
		d := p.text(gtx, f, th.Sp(theme.Body), fg, categories[i].name)
		call := m.Stop()
		oo := op.Offset(image.Pt(gtx.Dp(10), (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		oo.Pop()
		if q != "" && counts[i] > 0 {
			m := op.Record(gtx.Ops)
			d := p.text(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, fmt.Sprint(counts[i]))
			call := m.Stop()
			oo := op.Offset(image.Pt(w-gtx.Dp(10)-d.Size.X, (h-d.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			oo.Pop()
		}
		defer clip.Rect(rect).Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: rect.Max}
	})
}

// rowLine is a row's label column and control: side by side, the control
// right and both centred, or the control under the label when the label
// column would be narrower than minLabel.
func (p *Page) rowLine(gtx gl.Context, labels, control gl.Widget) gl.Dimensions {
	w := gtx.Constraints.Max.X
	m := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints = gl.Constraints{Max: gtx.Constraints.Max}
	cd := control(cg)
	ctrl := m.Stop()
	lg := gtx
	lg.Constraints = gl.Constraints{Min: image.Pt(0, 0), Max: gtx.Constraints.Max}
	if stacks(gtx, w, cd.Size.X) {
		ld := labels(lg)
		y := ld.Size.Y + gtx.Dp(theme.SpaceM)
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		ctrl.Add(gtx.Ops)
		o.Pop()
		return gl.Dimensions{Size: image.Pt(w, y+cd.Size.Y)}
	}
	lg.Constraints.Max.X = w - cd.Size.X - gtx.Dp(theme.SpaceL)
	m = op.Record(gtx.Ops)
	ld := labels(lg)
	lab := m.Stop()
	h := max(ld.Size.Y, cd.Size.Y)
	o := op.Offset(image.Pt(0, (h-ld.Size.Y)/2)).Push(gtx.Ops)
	lab.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(w-cd.Size.X, (h-cd.Size.Y)/2)).Push(gtx.Ops)
	ctrl.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: image.Pt(w, h)}
}
