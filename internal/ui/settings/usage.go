package settings

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// usagePage is the Usage category's state.
type usagePage struct {
	days  int  // the window: 7, 30 or 90 days; 0 is 30
	byDay bool // Breakdown lists days rather than models
	gen   int  // Show counts opens: each one scans again

	mu       sync.Mutex
	scanning bool
	report   *flow.Report // nil before the first scan lands
	of       [2]int       // the gen and days report was scanned for
}

var (
	// usageScanner keeps what it read across opens, so a rescan reads only
	// what the agents appended since.
	usageScanner flow.Scanner
	// usageSources are the session directories; tests replace it.
	usageSources = func() []flow.Source {
		home, _ := os.UserHomeDir()
		return flow.Sources(home)
	}
)

func (u *usagePage) window() int {
	if u.days == 0 {
		return 30
	}
	return u.days
}

// shown is the report to draw: nil until a scan of the window lands.
func (u *usagePage) shown() *flow.Report {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.report == nil || len(u.report.Days) != u.window() {
		return nil
	}
	return u.report
}

// scan starts a scan in the background when the page was reopened or the
// window changed, and asks for frames until it lands.
func (p *Page) scan(gtx gl.Context) {
	u := &p.us
	u.mu.Lock()
	defer u.mu.Unlock()
	days, want := u.window(), [2]int{u.gen, u.window()}
	if !u.scanning && u.of != want {
		u.scanning = true
		go func() {
			now := time.Now()
			r := flow.NewReport(usageScanner.Scan(usageSources(), flow.Since(now, days)), now, days)
			u.mu.Lock()
			u.report, u.of, u.scanning = &r, want, false
			u.mu.Unlock()
		}()
	}
	if u.scanning {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
}

func (p *Page) usage() []section {
	u := &p.us
	r := u.shown()
	windows := []string{"7 days", "30 days", "90 days"}
	sections := []section{p.limits(), {title: "Cost", rows: []row{
		{label: "Window", desc: "What Claude Code, Codex and pi cost on this machine, read from their session files and priced at the API's list prices of " + flow.PricesAsOf + ". A subscription bills differently.",
			extra: "usage cost spend tokens price dollars models sessions",
			control: p.segmented("uwin", windows, fmt.Sprint(u.window(), " days"), func(o string) {
				fmt.Sscan(o, &u.days)
			})},
		{control: func(gtx gl.Context) gl.Dimensions { return p.overview(gtx, r) }},
	}}}
	if r != nil && r.Tokens.Total() > 0 {
		sections = append(sections,
			section{title: "Totals", rows: []row{{control: func(gtx gl.Context) gl.Dimensions { return p.totals(gtx, r) }}}},
			section{title: "Breakdown", rows: []row{{control: func(gtx gl.Context) gl.Dimensions { return p.breakdown(gtx, r) }}}},
		)
	}
	return sections
}

// overview is the total and each agent's share beside the daily cost
// chart, side by side when there is room.
func (p *Page) overview(gtx gl.Context, r *flow.Report) gl.Dimensions {
	th := p.th
	p.scan(gtx)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if r == nil || r.Tokens.Total() == 0 {
		msg := "Scanning…"
		if r != nil {
			msg = fmt.Sprintf("No Claude Code, Codex or pi sessions in the last %d days.", len(r.Days))
		}
		return gl.Inset{Top: 24, Bottom: 24}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, msg)
			})
		})
	}
	summary := func(gtx gl.Context) gl.Dimensions { return p.summary(gtx, r) }
	chart := func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, "Daily cost")
			}),
			gl.Rigid(gl.Spacer{Height: 12}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.chart(gtx, r) }),
		)
	}
	if gtx.Constraints.Max.X < gtx.Dp(560) {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, gl.Rigid(summary), gl.Rigid(gl.Spacer{Height: 24}.Layout), gl.Rigid(chart))
	}
	return gl.Flex{}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints = gl.Exact(image.Pt(gtx.Dp(220), gtx.Constraints.Max.Y))
			gtx.Constraints.Min.Y = 0
			return summary(gtx)
		}),
		gl.Rigid(gl.Spacer{Width: 32}.Layout),
		gl.Flexed(1, chart),
	)
}

