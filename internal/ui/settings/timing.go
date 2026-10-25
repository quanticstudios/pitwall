package settings

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/timelog"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// timingPage is the Timing category's state: timing.jsonl over the
// chosen window, summarised off the UI goroutine.
type timingPage struct {
	path   string // timing.jsonl; tests set it
	window string // week, month or ever; "" is week
	read   bool   // a read has started since the page opened

	mu      sync.Mutex
	gen     int // the latest read; an older one's result is dropped
	loading bool
	sum     *timelog.Summary
	any     bool // the log has a turn at all
	err     string

	hovers map[string]*gesture.Hover
	focus  string    // the part under the pointer last frame, which the charts pick out
	next   string    // the part under the pointer this frame
	frame  time.Time // the frame next is for
}

// Lines the category shows instead of charts, and how many sinks and
// tabs it lists and copies.
const (
	timingEmpty   = "Appears after an agent with pitwall's hooks finishes its first turn."
	timingLoading = "Reading the timing log…"
	topSinks      = 10
	topTabs       = 10
)

// readTiming reads timing.jsonl; tests replace it.
var readTiming = timelog.Read

func (p *Page) timingPath() string {
	if p.tm.path == "" {
		p.tm.path = filepath.Join(config.StateDir(), "timing.jsonl")
	}
	return p.tm.path
}

// readTimingLog reads the log and summarises the window in the
// background. Frames poll for the result while it runs.
func (p *Page) readTimingLog() {
	p.tm.read = true
	win, path := p.tm.window, p.timingPath()
	p.tm.mu.Lock()
	p.tm.gen++
	gen := p.tm.gen
	p.tm.loading = true
	p.tm.mu.Unlock()
	go func() {
		evs, err := readTiming(path, time.Time{})
		s, any := timingWindow(evs, win, time.Now())
		p.tm.mu.Lock()
		if gen == p.tm.gen {
			p.tm.sum, p.tm.any, p.tm.loading, p.tm.err = &s, any, false, ""
			if err != nil {
				p.tm.err = err.Error()
			}
		}
		p.tm.mu.Unlock()
	}()
}

// timingWindow is the summary of win up to now, and whether evs, oldest
// first, hold a turn at all.
func timingWindow(evs []timelog.Event, win string, now time.Time) (timelog.Summary, bool) {
	i := slices.IndexFunc(evs, func(e timelog.Event) bool { return e.Kind == timelog.Turn })
	first := now
	if i >= 0 {
		first = evs[i].T
	}
	return timelog.Summarize(evs, windowFrom(win, now, first), now, time.Local), i >= 0
}

// timingNote is the line shown instead of charts, "" when there are some.
func timingNote(s *timelog.Summary, any, loading bool, win string) string {
	switch {
	case s == nil && loading:
		return timingLoading
	case s == nil || !any:
		return timingEmpty
	case s.Turns == 0:
		return "No turns in the last " + win + "."
	}
	return ""
}

// part is what the charts draw as one color: a kind of time or several.
// The hues come from the theme, in an order whose neighbors stay apart
// for color-blind readers in every built-in theme; waiting on you is
// gray, as it is not the agent's time.
type part struct {
	name  string
	kinds []string
	col   color.NRGBA
}

func (p *Page) timeParts() []part {
	th := p.th
	return []part{
		{"Model", []string{timelog.Model}, th.Blue},
		{"Tests", []string{timelog.Test}, theme.Mix(th.Red, th.Yellow, 0.5)},
		{"Builds", []string{timelog.Build}, th.Purple},
		{"CI and gh", []string{timelog.CI}, th.Green},
		{"Other commands", []string{timelog.Lint, timelog.Git, timelog.Install, timelog.Shell}, theme.Mix(th.Red, th.Purple, 0.5)},
		{"Files", []string{timelog.Search, timelog.Edit}, th.Yellow},
		{"Other tools", []string{timelog.Agent, timelog.Web, timelog.MCP, timelog.Tool}, theme.Mix(th.Green, th.Blue, 0.6)},
		{"Waiting on you", []string{timelog.Wait}, theme.Mix(th.SurfaceSecondary, th.Muted, 0.75)},
	}
}

