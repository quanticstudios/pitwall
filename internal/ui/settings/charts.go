package settings

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// charts is the hover state the Timing and Agent errors charts share.
// A focus is "<chart>/<part name>": the charts with that id pick the part
// out, the others are left alone.
type charts struct {
	hovers map[string]*gesture.Hover
	focus  string    // the part under the pointer last frame, which the charts pick out
	next   string    // the part under the pointer this frame
	frame  time.Time // the frame next is for
}

// share is one colored part of a chart: its name and value, the line
// under the name in a legend, and the lines of its tooltip.
type share struct {
	name   string
	col    color.NRGBA
	v      int64
	legend string
	tip    []string
}

// hovered reports whether the pointer is over r, tracked by id.
func (p *Page) hovered(gtx gl.Context, id string, r image.Rectangle) bool {
	if p.ch.hovers == nil {
		p.ch.hovers = map[string]*gesture.Hover{}
	}
	h := p.ch.hovers[id]
	if h == nil {
		h = new(gesture.Hover)
		p.ch.hovers[id] = h
	}
	st := clip.Rect(r).Push(gtx.Ops)
	h.Add(gtx.Ops)
	st.Pop()
	return h.Update(gtx.Source)
}

// focusFrame starts a frame's focus: the part hovered in the last one.
// Every chart that picks a part out calls it first, as the page's list
// lays out only the rows on screen.
func (p *Page) focusFrame(gtx gl.Context) {
	if !gtx.Now.Equal(p.ch.frame) {
		p.ch.frame, p.ch.focus, p.ch.next = gtx.Now, p.ch.next, ""
	}
}

// focusMoved asks for another frame when the hovered part changed, so the
// charts pick out the new one.
func (p *Page) focusMoved(gtx gl.Context) {
	if p.ch.next != p.ch.focus {
		gtx.Execute(op.InvalidateCmd{})
	}
}

// focused reports whether chart's part name has the focus.
func (p *Page) focused(chart, name string) bool { return p.ch.focus == chart+"/"+name }

// dim is c faded toward the card while another part of chart has the
// focus.
func (p *Page) dim(c color.NRGBA, chart, name string) color.NRGBA {
	if strings.HasPrefix(p.ch.focus, chart+"/") && !p.focused(chart, name) {
		return theme.Mix(p.th.SurfaceSecondary, c, 0.3)
	}
	return c
}

// tip draws a tooltip over everything, centred above anchor and kept
// inside the width w: its first line strong, the rest muted.
func (p *Page) tip(gtx gl.Context, anchor image.Rectangle, w int, lines ...string) {
	th := p.th
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = gl.Constraints{Max: image.Pt(gtx.Dp(280), gtx.Constraints.Max.Y)}
	var kids []gl.FlexChild
	for i, l := range lines {
		f, c, size := th.UIFont, th.Muted, p.th.Sp(theme.Small)
		if i == 0 {
			f, c = weight(f, font.Medium), th.Fg
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.text(gtx, f, size, c, l) }))
	}
	d := gl.Flex{Axis: gl.Vertical}.Layout(g, kids...)
	content := m.Stop()
	pad := image.Pt(gtx.Dp(10), gtx.Dp(7))
	size := d.Size.Add(pad.Mul(2))
	at := kit.Place(anchor, size, image.Rect(0, -1<<20, w, 1<<20), kit.Above, gtx.Dp(6), 0)
	m = op.Record(gtx.Ops)
	o := op.Offset(at).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: size}, gtx.Dp(6), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	o2 := op.Offset(pad).Push(gtx.Ops)
	content.Add(gtx.Ops)
	o2.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

// chartEmpty is a chart's card when it has nothing to draw.
func (p *Page) chartEmpty(gtx gl.Context, line string) gl.Dimensions {
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		return gl.Inset{Top: 8, Bottom: 8}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Body), p.th.Muted, line)
		})
	})
}