// summary is the total cost, the sessions, then a line per agent.
func (p *Page) summary(gtx gl.Context, r *flow.Report) gl.Dimensions {
	th := p.th
	sub := plural(r.Sessions, "session")
	if r.Unpriced > 0 {
		sub += " · " + percent(float64(r.Unpriced)/float64(r.Tokens.Total())) + " of tokens unpriced"
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, weight(th.UIFont, font.SemiBold), p.sp(30), th.Fg, money(r.Cost))
		}),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, sub) }),
	}
	for _, pu := range r.Providers {
		share := 0.0
		if r.Cost > 0 {
			share = pu.Cost / r.Cost
		}
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 16}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return gl.Flex{Alignment: gl.Baseline}.Layout(gtx,
						gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
							return hstack(gtx, 6,
								func(gtx gl.Context) gl.Dimensions { return dot(gtx, sidebar.AgentColor(pu.Provider, th.Fg), gtx.Dp(8)) },
								func(gtx gl.Context) gl.Dimensions {
									return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Fg, agentName(pu.Provider))
								},
								func(gtx gl.Context) gl.Dimensions {
									return p.text(gtx, th.UIFont, p.th.Sp(theme.Caption), th.Muted, plural(pu.Sessions, "session"))
								},
							)
						}),
						gl.Rigid(func(gtx gl.Context) gl.Dimensions {
							return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, money(pu.Cost))
						}),
					)
				}),
				gl.Rigid(gl.Spacer{Height: 2}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, percent(share)+" of cost · "+compact(pu.Tokens.Total())+" tokens")
				}),
			)
		}))
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

func agentName(p model.Provider) string {
	if p == model.ProviderClaude {
		return "Claude Code"
	}
	return sidebar.AgentName(p)
}

func dot(gtx gl.Context, c color.NRGBA, d int) gl.Dimensions {
	rrect(gtx, c, image.Rect(0, 0, d, d), d/2)
	return gl.Dimensions{Size: image.Pt(d, d)}
}