// of is pt's share of parts.
func (pt part) of(parts map[string]int64) int64 {
	var n int64
	for _, k := range pt.kinds {
		n += parts[k]
	}
	return n
}

// partOf is the part kind belongs to.
func (p *Page) partOf(kind string) part {
	ps := p.timeParts()
	if i := slices.IndexFunc(ps, func(pt part) bool { return slices.Contains(pt.kinds, kind) }); i >= 0 {
		return ps[i]
	}
	return ps[len(ps)-2]
}

// hovered reports whether the pointer is over r, tracked by id.
func (p *Page) hovered(gtx gl.Context, id string, r image.Rectangle) bool {
	if p.tm.hovers == nil {
		p.tm.hovers = map[string]*gesture.Hover{}
	}
	h := p.tm.hovers[id]
	if h == nil {
		h = new(gesture.Hover)
		p.tm.hovers[id] = h
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
	if !gtx.Now.Equal(p.tm.frame) {
		p.tm.frame, p.tm.focus, p.tm.next = gtx.Now, p.tm.next, ""
	}
}

// focusMoved asks for another frame when the hovered part changed, so the
// charts pick out the new one.
func (p *Page) focusMoved(gtx gl.Context) {
	if p.tm.next != p.tm.focus {
		gtx.Execute(op.InvalidateCmd{})
	}
}

// dim is c faded toward the card while another part has the focus.
func (p *Page) dim(c color.NRGBA, name string) color.NRGBA {
	if p.tm.focus != "" && p.tm.focus != name {
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

func (p *Page) timing() []section {
	th := p.th
	p.tm.mu.Lock()
	s, any, loading, err := p.tm.sum, p.tm.any, p.tm.loading || !p.tm.read, p.tm.err
	p.tm.mu.Unlock()
	win := cmp.Or(p.tm.window, week)
	note := timingNote(s, any, loading, win)

	head := func(gtx gl.Context) gl.Dimensions {
		if !p.tm.read {
			p.readTimingLog()
		}
		if note == timingLoading || note == timingEmpty {
			d := p.blank(gtx, note == timingLoading, "zap", note, nil)
			p.pollTiming(gtx)
			return d
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		text := func(gtx gl.Context) gl.Dimensions {
			if note != "" {
				d := gl.Inset{Top: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					return p.para(gtx, th.UIFont, p.th.Sp(theme.Large), th.Muted, note)
				})
				d.Size.X = gtx.Constraints.Max.X // keeps the window picker on the right
				return d
			}
			return p.timingHead(gtx, s, win)
		}
		seg := p.segmented("timing-window", []string{week, month, ever}, win, func(o string) {
			p.tm.window = o
			p.readTimingLog()
		})
		pick := func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = 0
			return seg(gtx)
		}
		kids := []gl.FlexChild{gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			if gtx.Constraints.Max.X < gtx.Dp(480) {
				// Too narrow to share a row: the picker goes above the headline.
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx, gl.Rigid(pick), gl.Rigid(gl.Spacer{Height: 16}.Layout), gl.Rigid(text))
			}
			return gl.Flex{Alignment: gl.Start}.Layout(gtx,
				gl.Flexed(1, text),
				gl.Rigid(gl.Spacer{Width: 16}.Layout),
				gl.Rigid(pick),
			)
		})}
		if err != "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Red, err)
			}))
		}
		if note == "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 24}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.timingTiles(gtx, s) }))
		}
		d := gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
		p.pollTiming(gtx)
		return d
	}
	secs := []section{{rows: []row{{bare: true, control: head}}}}
	if note != "" {
		return secs
	}
	return append(secs,
		section{title: "Where the time goes",
			desc: "Each turn runs from your prompt to the agent stopping. Tool time comes from the hooks around each call, counted once when calls run in parallel; model time is the rest. Hover a part for its hours.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.split(gtx, s) }}}},
		section{title: "Agent hours per day",
			desc: "The agents' time in turns that ended each day, waiting on you left out.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.dayHours(gtx, s) }}}},
		section{title: "Biggest time sinks",
			desc: "Commands by their total time, with numbers, paths and quoted text set aside. Each turn keeps its five slowest commands of a second or more, redacted.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.sinks(gtx, s) }}}},
		section{title: "Per task",
			desc: "Recent tabs: the agents' time, the part of it in tools, your time in approvals and replies, and how long each step to a merged pull request took.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.taskList(gtx, s) }}}},
	)
}

// timingHead is the headline, the biggest kind of tool time, then the
// model's and your share, and the Copy report button.
func (p *Page) timingHead(gtx gl.Context, s *timelog.Summary, win string) gl.Dimensions {
	th := p.th
	total := s.Agent + s.Wait
	head := "No tool time in these turns"
	for _, k := range s.ByTime() {
		if k != timelog.Model && k != timelog.Wait {
			head = timelog.Label(k) + " took " + timelog.Pct(s.Parts[k], total) + " of turn time"
			break
		}
	}
	sub := fmt.Sprintf("The model took %s and waiting on you %s, over %s.",
		timelog.Pct(s.Parts[timelog.Model], total), timelog.Pct(s.Wait, total), plural(s.Turns, "turn"))
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, weight(th.UIFont, font.SemiBold), p.sp(24), th.Fg, head)
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, sub)
		}),
		gl.Rigid(gl.Spacer{Height: 14}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			c := p.btn("timing-copy")
			for c.Clicked(gtx) {
				p.copy(gtx, "timing-copy", timelog.Report(*s, topSinks, span(win)))
			}
			return p.button(gtx, c, secondary, p.copyLabel("timing-copy", "Copy report"))
		}),
	)
}

