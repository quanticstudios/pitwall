package settings

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func hex(c config.Color) color.NRGBA {
	if c == "" {
		return color.NRGBA{A: 0xff}
	}
	return theme.Hex(string(c))
}

// themeCards is aide's ThemePicker: a grid of preview cards, the current
// theme ringed in the accent color. A click applies the theme.
func (p *Page) themeCards(gtx gl.Context) gl.Dimensions {
	w := gtx.Constraints.Max.X
	cols := themeCols(gtx, w)
	gap := gtx.Dp(12)
	cw := (w - gap*(cols-1)) / cols
	y, rowH := 0, 0
	for i, t := range p.themes {
		c := p.btn("theme:" + t.Name)
		for c.Clicked(gtx) {
			if t.Name != p.s.ThemeName {
				p.saveValue("theme", "name", config.Quote(t.Name))
			}
		}
		if i > 0 && i%cols == 0 {
			y, rowH = y+rowH+gap, 0
		}
		o := op.Offset(image.Pt((i%cols)*(cw+gap), y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Constraints{Min: image.Pt(cw, 0), Max: image.Pt(cw, gtx.Constraints.Max.Y)}
		d := c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			d := p.themeCard(gtx, t, cw, c.Hovered())
			if gtx.Focused(c) {
				kit.FocusRing(gtx, p.th, image.Rectangle{Max: d.Size}, gtx.Dp(8))
			}
			return d
		})
		o.Pop()
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

func (p *Page) themeCard(gtx gl.Context, t config.NamedTheme, w int, hover bool) gl.Dimensions {
	th := p.th
	pad := gtx.Dp(8)
	tw := w - 2*pad
	thH := tw * 10 / 16
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints.Max.X = tw
	name := p.text(g, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, t.Name)
	call := m.Stop()
	h := pad + thH + gtx.Dp(8) + name.Size.Y + pad
	sz := image.Pt(w, h)
	sel := t.Name == p.s.ThemeName
	ring, rw := th.Border, 1
	switch {
	case sel:
		ring, rw = th.Primary, gtx.Dp(2)
	case hover:
		ring = theme.Mix(th.Border, th.Fg, 0.25)
	}
	r := gtx.Dp(8)
	rrect(gtx, ring, image.Rectangle{Max: sz}, r)
	rrect(gtx, th.SurfaceSecondary, image.Rectangle{Max: sz}.Inset(rw), r-rw)

	o := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	thumbnail(gtx, t.Theme, image.Pt(tw, thH))
	o.Pop()
	o = op.Offset(image.Pt(pad, pad+thH+gtx.Dp(8))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	if t.Custom {
		m := op.Record(gtx.Ops)
		d := p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, "custom")
		c := m.Stop()
		o := op.Offset(image.Pt(w-pad-d.Size.X, pad+thH+gtx.Dp(8))).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
	}
	defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
	pointer.CursorPointer.Add(gtx.Ops)
	return gl.Dimensions{Size: sz}
}

// thumbnail is aide's ThemeThumbnail: a sidebar of tab rows with status
// dots beside a terminal pane with a few colored lines.
func thumbnail(gtx gl.Context, t config.Theme, sz image.Point) {
	k := t.Colors
	defer clip.UniformRRect(image.Rectangle{Max: sz}, gtx.Dp(6)).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, hex(k.Sidebar))
	u := float32(sz.Y) / 100 // layout in hundredths of the height
	px := func(v float32) int { return int(v*u + 0.5) }
	bar := func(x, y, w float32, c color.NRGBA) {
		rrect(gtx, c, image.Rect(px(x), px(y), px(x+w), px(y+5)), px(2.5))
	}
	side := float32(sz.X) * 0.3 / u
	// the selected tab's row
	rrect(gtx, hex(k.SurfaceSecondary), image.Rect(px(5), px(24), px(side-5), px(40)), px(4))
	for i, c := range []config.Color{k.Blue, k.Yellow, k.Green} {
		y := 12 + float32(i)*18
		rrect(gtx, hex(c), image.Rect(px(10), px(y), px(16), px(y+6)), px(3))
		bar(20, y+0.5, (side-30)*[]float32{0.8, 0.6, 0.7}[i], theme.Mix(hex(k.Sidebar), hex(k.Fg), []float32{0.5, 0.9, 0.5}[i]))
	}
	pane := image.Rect(px(side), px(6), sz.X-px(6), sz.Y-px(6))
	rrect(gtx, hex(t.Terminal.Background), pane, px(5))
	x0 := side + 8
	pw := float32(pane.Dx()) / u
	ansi := func(i int) color.NRGBA {
		if i < len(t.Terminal.ANSI) {
			return hex(t.Terminal.ANSI[i])
		}
		return hex(t.Terminal.Foreground)
	}
	fg := hex(t.Terminal.Foreground)
	bar(x0, 16, pw*0.15, ansi(2))
	bar(x0+pw*0.18, 16, pw*0.4, fg)
	bar(x0, 30, pw*0.6, theme.Mix(hex(t.Terminal.Background), fg, 0.6))
	bar(x0, 44, pw*0.25, ansi(4))
	bar(x0+pw*0.28, 44, pw*0.2, ansi(3))
	bar(x0, 58, pw*0.15, ansi(2))
	rrect(gtx, hex(t.Terminal.Cursor), image.Rect(px(x0+pw*0.18), px(57), px(x0+pw*0.18+4), px(64)), 0)
	// a card with the accent dot, like aide's
	card := image.Rect(pane.Max.X-px(36), pane.Max.Y-px(20), pane.Max.X-px(6), pane.Max.Y-px(6))
	rrect(gtx, hex(k.Surface), card, px(3))
	d := px(8)
	c := image.Pt(card.Max.X-px(5)-d, card.Min.Y+(card.Dy()-d)/2)
	rrect(gtx, hex(k.Primary), image.Rectangle{Min: c, Max: c.Add(image.Pt(d, d))}, d/2)
}

// fontPicker is a dropdown of installed families with a filter field.
func (p *Page) fontPicker(k, cur, def string) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		th := p.th
		id := "dd:" + k
		c := p.btn(id)
		for c.Clicked(gtx) {
			if p.dd == k {
				p.dd = ""
			} else {
				p.dd, p.ddFocus, p.ddList.Position, p.conflict, p.rec = k, true, gl.Position{}, nil, slot{}
				p.ddFilter.SetText("")
			}
		}
		for p.btn(id + "reset").Clicked(gtx) {
			p.save("font", k, nil)
		}
		return hstack(gtx, 4,
			p.resetSlot(id+"reset", !strings.EqualFold(cur, def)),
			func(gtx gl.Context) gl.Dimensions {
				w, h := gtx.Dp(240), gtx.Dp(32)
				d := c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					border := th.Border
					if c.Hovered() || p.dd == k {
						border = theme.Mix(th.Border, th.Fg, 0.25)
					}
					if gtx.Focused(c) {
						kit.FocusRing(gtx, th, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
					}
					rrect(gtx, border, image.Rect(0, 0, w, h), gtx.Dp(6))
					rrect(gtx, th.SurfaceElevated, image.Rect(1, 1, w-1, h-1), gtx.Dp(6)-1)
					g := gtx
					g.Constraints.Max.X = w - gtx.Dp(40)
					m := op.Record(gtx.Ops)
					td := p.text(g, th.UIFont, p.th.Sp(theme.Body), th.Fg, cur)
					call := m.Stop()
					o := op.Offset(image.Pt(gtx.Dp(10), (h-td.Size.Y)/2)).Push(gtx.Ops)
					call.Add(gtx.Ops)
					o.Pop()
					is := gtx.Dp(14)
					o = op.Offset(image.Pt(w-gtx.Dp(10)-is, (h-is)/2)).Push(gtx.Ops)
					icon(gtx, th.Muted, is, chevronGlyph)
					o.Pop()
					defer clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops).Pop()
					pointer.CursorPointer.Add(gtx.Ops)
					return gl.Dimensions{Size: image.Pt(w, h)}
				})
				if p.dd == k {
					p.popover(gtx, k, cur, def, w, h)
				}
				return d
			},
		)
	}
}