// chart draws each agent's daily cost as a smoothed line over a faded
// area, each from zero, on dollar gridlines, with the first, middle and
// last day under it.
func (p *Page) chart(gtx gl.Context, r *flow.Report) gl.Dimensions {
	th := p.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(180)
	peak := 0.0
	for _, pu := range r.Providers {
		for _, c := range pu.Daily {
			peak = max(peak, c)
		}
	}
	top, step := niceScale(peak, 4)
	label := func(s string) (op.CallOp, image.Point) {
		m := op.Record(gtx.Ops)
		d := p.text(gtx, th.UIFont, p.th.Sp(theme.Caption), th.Muted, s)
		return m.Stop(), d.Size
	}
	// The y labels' column is as wide as the widest of them.
	type tick struct {
		y    float64
		call op.CallOp
		sz   image.Point
	}
	var ticks []tick
	gutter := 0
	for v := 0.0; v <= top+step/2; v += step {
		c, sz := label(axisMoney(v, step))
		ticks = append(ticks, tick{v, c, sz})
		gutter = max(gutter, sz.X)
	}
	gutter += gtx.Dp(8)
	_, lsz := label("0")
	plot := image.Rect(gutter, lsz.Y/2, w, h-lsz.Y-gtx.Dp(8))
	ys := func(v float64) float32 {
		return float32(plot.Max.Y) - float32(v/top)*float32(plot.Dy())
	}
	grid := theme.Mix(th.SurfaceSecondary, th.Fg, 0.08)
	for _, t := range ticks {
		y := int(ys(t.y))
		paint.FillShape(gtx.Ops, grid, clip.Rect{Min: image.Pt(plot.Min.X, y), Max: image.Pt(plot.Max.X, y+1)}.Op())
		o := op.Offset(image.Pt(gutter-gtx.Dp(8)-t.sz.X, y-t.sz.Y/2)).Push(gtx.Ops)
		t.call.Add(gtx.Ops)
		o.Pop()
	}
	n := len(r.Days)
	xs := func(i int) float32 {
		if n == 1 {
			return float32(plot.Min.X)
		}
		return float32(plot.Min.X) + float32(i)*float32(plot.Dx()-1)/float32(n-1)
	}
	for _, i := range []int{0, n / 2, n - 1} {
		c, sz := label(r.Days[i].Format("Jan 2"))
		x := int(xs(i)) - sz.X/2
		x = max(plot.Min.X, min(x, plot.Max.X-sz.X))
		o := op.Offset(image.Pt(x, plot.Max.Y+gtx.Dp(8))).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
	}
	// The biggest agent first, so smaller ones draw over it.
	for _, pu := range r.Providers {
		col := sidebar.AgentColor(pu.Provider, th.Fg)
		pts := make([]f32.Point, n)
		for i, c := range pu.Daily {
			pts[i] = f32.Pt(xs(i), ys(c))
		}
		curve := func(path *clip.Path) {
			path.MoveTo(pts[0])
			smooth(path, pts)
		}
		var area clip.Path
		area.Begin(gtx.Ops)
		curve(&area)
		area.LineTo(f32.Pt(pts[n-1].X, float32(plot.Max.Y)))
		area.LineTo(f32.Pt(pts[0].X, float32(plot.Max.Y)))
		area.Close()
		st := clip.Outline{Path: area.End()}.Op().Push(gtx.Ops)
		faded := col
		faded.A = 0x48
		clear := col
		clear.A = 0
		paint.LinearGradientOp{Stop1: f32.Pt(0, float32(plot.Min.Y)), Color1: faded, Stop2: f32.Pt(0, float32(plot.Max.Y)), Color2: clear}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		st.Pop()
		var line clip.Path
		line.Begin(gtx.Ops)
		curve(&line)
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: line.End(), Width: float32(gtx.Dp(2))}.Op())
	}
	return gl.Dimensions{Size: image.Pt(w, h)}
}

// Adapted from t3code (MIT): apps/web/src/components/usage/UsageProviderChart.tsx

// niceScale is the chart's top, a 1, 2 or 5 times 10^n step at or above
// peak, and the step between count gridlines.
func niceScale(peak float64, count int) (top, step float64) {
	if peak <= 0 {
		return 1, 1
	}
	raw := peak / float64(count)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	switch n := raw / mag; {
	case n > 5:
		step = 10 * mag
	case n > 2:
		step = 5 * mag
	case n > 1:
		step = 2 * mag
	default:
		step = mag
	}
	return math.Ceil(peak/step) * step, step
}

// smooth adds a monotone cubic (Fritsch and Carlson) through pts to path,
// which is at pts[0]: it never overshoots a point, so a day of zero cost
// stays on the axis.
func smooth(path *clip.Path, pts []f32.Point) {
	n := len(pts)
	if n < 2 {
		return
	}
	d := make([]float32, n-1) // each segment's slope
	for i := range d {
		d[i] = (pts[i+1].Y - pts[i].Y) / (pts[i+1].X - pts[i].X)
	}
	m := make([]float32, n) // the tangent at each point
	m[0], m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] > 0 {
			m[i] = (d[i-1] + d[i]) / 2
		}
	}
	for i, s := range d {
		if s == 0 {
			m[i], m[i+1] = 0, 0
			continue
		}
		a, b := m[i]/s, m[i+1]/s
		if q := a*a + b*b; q > 9 {
			t := 3 / float32(math.Sqrt(float64(q)))
			m[i], m[i+1] = t*a*s, t*b*s
		}
	}
	for i := range d {
		dx := (pts[i+1].X - pts[i].X) / 3
		path.CubeTo(f32.Pt(pts[i].X+dx, pts[i].Y+m[i]*dx), f32.Pt(pts[i+1].X-dx, pts[i+1].Y-m[i+1]*dx), pts[i+1])
	}
}