// timingTiles are the turns, their median and p90, the agents' time and
// the time they waited on you.
func (p *Page) timingTiles(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	reply := "no replies yet"
	if s.Replies > 0 {
		reply = "median reply " + timelog.Dur(s.Reply)
	}
	return p.tileGrid(gtx, []tile{
		{"Turns", count(s.Turns), fmt.Sprintf("in %s", plural(len(s.Tabs), "tab"))},
		{"Turn time", timelog.Dur(s.Median), "median; p90 " + timelog.Dur(s.P90)},
		{"Agent time", timelog.Hours(s.Agent), timelog.Pct(s.Agent, s.Agent+s.Wait) + " of turn time"},
		{"Waiting on you", timelog.Hours(s.Wait), reply},
	})
}

// chartEmpty is a chart's card when it has nothing to draw.
func (p *Page) chartEmpty(gtx gl.Context, line string) gl.Dimensions {
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		return gl.Inset{Top: 8, Bottom: 8}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Body), p.th.Muted, line)
		})
	})
}

// split is one stacked bar of the window's turn time by part, with a
// legend of each part's share and hours.
func (p *Page) split(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	total := s.Agent + s.Wait
	var ps []part
	for _, pt := range p.timeParts() {
		if pt.of(s.Parts) > 0 {
			ps = append(ps, pt)
		}
	}
	if total <= 0 || len(ps) == 0 {
		return p.chartEmpty(gtx, "The turns in this window took no measurable time.")
	}
	p.focusFrame(gtx)
	defer p.focusMoved(gtx)
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		w := gtx.Constraints.Max.X
		bar := func(gtx gl.Context) gl.Dimensions {
			h, gap := gtx.Dp(28), gtx.Dp(2)
			type seg struct {
				pt   part
				r    image.Rectangle
				part int64
			}
			// Every part gets at least 2px, so a sliver stays visible.
			var segs []seg
			avail := w - gap*(len(ps)-1)
			x := 0
			for i, pt := range ps {
				v := pt.of(s.Parts)
				sw := max(gtx.Dp(2), int(float64(avail)*float64(v)/float64(total)+0.5))
				if i == len(ps)-1 {
					sw = max(1, w-x)
				}
				segs = append(segs, seg{pt, image.Rect(x, 0, min(w, x+sw), h), v})
				x += sw + gap
			}
			st := clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(4)).Push(gtx.Ops)
			for _, sg := range segs {
				paint.FillShape(gtx.Ops, p.dim(sg.pt.col, sg.pt.name), clip.Rect(sg.r).Op())
			}
			st.Pop()
			for _, sg := range segs {
				if p.hovered(gtx, "split-"+sg.pt.name, sg.r) {
					p.tm.next = sg.pt.name
					lines := []string{sg.pt.name, timelog.Hours(sg.part) + " · " + timelog.Pct(sg.part, total) + " of turn time"}
					if len(sg.pt.kinds) > 1 {
						for _, k := range sg.pt.kinds {
							if v := s.Parts[k]; v > 0 {
								lines = append(lines, timelog.Label(k)+" "+timelog.Hours(v))
							}
						}
					}
					p.tip(gtx, sg.r, w, lines...)
				}
			}
			return gl.Dimensions{Size: image.Pt(w, h)}
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(bar),
			gl.Rigid(gl.Spacer{Height: 16}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.legend(gtx, ps, s.Parts, total) }),
		)
	})
}