// splitBar is a card of one stacked bar of ss against total, with a
// legend in at most cols columns under it. Each share needs a value.
func (p *Page) splitBar(gtx gl.Context, chart string, ss []share, total int64, cols int) gl.Dimensions {
	p.focusFrame(gtx)
	defer p.focusMoved(gtx)
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		w := gtx.Constraints.Max.X
		bar := func(gtx gl.Context) gl.Dimensions {
			h, gap := gtx.Dp(28), gtx.Dp(2)
			type seg struct {
				s share
				r image.Rectangle
			}
			// Every part gets at least 2px, so a sliver stays visible.
			var segs []seg
			avail := w - gap*(len(ss)-1)
			x := 0
			for i, s := range ss {
				sw := max(gtx.Dp(2), int(float64(avail)*float64(s.v)/float64(total)+0.5))
				if i == len(ss)-1 {
					sw = max(1, w-x)
				}
				segs = append(segs, seg{s, image.Rect(x, 0, min(w, x+sw), h)})
				x += sw + gap
			}
			st := clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(4)).Push(gtx.Ops)
			for _, sg := range segs {
				paint.FillShape(gtx.Ops, p.dim(sg.s.col, chart, sg.s.name), clip.Rect(sg.r).Op())
			}
			st.Pop()
			for _, sg := range segs {
				if p.hovered(gtx, chart+"-split-"+sg.s.name, sg.r) {
					p.ch.next = chart + "/" + sg.s.name
					p.tip(gtx, sg.r, w, sg.s.tip...)
				}
			}
			return gl.Dimensions{Size: image.Pt(w, h)}
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(bar),
			gl.Rigid(gl.Spacer{Height: 16}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.legend(gtx, chart, ss, cols) }),
		)
	})
}

// legend is a grid of ss in at most cols columns: a dot, the name, then
// its legend line. Hovering one picks it out in chart.
func (p *Page) legend(gtx gl.Context, chart string, ss []share, cols int) gl.Dimensions {
	th := p.th
	w := gtx.Constraints.Max.X
	cols = max(1, min(cols, w/gtx.Dp(150)))
	gap := gtx.Dp(12)
	cw := (w - gap*(cols-1)) / cols
	y, rowH := 0, 0
	for i, s := range ss {
		if i > 0 && i%cols == 0 {
			y, rowH = y+rowH+gtx.Dp(10), 0
		}
		at := image.Pt((i%cols)*(cw+gap), y)
		o := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(cw, gtx.Constraints.Max.Y)}
		d := hstack(g, 8,
			func(gtx gl.Context) gl.Dimensions {
				return gl.Inset{Top: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions { return dot(gtx, p.dim(s.col, chart, s.name), gtx.Dp(8)) })
			},
			func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Fg, s.name)
					}),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, s.legend)
					}),
				)
			},
		)
		o.Pop()
		if p.hovered(gtx, chart+"-legend-"+s.name, image.Rectangle{Min: at, Max: at.Add(image.Pt(cw, d.Size.Y))}) {
			p.ch.next = chart + "/" + s.name
		}
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// dayChart is a bar per day, stacked by series from the base up.
type dayChart struct {
	id     string  // the chart's name for focus and hover
	series []share // the name and color of each part of a bar
	days   []time.Time
	vals   [][]int64 // vals[day][series]
	per    float64   // values per axis unit, such as 3.6e6 ms an hour
	step   float64   // the smallest step between gridlines, 0 for any
	label  func(v float64) string
	tip    func(day int) []string // the hovered day's tooltip
}