// totals is a row of figures for the whole window.
func (p *Page) totals(gtx gl.Context, r *flow.Report) gl.Dimensions {
	th := p.th
	t := r.Tokens
	cells := []struct{ k, v string }{
		{"Processed tokens", compact(t.Total())},
		{"Cached input", compact(t.CacheRead)},
		{"Uncached input", compact(t.Input + t.CacheWrite)},
		{"Output", compact(t.Output)},
		{"Cache savings", money(r.Savings)},
	}
	cols := len(cells)
	if gtx.Constraints.Max.X < gtx.Dp(560) {
		cols = 3
	}
	w := gtx.Constraints.Max.X
	cw := w / cols
	y, rowH := 0, 0
	for i, c := range cells {
		if i > 0 && i%cols == 0 {
			y, rowH = y+rowH+gtx.Dp(16), 0
		}
		o := op.Offset(image.Pt((i%cols)*cw, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(cw-gtx.Dp(8), gtx.Constraints.Max.Y)}
		d := gl.Flex{Axis: gl.Vertical}.Layout(g,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, c.k) }),
			gl.Rigid(gl.Spacer{Height: 4}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, weight(th.UIFont, font.Medium), p.sp(18), th.Fg, c.v)
			}),
		)
		o.Pop()
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// breakdown is a table of cost by model, or by day with the latest first.
func (p *Page) breakdown(gtx gl.Context, r *flow.Report) gl.Dimensions {
	th := p.th
	u := &p.us
	cur := "Model"
	if u.byDay {
		cur = "Day"
	}
	type line struct {
		mark      model.Provider // "" for none
		name      string
		cost      string
		share     float64 // -1 when unpriced
		tokens    int64
		dim, last bool
	}
	share := func(c float64) float64 {
		if r.Cost == 0 {
			return 0
		}
		return c / r.Cost
	}
	var lines []line
	if u.byDay {
		for i := len(r.Days) - 1; i >= 0; i-- {
			d := r.Daily[i]
			lines = append(lines, line{name: r.Days[i].Format("Mon, Jan 2"), cost: money(d.Cost), share: share(d.Cost), tokens: d.Tokens.Total(), dim: d.Tokens.Total() == 0})
		}
	} else {
		for _, m := range r.Models {
			l := line{mark: m.Provider, name: m.Model, cost: money(m.Cost), share: share(m.Cost), tokens: m.Tokens.Total()}
			if !m.Priced {
				l.cost, l.share = "Unpriced", -1
			}
			lines = append(lines, l)
		}
	}
	// The bars measure against the biggest row, so small days still show.
	top := 0.0
	for _, l := range lines {
		top = max(top, l.share)
	}
	costW, shareW, tokW := gtx.Dp(96), gtx.Dp(104), gtx.Dp(72)
	cell := func(gtx gl.Context, x, w int, c color.NRGBA, s string, f font.Font) {
		m := op.Record(gtx.Ops)
		d := p.text(gtx, f, p.th.Sp(theme.Body), c, s)
		call := m.Stop()
		o := op.Offset(image.Pt(x+w-d.Size.X, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
	}
	tableRow := func(gtx gl.Context, head bool, l line) gl.Dimensions {
		w := gtx.Constraints.Max.X
		h := gtx.Dp(32)
		if head {
			h = gtx.Dp(24)
		}
		fg, muted, f := th.Fg, th.Muted, th.UIFont
		if l.dim {
			fg = th.Muted
		}
		if head {
			fg = th.Muted
		}
		m := op.Record(gtx.Ops)
		x := 0
		if l.mark != "" {
			is := gtx.Dp(14)
			o := op.Offset(image.Pt(0, (gtx.Sp(p.th.Sp(theme.Body)*1.2)-is)/2)).Push(gtx.Ops)
			sidebar.AgentMark(gtx, l.mark, is, th.Fg)
			o.Pop()
			x = is + gtx.Dp(8)
		}
		nameW := w - costW - shareW - tokW - x
		g := gtx
		g.Constraints.Max.X = nameW - gtx.Dp(8)
		o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		p.text(g, f, p.th.Sp(theme.Body), fg, l.name)
		o.Pop()
		x = w - costW - shareW - tokW
		costFg := fg
		if l.share < 0 {
			costFg = muted
		}
		cell(gtx, x, costW, costFg, l.cost, f)
		switch {
		case head:
			cell(gtx, x+costW, shareW, fg, "Share", f)
		case l.share < 0:
			cell(gtx, x+costW, shareW, muted, "—", f)
		default:
			s := percent(l.share)
			m := op.Record(gtx.Ops)
			d := p.text(gtx, f, p.th.Sp(theme.Body), fg, s)
			call := m.Stop()
			bw, bh := gtx.Dp(32), gtx.Dp(4)
			sx := x + costW + shareW - d.Size.X - gtx.Dp(8) - bw
			by := (d.Size.Y - bh) / 2
			rrect(gtx, theme.Mix(th.SurfaceSecondary, th.Fg, 0.1), image.Rect(sx, by, sx+bw, by+bh), bh/2)
			if fw := int(float64(bw) * l.share / max(top, 1e-9)); fw > 0 {
				col := th.Primary
				if l.mark != "" {
					col = sidebar.AgentColor(l.mark, th.Fg)
				}
				rrect(gtx, col, image.Rect(sx, by, sx+max(fw, bh), by+bh), bh/2)
			}
			o := op.Offset(image.Pt(x+costW+shareW-d.Size.X, 0)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
		}
		tok := compact(l.tokens)
		if head {
			tok = "Tokens"
		}
		cell(gtx, x+costW+shareW, tokW, fg, tok, f)
		call := m.Stop()
		lh := gtx.Sp(p.th.Sp(theme.Body) * 1.2)
		o = op.Offset(image.Pt(0, (h-lh)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		if !l.last {
			paint.FillShape(gtx.Ops, p.th.Border, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(w, h)}.Op())
		}
		return gl.Dimensions{Size: image.Pt(w, h)}
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = 0
			return p.segmented("ubreak", []string{"Model", "Day"}, cur, func(o string) { u.byDay = o == "Day" })(gtx)
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return tableRow(gtx, true, line{name: cur, cost: "Cost"})
		}),
	}
	for i, l := range lines {
		l.last = i == len(lines)-1
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return tableRow(gtx, false, l) }))
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// money is a cost in dollars and cents: "$16,624.19".
func money(c float64) string {
	s := fmt.Sprintf("%.2f", c)
	return "$" + thousands(s[:len(s)-3]) + s[len(s)-3:]
}

// axisMoney is a gridline's label: whole dollars unless the step is less.
func axisMoney(v, step float64) string {
	if v == 0 {
		return "$0"
	}
	if step < 1 {
		return money(v)
	}
	return "$" + thousands(fmt.Sprintf("%.0f", v))
}

// thousands puts commas in a run of digits.
func thousands(n string) string {
	for i := len(n) - 3; i > 0; i -= 3 {
		n = n[:i] + "," + n[i:]
	}
	return n
}

// compact is a count in three figures: "950", "35.8K", "220M", "7.99B".
func compact(n int64) string {
	v := float64(n)
	for _, u := range []string{"", "K", "M", "B", "T"} {
		if u != "" {
			v /= 1000
		}
		var s string
		switch {
		case u == "" && v < 1000:
			return fmt.Sprint(n)
		case v < 9.995:
			s = fmt.Sprintf("%.2f", v)
		case v < 99.95:
			s = fmt.Sprintf("%.1f", v)
		case v < 999.5:
			s = fmt.Sprintf("%.0f", v)
		default:
			continue
		}
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s + u
	}
	return fmt.Sprintf("%.0fT", v)
}

// percent is a share to one decimal: "30.1%", "<0.1%".
func percent(f float64) string {
	if f > 0 && f < 0.0005 {
		return "<0.1%"
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

func plural(n int, what string) string {
	if n != 1 {
		what += "s"
	}
	return thousands(fmt.Sprint(n)) + " " + what
}