// legend is a grid of the parts: a dot, the name, then its share and
// hours. Hovering one picks it out in the charts.
func (p *Page) legend(gtx gl.Context, ps []part, parts map[string]int64, total int64) gl.Dimensions {
	th := p.th
	w := gtx.Constraints.Max.X
	cols := max(1, min(4, w/gtx.Dp(150)))
	gap := gtx.Dp(12)
	cw := (w - gap*(cols-1)) / cols
	y, rowH := 0, 0
	for i, pt := range ps {
		if i > 0 && i%cols == 0 {
			y, rowH = y+rowH+gtx.Dp(10), 0
		}
		at := image.Pt((i%cols)*(cw+gap), y)
		o := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(cw, gtx.Constraints.Max.Y)}
		v := pt.of(parts)
		d := hstack(g, 8,
			func(gtx gl.Context) gl.Dimensions {
				return gl.Inset{Top: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions { return dot(gtx, p.dim(pt.col, pt.name), gtx.Dp(8)) })
			},
			func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Fg, pt.name)
					}),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, timelog.Pct(v, total)+" · "+timelog.Hours(v))
					}),
				)
			},
		)
		o.Pop()
		if p.hovered(gtx, "legend-"+pt.name, image.Rectangle{Min: at, Max: at.Add(image.Pt(cw, d.Size.Y))}) {
			p.tm.next = pt.name
		}
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// dayHours is a bar per day of the agents' hours, stacked by part with
// the model at the base, on hour gridlines, the first, middle and last
// day under it. Hovering a day shows its hours.
func (p *Page) dayHours(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	th := p.th
	ps := p.timeParts()
	ps = ps[:len(ps)-1] // agent time: no waiting on you
	agent := func(d timelog.Day) int64 { return d.Total - d.Parts[timelog.Wait] }
	var peak int64
	for _, d := range s.Days {
		peak = max(peak, agent(d))
	}
	if peak <= 0 {
		return p.chartEmpty(gtx, "No agent time in this window.")
	}
	p.focusFrame(gtx)
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		w, h := gtx.Constraints.Max.X, gtx.Dp(168)
		top, step := niceScale(float64(peak)/3.6e6, 4)
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
			c, sz := label(strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "h")
			ticks = append(ticks, tick{v, c, sz})
			gutter = max(gutter, sz.X)
		}
		gutter += gtx.Dp(8)
		_, lsz := label("0")
		plot := image.Rect(gutter, lsz.Y/2, w, h-lsz.Y-gtx.Dp(8))
		ys := func(hours float64) int { return plot.Max.Y - int(hours/top*float64(plot.Dy())+0.5) }
		grid := theme.Mix(th.SurfaceSecondary, th.Fg, 0.08)
		for _, t := range ticks {
			y := ys(t.v)
			paint.FillShape(gtx.Ops, grid, clip.Rect{Min: image.Pt(plot.Min.X, y), Max: image.Pt(plot.Max.X, y+1)}.Op())
			o := op.Offset(image.Pt(gutter-gtx.Dp(8)-t.sz.X, y-t.sz.Y/2)).Push(gtx.Ops)
			t.call.Add(gtx.Ops)
			o.Pop()
		}
		n := len(s.Days)
		slot := float64(plot.Dx()) / float64(n)
		bw := max(1, min(gtx.Dp(28), int(slot*0.62)))
		gap := gtx.Dp(2)
		if bw < gtx.Dp(6) {
			gap = 0 // too thin to cut
		}
		for i, d := range s.Days {
			x := plot.Min.X + int(float64(i)*slot+(slot-float64(bw))/2)
			col := image.Rect(plot.Min.X+int(float64(i)*slot), plot.Min.Y, plot.Min.X+int(float64(i+1)*slot), plot.Max.Y)
			hover := p.hovered(gtx, fmt.Sprint("day-", i), col)
			if hover {
				rrect(gtx, theme.Mix(th.SurfaceSecondary, th.Fg, 0.05), col, gtx.Dp(4))
			}
			a := agent(d)
			if a <= 0 {
				continue
			}
			barTop := ys(float64(a) / 3.6e6)
			st := clip.RRect{Rect: image.Rect(x, barTop, x+bw, plot.Max.Y), NW: gtx.Dp(4), NE: gtx.Dp(4)}.Push(gtx.Ops)
			var acc int64
			for _, pt := range ps {
				v := pt.of(d.Parts)
				if v <= 0 {
					continue
				}
				y0, y1 := ys(float64(acc)/3.6e6), ys(float64(acc+v)/3.6e6)
				acc += v
				if acc < a {
					y1 += gap // a surface gap above all but the top part
				}
				if y1 < y0 {
					paint.FillShape(gtx.Ops, p.dim(pt.col, pt.name), clip.Rect{Min: image.Pt(x, y1), Max: image.Pt(x+bw, y0)}.Op())
				}
			}
			st.Pop()
			if hover {
				lines := []string{d.Date.Format("Mon Jan 2") + " · " + timelog.Hours(a)}
				type pv struct {
					name string
					v    int64
				}
				var top []pv
				for _, pt := range ps {
					if v := pt.of(d.Parts); v > 0 {
						top = append(top, pv{pt.name, v})
					}
				}
				slices.SortStableFunc(top, func(a, b pv) int { return cmp.Compare(b.v, a.v) })
				for _, t := range top[:min(4, len(top))] {
					lines = append(lines, t.name+" "+timelog.Hours(t.v)+" · "+timelog.Pct(t.v, a))
				}
				p.tip(gtx, image.Rect(x, barTop, x+bw, barTop), w, lines...)
			}
		}
		for _, i := range slices.Compact([]int{0, n / 2, n - 1}) {
			c, sz := label(s.Days[i].Date.Format("Jan 2"))
			x := plot.Min.X + int((float64(i)+0.5)*slot) - sz.X/2
			x = max(plot.Min.X, min(x, plot.Max.X-sz.X))
			o := op.Offset(image.Pt(x, plot.Max.Y+gtx.Dp(8))).Push(gtx.Ops)
			c.Add(gtx.Ops)
			o.Pop()
		}
		return gl.Dimensions{Size: image.Pt(w, h)}
	})
}