// dayBars draws c on gridlines, the first, middle and last day under it.
// Hovering a day shows its tooltip.
func (p *Page) dayBars(gtx gl.Context, c dayChart) gl.Dimensions {
	th := p.th
	sums := make([]int64, len(c.days))
	var peak int64
	for i, vs := range c.vals {
		for _, v := range vs {
			sums[i] += v
		}
		peak = max(peak, sums[i])
	}
	w, h := gtx.Constraints.Max.X, gtx.Dp(168)
	top, step := niceScale(float64(peak)/c.per, 4)
	if step < c.step {
		step = c.step
		top = math.Ceil(float64(peak)/c.per/step) * step
	}
	label := func(s string) (op.CallOp, image.Point) {
		m := op.Record(gtx.Ops)
		d := p.text(gtx, th.UIFont, p.th.Sp(theme.Caption), th.Muted, s)
		return m.Stop(), d.Size
	}
	type tick struct {
		v    float64
		call op.CallOp
		sz   image.Point
	}
	var ticks []tick
	gutter := 0
	for v := 0.0; v <= top+step/2; v += step {
		c, sz := label(c.label(v))
		ticks = append(ticks, tick{v, c, sz})
		gutter = max(gutter, sz.X)
	}
	gutter += gtx.Dp(8)
	_, lsz := label("0")
	plot := image.Rect(gutter, lsz.Y/2, w, h-lsz.Y-gtx.Dp(8))
	ys := func(v float64) int { return plot.Max.Y - int(v/top*float64(plot.Dy())+0.5) }
	grid := theme.Mix(th.SurfaceSecondary, th.Fg, 0.08)
	for _, t := range ticks {
		y := ys(t.v)
		paint.FillShape(gtx.Ops, grid, clip.Rect{Min: image.Pt(plot.Min.X, y), Max: image.Pt(plot.Max.X, y+1)}.Op())
		o := op.Offset(image.Pt(gutter-gtx.Dp(8)-t.sz.X, y-t.sz.Y/2)).Push(gtx.Ops)
		t.call.Add(gtx.Ops)
		o.Pop()
	}
	n := len(c.days)
	slot := float64(plot.Dx()) / float64(n)
	bw := max(1, min(gtx.Dp(28), int(slot*0.62)))
	gap := gtx.Dp(2)
	if bw < gtx.Dp(6) {
		gap = 0 // too thin to cut
	}
	for i := range c.days {
		x := plot.Min.X + int(float64(i)*slot+(slot-float64(bw))/2)
		col := image.Rect(plot.Min.X+int(float64(i)*slot), plot.Min.Y, plot.Min.X+int(float64(i+1)*slot), plot.Max.Y)
		hover := p.hovered(gtx, fmt.Sprint(c.id, "-day-", i), col)
		if hover {
			rrect(gtx, theme.Mix(th.SurfaceSecondary, th.Fg, 0.05), col, gtx.Dp(4))
		}
		a := sums[i]
		if a <= 0 {
			continue
		}
		barTop := ys(float64(a) / c.per)
		st := clip.RRect{Rect: image.Rect(x, barTop, x+bw, plot.Max.Y), NW: gtx.Dp(4), NE: gtx.Dp(4)}.Push(gtx.Ops)
		var acc int64
		for j, s := range c.series {
			v := c.vals[i][j]
			if v <= 0 {
				continue
			}
			y0, y1 := ys(float64(acc)/c.per), ys(float64(acc+v)/c.per)
			acc += v
			if acc < a {
				y1 += gap // a surface gap above all but the top part
			}
			if y1 < y0 {
				paint.FillShape(gtx.Ops, p.dim(s.col, c.id, s.name), clip.Rect{Min: image.Pt(x, y1), Max: image.Pt(x+bw, y0)}.Op())
			}
		}
		st.Pop()
		if hover {
			p.tip(gtx, image.Rect(x, barTop, x+bw, barTop), w, c.tip(i)...)
		}
	}
	for _, i := range slices.Compact([]int{0, n / 2, n - 1}) {
		c, sz := label(c.days[i].Format("Jan 2"))
		x := plot.Min.X + int((float64(i)+0.5)*slot) - sz.X/2
		x = max(plot.Min.X, min(x, plot.Max.X-sz.X))
		o := op.Offset(image.Pt(x, plot.Max.Y+gtx.Dp(8))).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
	}
	return gl.Dimensions{Size: image.Pt(w, h)}
}