// popover draws the open dropdown over everything, below its trigger. A
// press outside it closes it.
func (p *Page) popover(gtx gl.Context, k, cur, def string, w, top int) {
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
		_, ok := gtx.Event(pointer.Filter{Target: &p.ddPanel, Kinds: pointer.Press})
		if !ok {
			break
		}
	}
	fams := families(nil)
	q := strings.ToLower(strings.TrimSpace(p.ddFilter.Text()))
	var shown []string
	for _, f := range fams {
		if strings.Contains(strings.ToLower(f), q) {
			shown = append(shown, f)
		}
	}
	pick := func(f string) {
		if strings.EqualFold(f, def) {
			p.save("font", k, nil)
		} else {
			p.saveValue("font", k, config.Quote(f))
		}
		p.dd = ""
	}
	p.ddFilter.SingleLine, p.ddFilter.Submit = true, true
	for {
		ev, ok := p.ddFilter.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			p.ddList.Position = gl.Position{}
		case widget.SubmitEvent:
			if len(shown) > 0 {
				pick(shown[0])
			}
		}
	}
	for i, f := range shown {
		if i > 400 {
			break
		}
		for p.btn("font:" + f).Clicked(gtx) {
			pick(f)
		}
	}
	if p.dd == "" {
		return
	}
	if p.ddFocus {
		p.ddFocus = false
		gtx.Execute(key.FocusCmd{Tag: &p.ddFilter})
	}

	m := op.Record(gtx.Ops)
	bg := clip.Rect{Min: image.Pt(-1<<14, -1<<14), Max: image.Pt(1<<14, 1<<14)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddTag)
	bg.Pop()
	itemH, pad := gtx.Dp(28), gtx.Dp(6)
	listH := min(gtx.Dp(280), max(1, len(shown))*itemH)
	panel := image.Rect(0, 0, w+gtx.Dp(40), pad+gtx.Dp(32)+pad+listH+pad)
	o := op.Offset(image.Pt(0, top+gtx.Dp(4))).Push(gtx.Ops)
	kit.Surface(gtx, panel, gtx.Dp(theme.RadiusPopover), kit.Floating, theme.Mix(th.SurfaceElevated, th.Fg, 0.1), th.SurfaceElevated)
	pa := clip.Rect(panel).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddPanel) // the panel takes presses the backdrop would
	pa.Pop()

	fo := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	fg := gtx
	fg.Constraints = gl.Exact(image.Pt(panel.Dx()-2*pad, gtx.Dp(32)))
	p.field(fg, &p.ddFilter, "Filter fonts", true)
	fo.Pop()

	lo := op.Offset(image.Pt(pad, pad+gtx.Dp(32)+pad)).Push(gtx.Ops)
	lg := gtx
	lg.Constraints = gl.Exact(image.Pt(panel.Dx()-2*pad, listH))
	cl := clip.Rect{Max: lg.Constraints.Max}.Push(gtx.Ops)
	switch {
	case fams == nil:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(150 * time.Millisecond)})
		gl.Center.Layout(lg, func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, "Reading installed fonts…")
		})
	case len(shown) == 0:
		gl.Center.Layout(lg, func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, "No installed font matches")
		})
	default:
		p.ddList.Axis = gl.Vertical
		p.ddList.Layout(lg, min(len(shown), 401), func(gtx gl.Context, i int) gl.Dimensions {
			f := shown[i]
			c := p.btn("font:" + f)
			return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				sz := image.Pt(gtx.Constraints.Max.X, itemH)
				if c.Hovered() {
					rrect(gtx, theme.Mix(th.SurfaceElevated, th.Fg, 0.06), image.Rectangle{Max: sz}, gtx.Dp(4))
				}
				g := gtx
				g.Constraints.Max.X = sz.X - gtx.Dp(36)
				mm := op.Record(gtx.Ops)
				d := p.text(g, th.UIFont, p.th.Sp(theme.Body), th.Fg, f)
				call := mm.Stop()
				oo := op.Offset(image.Pt(gtx.Dp(8), (itemH-d.Size.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				oo.Pop()
				if strings.EqualFold(f, cur) {
					is := gtx.Dp(14)
					oo := op.Offset(image.Pt(sz.X-gtx.Dp(8)-is, (itemH-is)/2)).Push(gtx.Ops)
					icon(gtx, th.Primary, is, checkGlyph)
					oo.Pop()
				}
				defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: sz}
			})
		})
	}
	cl.Pop()
	lo.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