// sinks is the slowest commands, ranked by total time, each with its
// runs and median and a bar of its total against the biggest.
func (p *Page) sinks(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	th := p.th
	if len(s.Sinks) == 0 {
		return p.chartEmpty(gtx, "No command ran for a second or more in this window.")
	}
	ks := s.Sinks[:min(topSinks, len(s.Sinks))]
	peak := ks[0].Total
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		var kids []gl.FlexChild
		for i, k := range ks {
			pt := p.partOf(k.Kind)
			if i > 0 {
				kids = append(kids, gl.Rigid(gl.Spacer{Height: 14}.Layout))
			}
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				w := gtx.Constraints.Max.X
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						gtx.Constraints.Min.X = w
						return gl.Flex{Alignment: gl.Baseline}.Layout(gtx,
							gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
								d := p.text(gtx, th.MonoFont, p.th.Sp(theme.Small), th.Fg, k.Command)
								d.Size.X = gtx.Constraints.Max.X
								return d
							}),
							gl.Rigid(gl.Spacer{Width: 12}.Layout),
							gl.Rigid(func(gtx gl.Context) gl.Dimensions {
								return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, timelog.Dur(k.Total))
							}),
						)
					}),
					gl.Rigid(gl.Spacer{Height: 6}.Layout),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						h := gtx.Dp(6)
						rrect(gtx, theme.Mix(th.SurfaceSecondary, th.Fg, 0.07), image.Rect(0, 0, w, h), h/2)
						fill := max(h, int(float64(w)*float64(k.Total)/float64(peak)+0.5))
						rrect(gtx, pt.col, image.Rect(0, 0, fill, h), h/2)
						return gl.Dimensions{Size: image.Pt(w, h)}
					}),
					gl.Rigid(gl.Spacer{Height: 6}.Layout),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						meta := fmt.Sprintf("%s · ×%s · median %s", timelog.Label(k.Kind), count(k.Count), timelog.Dur(k.Median))
						return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, meta)
					}),
				)
			}))
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
	})
}

// taskList is the recent tabs: the agents', the tools' and your time,
// and the steps from the first prompt to a merged pull request.
func (p *Page) taskList(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	th := p.th
	if len(s.Tabs) == 0 {
		return p.chartEmpty(gtx, "No turns in a tab in this window.")
	}
	tabs := s.Tabs[:min(topTabs, len(s.Tabs))]
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		var kids []gl.FlexChild
		for i, t := range tabs {
			if i > 0 {
				kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					w := gtx.Constraints.Max.X
					y := gtx.Dp(12)
					paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
					return gl.Dimensions{Size: image.Pt(w, 2*y+1)}
				}))
			}
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.task(gtx, t) }))
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
	})
}

// task is one tab's row.
func (p *Page) task(gtx gl.Context, t timelog.Tab) gl.Dimensions {
	th := p.th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	w := gtx.Constraints.Max.X
	meta := []string{plural(t.Turns, "turn"), "tools " + timelog.Dur(t.Tools), "you " + timelog.Dur(t.You), ago(gtx.Now.Sub(t.Last))}
	if t.Provider != "" {
		meta = append([]string{agentName(model.Provider(t.Provider))}, meta...)
	}
	steps := "No pull request yet"
	if !t.Opened.IsZero() {
		in := func(a, b time.Time) string {
			if d := timelog.Between(a, b); d != "" {
				return " in " + d
			}
			return ""
		}
		steps = fmt.Sprintf("Prompt → PR #%d%s", t.PR, in(t.Prompt, t.Opened))
		switch {
		case !t.CI.IsZero():
			steps += " → CI " + t.CIState + in(t.Opened, t.CI)
		case t.Done.IsZero():
			steps += " → CI running"
		}
		if !t.Done.IsZero() {
			steps += " → merged" + in(cmp.Or(t.CI, t.Opened), t.Done)
		}
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = w
			return gl.Flex{Alignment: gl.Baseline}.Layout(gtx,
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
					d := p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, cmp.Or(t.Name, t.ID))
					d.Size.X = gtx.Constraints.Max.X
					return d
				}),
				gl.Rigid(gl.Spacer{Width: 12}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, timelog.Dur(t.Agent))
				}),
			)
		}),
		gl.Rigid(gl.Spacer{Height: 2}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, strings.Join(meta, " · "))
		}),
		gl.Rigid(gl.Spacer{Height: 2}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, steps)
		}),
	)
}

// pollTiming asks for frames while a read runs.
func (p *Page) pollTiming(gtx gl.Context) {
	p.tm.mu.Lock()
	busy := p.tm.loading
	p.tm.mu.Unlock()
	if busy {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
}
