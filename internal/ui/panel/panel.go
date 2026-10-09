// Package panel is the side panel: what the agent in the focused pane is
// doing, in five tabs (Flow, Subagents, Plan, Changes, Timeline).
package panel

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Width is the panel's width when the window has room; MinWidth is the
// narrowest it shrinks to before it hides.
const (
	Width    unit.Dp = 432
	MinWidth unit.Dp = 320
)

// Panel keeps the open tab, the subagent being read and each view's scroll
// position between frames.
type Panel struct {
	view   View
	detail int // index into Feed.Subagents being read, -1 for the list
	epoch  time.Time

	lists   map[string]*layout.List
	tabBtn  map[View]*widget.Clickable
	rowBtn  []widget.Clickable // one per subagent
	fileBtn []widget.Clickable // one per changed file
	back    widget.Clickable
	subNode widget.Clickable
	diff    *gitstat.FileStat // the file clicked in Changes, until Diff takes it

	// Set while drawing: something pulses or spins, or a duration ticks.
	live, tick bool
}

// Diff returns the file last clicked in Changes, once.
func (p *Panel) Diff() (gitstat.FileStat, bool) {
	f := p.diff
	p.diff = nil
	if f == nil {
		return gitstat.FileStat{}, false
	}
	return *f, true
}

// SetView opens view, and in Subagents the subagent at index detail (-1
// for the list).
func (p *Panel) SetView(v View, detail int) {
	p.view, p.detail = v, detail
}

func (p *Panel) list(key string) *layout.List {
	if p.lists == nil {
		p.lists = map[string]*layout.List{}
	}
	l := p.lists[key]
	if l == nil {
		l = &layout.List{Axis: layout.Vertical}
		p.lists[key] = l
	}
	return l
}

// Layout draws the panel filling gtx's constraints. It asks for frames
// only while something on it moves: 30 a second for a pulse or spinner,
// once a second for a ticking duration.
func (p *Panel) Layout(gtx layout.Context, th *theme.Theme, in Input) layout.Dimensions {
	if p.view == "" {
		p.view, p.detail = ViewFlow, -1
	}
	if p.epoch.IsZero() {
		p.epoch = gtx.Now
	}
	if p.tabBtn == nil {
		p.tabBtn = map[View]*widget.Clickable{}
	}
	if in.Now.IsZero() {
		in.Now = gtx.Now
	}
	p.live, p.tick = false, false
	tabs := tabsFor(&in)
	var subs []flow.Subagent
	if in.Feed != nil {
		subs = in.Feed.Subagents
	}
	for len(p.rowBtn) < len(subs) {
		p.rowBtn = append(p.rowBtn, widget.Clickable{})
	}
	for _, t := range tabs {
		if b := p.tabBtn[t.view]; b != nil && b.Clicked(gtx) {
			p.view, p.detail = t.view, -1
			p.list(string(t.view)).Position = layout.Position{}
		}
	}
	if p.back.Clicked(gtx) {
		p.detail = -1
	}
	if p.subNode.Clicked(gtx) {
		p.view, p.detail = ViewSubagents, -1
	}
	for i := range subs {
		if p.rowBtn[i].Clicked(gtx) {
			p.detail = i
			p.list("detail").Position = layout.Position{}
		}
	}
	for len(p.fileBtn) < len(in.Files) {
		p.fileBtn = append(p.fileBtn, widget.Clickable{})
	}
	for i, f := range in.Files {
		if p.fileBtn[i].Clicked(gtx) {
			p.diff = &f
		}
	}
	shown := false
	for _, t := range tabs {
		shown = shown || t.view == p.view
	}
	if !shown {
		p.view, p.detail = ViewFlow, -1
	}
	if p.detail >= len(subs) {
		p.detail = -1
	}

	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	d := &drawer{p: p, th: th, c: palette(th), in: &in, t: gtx.Now.Sub(p.epoch).Seconds()}

	hm := op.Record(gtx.Ops)
	hgtx := gtx
	hgtx.Constraints = layout.Constraints{Max: size}
	hd := d.tabs(hgtx, tabs)
	header := hm.Stop()

	off := op.Offset(image.Pt(0, hd.Size.Y)).Push(gtx.Ops)
	bgtx := gtx
	bgtx.Constraints = layout.Exact(image.Pt(size.X, max(0, size.Y-hd.Size.Y)))
	items := d.items()
	key := string(p.view)
	if p.view == ViewSubagents && p.detail >= 0 {
		key = "detail"
	}
	inset := layout.Inset{Left: 14, Right: 14}
	l := p.list(key)
	// The timeline follows its newest line once it overflows; a short one
	// stays at the top.
	l.ScrollToEnd = p.view == ViewTimeline && l.Position.Length > bgtx.Constraints.Max.Y
	l.Layout(bgtx, len(items), func(gtx layout.Context, i int) layout.Dimensions {
		return inset.Layout(gtx, items[i])
	})
	off.Pop()
	header.Add(gtx.Ops)

	switch {
	case p.live:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	case p.tick:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Truncate(time.Second).Add(time.Second)})
	}
	return layout.Dimensions{Size: size}
}

// drawer is one frame's drawing state.
type drawer struct {
	p  *Panel
	th *theme.Theme
	c  pal
	in *Input
	t  float64 // seconds since the panel's first frame, for animations
}

func (d *drawer) ui(size unit.Sp, col color.NRGBA, s string) layout.Widget {
	return d.text(d.th.UIFont, size, col, s, 1)
}

func (d *drawer) text(f font.Font, size unit.Sp, col color.NRGBA, s string, lines int) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return label(gtx, d.th, f, size, col, s, lines) }
}

// tabs is the sticky tab row, the agent's token use under it, and the
// bottom hairline.
func (d *drawer) tabs(gtx layout.Context, tabs []tab) layout.Dimensions {
	var parts []part
	for i, t := range tabs {
		btn := d.p.tabBtn[t.view]
		if btn == nil {
			btn = new(widget.Clickable)
			d.p.tabBtn[t.view] = btn
		}
		on := t.view == d.p.view
		parts = append(parts, part{gap: gapIf(i > 0, gtx.Dp(1)), w: func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				col := d.c.muted
				if on || btn.Hovered() {
					col = d.c.fg
				}
				in := layout.Inset{Top: 5, Bottom: 5, Left: 9, Right: 9}
				return boxed(gtx, in, func(gtx layout.Context) layout.Dimensions {
					ps := []part{{w: d.ui(12.5, col, t.label)}}
					if t.live {
						ps = append(ps, part{gap: gtx.Dp(6), w: func(gtx layout.Context) layout.Dimensions {
							d.p.live = true
							s := gtx.Dp(6)
							off := op.Offset(image.Pt(0, gtx.Dp(5.5))).Push(gtx.Ops)
							circle(gtx, image.Point{}, s, theme.Mix(d.c.bg, d.c.blue, pulse(d.t)))
							off.Pop()
							return layout.Dimensions{Size: image.Pt(s, s)}
						}})
					}
					if t.count != "" {
						ps = append(ps, part{gap: gtx.Dp(gapF(t.live, 4, 6)), w: pad(2, 0, 0, 0, d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.quiet, t.count, 1))})
					}
					gtx.Constraints.Min = image.Point{}
					return rowFit(gtx, ps...)
				}, func(sz image.Point) {
					if on {
						rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(6), d.th.SurfaceSecondary, color.NRGBA{})
					}
				})
			})
		}})
	}
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return row(gtx, parts...)
			})
		}),
		layout.Rigid(d.usageLine),
	)
	h := dims.Size.Y + gtx.Dp(1)
	w := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, d.c.border, clip.Rect{Min: image.Pt(0, h-gtx.Dp(1)), Max: image.Pt(w, h)}.Op())
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func gapIf(b bool, n int) int {
	if b {
		return n
	}
	return 0
}

func gapF(b bool, a, c unit.Dp) unit.Dp {
	if b {
		return a
	}
	return c
}

// rowFit is row sized to its content.
func rowFit(gtx layout.Context, parts ...part) layout.Dimensions {
	x, h := 0, 0
	for _, p := range parts {
		x += p.gap
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-x), gtx.Constraints.Max.Y)}
		dm := p.w(g)
		call := m.Stop()
		off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		x += dm.Size.X
		h = max(h, dm.Size.Y)
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}

// items are the open view's rows, top to bottom.
func (d *drawer) items() []layout.Widget {
	var out []layout.Widget
	switch d.p.view {
	case ViewSubagents:
		if d.p.detail >= 0 {
			out = d.detail(d.in.Feed.Subagents[d.p.detail])
		} else {
			out = d.subagents()
		}
	case ViewPlan:
		out = d.plan()
	case ViewChanges:
		out = d.changes()
	case ViewTimeline:
		out = d.timeline()
	default:
		out = d.flow()
	}
	return append(append([]layout.Widget{space(14)}, out...), space(28))
}

// note is an empty state's muted paragraph.
func (d *drawer) note(s string) layout.Widget {
	return pad(2, 2, 6, 2, d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.muted, s, 0))
}

// section is the mock's .dsec heading.
func (d *drawer) section(s string) layout.Widget {
	return pad(18, 0, 7, 0, d.text(d.th.UIFont, d.th.Sp(theme.Caption), d.c.quiet, strings.ToUpper(s), 1))
}

// --- Flow ---

func (d *drawer) flow() []layout.Widget {
	in := d.in
	if in.Pane == nil {
		return []layout.Widget{d.note("No pane is focused.")}
	}
	ag := in.agent()
	if ag == "" {
		return []layout.Widget{d.note("No agent in this pane. Start Claude Code, Codex, Gemini CLI or pi here and the panel follows it. Changes shows this folder's work.")}
	}
	out := []layout.Widget{d.banner(bannerFor(in))}
	if !flow.Reads(ag) {
		return append(out, space(12), d.note(sidebar.AgentName(ag)+" keeps no session file pitwall reads, so its turns and tool calls do not show here. Changes shows this folder's work."))
	}
	if in.Feed == nil {
		return append(out, space(12), d.note("No transcript yet. The panel reads the agent's session file once a hook names it, usually at the next prompt or tool call."))
	}
	out = append(out, space(12), d.graph(graphFor(in)), space(12), d.stats(statsFor(in)))
	if u := in.usage(); u != nil {
		out = append(out, d.session(*u)...)
	}
	if n := len(in.Feed.Turns); n > 1 {
		out = append(out, d.section("Earlier turns"))
		for i := n - 2; i >= max(0, n-11); i-- {
			t := in.Feed.Turns[i]
			out = append(out, d.event(fmt.Sprintf("Turn %d", i+1), t.Prompt+" · Done", "", false))
		}
	}
	switch {
	case ag == model.ProviderPi:
		out = append(out, space(14), d.note("pi has no plan or subagents to show."))
	case ag == model.ProviderGemini && in.Feed.Plan == nil:
		out = append(out, space(14), d.note("Plan shows once Gemini calls write_todos."))
	case ag == model.ProviderCodex && (in.Feed.Plan == nil || len(in.Feed.Subagents) == 0):
		out = append(out, space(14), d.note("Plan and Subagents show once Codex calls update_plan or spawn_agent."))
	}
	return out
}

func (d *drawer) banner(b banner) layout.Widget {
	accent := d.c.primary
	switch b.kind {
	case "wait":
		accent = d.c.yellow
	case "error":
		accent = d.c.red
	case "done":
		accent = d.c.green
	}
	return func(gtx layout.Context) layout.Dimensions {
		return boxed(gtx, layout.Inset{Top: 11, Bottom: 12, Left: 14, Right: 14}, func(gtx layout.Context) layout.Dimensions {
			ws := []layout.FlexChild{
				layout.Rigid(d.text(semibold(d.th.UIFont), d.th.Sp(theme.Caption), accent, strings.ToUpper(b.eyebrow), 1)),
				layout.Rigid(pad(5, 0, 0, 0, d.text(semibold(d.th.UIFont), d.th.Sp(theme.Title), d.c.fg, b.what, 2))),
			}
			if b.why != "" {
				ws = append(ws, layout.Rigid(pad(4, 0, 0, 0, d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.muted, b.why, 3))))
			}
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
		}, func(sz image.Point) {
			rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(10), theme.Mix(d.c.bg, accent, 0.06), theme.Mix(d.c.bg, accent, 0.38))
		})
	}
}

// graph draws the nodes in a column on a dotted grid, joined by labelled
// curves; an edge into a node not reached yet is dashed.
func (d *drawer) graph(nodes []node) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		padY, h, gap := gtx.Dp(18), gtx.Dp(86), gtx.Dp(44)
		nw := min(gtx.Dp(252), w-2*gtx.Dp(18))
		x := (w - nw) / 2
		H := 2*padY + len(nodes)*h + (len(nodes)-1)*gap
		box := image.Rectangle{Max: image.Pt(w, H)}
		rrect(gtx, box, gtx.Dp(12), d.c.graph, d.c.border)

		// The dots, one path.
		cl := clip.UniformRRect(box.Inset(gtx.Dp(1)), gtx.Dp(11)).Push(gtx.Ops)
		var p clip.Path
		p.Begin(gtx.Ops)
		step, dot := float32(gtx.Dp(14)), float32(gtx.Dp(1.2))
		for yy := step / 2; yy < float32(H); yy += step {
			for xx := step / 2; xx < float32(w); xx += step {
				p.MoveTo(f32.Pt(xx, yy))
				p.LineTo(f32.Pt(xx+dot, yy))
				p.LineTo(f32.Pt(xx+dot, yy+dot))
				p.LineTo(f32.Pt(xx, yy+dot))
				p.Close()
			}
		}
		paint.FillShape(gtx.Ops, theme.Mix(d.c.graph, d.c.fg, 0.09), clip.Outline{Path: p.End()}.Op())
		cl.Pop()

		cx := float32(x + nw/2)
		for i := 1; i < len(nodes); i++ {
			y1 := float32(padY + i*h + (i-1)*gap)
			y2 := y1 + float32(gap)
			lit := nodes[i].state != nodePending
			col := theme.Mix(d.c.graph, d.c.green, 0.45)
			if !lit {
				col = theme.Mix(d.c.graph, d.c.fg, 0.16)
			}
			bezier(gtx, f32.Pt(cx, y1), f32.Pt(cx, y2), float32(gtx.Dp(1.3)), col, !lit)
			if e := nodes[i].edge; e != "" {
				m := op.Record(gtx.Ops)
				ld := label(gtx, d.th, d.th.UIFont, d.th.Sp(theme.Caption), d.c.quiet, e, 1)
				call := m.Stop()
				lr := image.Rectangle{Max: ld.Size.Add(image.Pt(gtx.Dp(8), 0))}
				lr = lr.Add(image.Pt(int(cx)-lr.Dx()/2, int((y1+y2)/2)-lr.Dy()/2))
				paint.FillShape(gtx.Ops, d.c.graph, clip.Rect(lr).Op())
				off := op.Offset(lr.Min.Add(image.Pt(gtx.Dp(4), 0))).Push(gtx.Ops)
				call.Add(gtx.Ops)
				off.Pop()
			}
		}
		for i, n := range nodes {
			r := image.Rect(x, padY+i*(h+gap), x+nw, padY+i*(h+gap)+h)
			off := op.Offset(r.Min).Push(gtx.Ops)
			g := gtx
			g.Constraints = layout.Exact(r.Size())
			if n.subagents {
				clickable(g, &d.p.subNode, func(gtx layout.Context) layout.Dimensions { return d.node(gtx, n) })
			} else {
				d.node(g, n)
			}
			off.Pop()
		}
		return layout.Dimensions{Size: box.Max}
	}
}

func (d *drawer) node(gtx layout.Context, n node) layout.Dimensions {
	size := gtx.Constraints.Max
	r := image.Rectangle{Max: size}
	rad := gtx.Dp(9)
	top, title := d.c.quiet, d.c.fg
	stateText := map[nodeState]string{nodeCompleted: "Completed", nodeRunning: "Running", nodeWaiting: "Needs you", nodePending: "Not reached"}[n.state]
	switch n.state {
	case nodeCompleted:
		rrect(gtx, r, rad, theme.Mix(d.c.graph, d.c.green, 0.06), theme.Mix(d.c.graph, d.c.green, 0.45))
		top = theme.Mix(d.c.green, d.c.fg, 0.35)
	case nodeRunning:
		g := gtx.Dp(3)
		rrect(gtx, r.Inset(-g), rad+g, theme.Mix(d.c.graph, d.c.blue, 0.12), color.NRGBA{})
		rrect(gtx, r, rad, theme.Mix(d.c.graph, d.c.blue, 0.08), theme.Mix(d.c.graph, d.c.blue, 0.7))
		top = d.c.blue
	case nodeWaiting:
		rrect(gtx, r, rad, theme.Mix(d.c.graph, d.c.yellow, 0.07), theme.Mix(d.c.graph, d.c.yellow, 0.6))
		top = d.c.yellow
		if n.title == "Error" {
			top = d.c.red
		}
	default:
		rrect(gtx, r, rad, d.c.node, color.NRGBA{})
		dashedBorder(gtx, r, rad, d.c.border2)
		title = d.c.muted
	}
	if n.subagents && d.p.subNode.Hovered() {
		paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 10}, clip.UniformRRect(r, rad).Op(gtx.Ops))
	}
	icon := func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(10)
		off := op.Offset(image.Pt(0, gtx.Dp(1))).Push(gtx.Ops)
		defer off.Pop()
		switch n.state {
		case nodeCompleted:
			return check(gtx, s, float32(gtx.Dp(1.4)), d.c.green)
		case nodeRunning:
			d.p.live = true
			return spinner(gtx, s, d.c.graph, d.c.blue, d.t)
		case nodeWaiting:
			m := op.Record(gtx.Ops)
			ld := label(gtx, d.th, semibold(d.th.UIFont), d.th.Sp(theme.Caption), top, "!", 1)
			c := m.Stop()
			o := op.Offset(image.Pt((s-ld.Size.X)/2, -gtx.Dp(2))).Push(gtx.Ops)
			c.Add(gtx.Ops)
			o.Pop()
			return layout.Dimensions{Size: image.Pt(s, s)}
		}
		ring(gtx, image.Pt(gtx.Dp(0.5), gtx.Dp(0.5)), gtx.Dp(9), float32(gtx.Dp(1.5)), d.c.quiet)
		return layout.Dimensions{Size: image.Pt(s, s)}
	}
	return layout.Inset{Top: 7, Bottom: 7, Left: 11, Right: 11}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return row(gtx,
					part{w: icon},
					part{gap: gtx.Dp(6), flex: true, w: d.text(semibold(d.th.UIFont), d.th.Sp(theme.Caption), top, strings.ToUpper(n.kind), 1)},
					part{gap: gtx.Dp(6), w: d.text(semibold(d.th.UIFont), d.th.Sp(theme.Caption), top, strings.ToUpper(stateText), 1)},
				)
			}),
			layout.Rigid(pad(3, 0, 0, 0, d.text(semibold(d.th.UIFont), d.th.Sp(theme.Body), title, n.title, 2))),
			layout.Rigid(pad(2, 0, 0, 0, d.ui(11.5, d.c.muted, n.detail))),
		)
	})
}

func (d *drawer) stats(stats []stat) layout.Widget {
	cell := func(s stat) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return boxed(gtx, layout.Inset{Top: 9, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				var v layout.Widget
				if s.key == "Turn time" && d.in.turn() != nil && d.in.turn().End.IsZero() {
					d.p.tick = true
				}
				if s.changes {
					v = func(gtx layout.Context) layout.Dimensions {
						sb := semibold(d.th.UIFont)
						return rowFit(gtx,
							part{w: d.text(sb, d.th.Sp(theme.Title), d.c.green, fmt.Sprintf("+%d", s.add), 1)},
							part{gap: gtx.Dp(4), w: d.text(sb, d.th.Sp(theme.Title), d.c.red, fmt.Sprintf("-%d", s.del), 1)},
							part{gap: gtx.Dp(5), w: pad(3.5, 0, 0, 0, d.text(medium(d.th.UIFont), d.th.Sp(theme.Small), d.c.muted, s.value, 1))},
						)
					}
				} else {
					v = d.text(semibold(d.th.UIFont), d.th.Sp(theme.Title), d.c.fg, s.value, 1)
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(d.ui(10, d.c.quiet, strings.ToUpper(s.key))),
					layout.Rigid(pad(4, 0, 0, 0, v)),
				)
			}, func(sz image.Point) {
				rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(10), d.c.card, d.c.border)
			})
		}
	}
	return func(gtx layout.Context) layout.Dimensions {
		g := gtx.Dp(8)
		cw := (gtx.Constraints.Max.X - g) / 2
		y := 0
		for i := 0; i < len(stats); i += 2 {
			m := op.Record(gtx.Ops)
			dims := row(gtx, part{w: fixed(cw, cell(stats[i]))}, part{gap: g, w: fixed(cw, cell(stats[min(i+1, len(stats)-1)]))})
			c := m.Stop()
			off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
			c.Add(gtx.Ops)
			off.Pop()
			y += dims.Size.Y + g
		}
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y-g)}
	}
}

// event is a timeline-style row: a time column and text, with an optional
// purple note under it.
func (d *drawer) event(at, txt, note string, bad bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		col := d.c.fg
		if bad {
			col = d.c.red
		}
		dims := layout.Inset{Top: 5, Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return row(gtx,
				part{w: fixed(gtx.Dp(44), pad(1.5, 0, 0, 0, d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.quiet, at, 1)))},
				part{gap: gtx.Dp(10), flex: true, w: func(gtx layout.Context) layout.Dimensions {
					ws := []layout.FlexChild{layout.Rigid(d.text(d.th.UIFont, d.th.Sp(theme.Small), col, txt, 0))}
					if note != "" {
						ws = append(ws, layout.Rigid(d.text(d.th.UIFont, d.th.Sp(theme.Small), d.c.purple, note, 0)))
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
				}},
			)
		})
		paint.FillShape(gtx.Ops, d.c.soft, clip.Rect{Min: image.Pt(0, dims.Size.Y-gtx.Dp(1)), Max: dims.Size}.Op())
		return dims
	}
}

// --- Subagents ---

func (d *drawer) subagents() []layout.Widget {
	var subs []flow.Subagent
	if d.in.Feed != nil {
		subs = d.in.Feed.Subagents
	}
	if len(subs) == 0 {
		return []layout.Widget{d.note("No subagents in this session yet. " + sidebar.AgentName(d.in.agent()) + " starts them for side tasks, and each one shows here with what it is doing.")}
	}
	var act, done []int
	for i, s := range subs {
		if s.Running() {
			act = append(act, i)
		} else {
			done = append(done, i)
		}
	}
	var out []layout.Widget
	group := func(name string, idx []int) {
		if len(idx) == 0 {
			return
		}
		top := unit.Dp(6)
		if len(out) > 0 {
			top = 14
		}
		out = append(out, pad(top, 2, 4, 2, d.ui(12, d.c.quiet, fmt.Sprintf("%s · %d", name, len(idx)))))
		for _, i := range idx {
			out = append(out, d.subRow(i, subs[i]))
		}
	}
	slices.SortStableFunc(done, func(a, b int) int { return subs[b].End.Compare(subs[a].End) })
	group("Active", act)
	group("Done", done)
	return out
}

func (d *drawer) subRow(i int, s flow.Subagent) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		btn := &d.p.rowBtn[i]
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return clickable(gtx, btn, func(gtx layout.Context) layout.Dimensions {
			return boxed(gtx, layout.UniformInset(8), func(gtx layout.Context) layout.Dimensions {
				when := sidebar.RelTime(d.in.Now, s.End)
				if s.Running() {
					when = duration(d.in.Now.Sub(s.Start))
					d.p.tick = true
				}
				return row(gtx,
					part{w: pad(1, 0, 0, 0, func(gtx layout.Context) layout.Dimensions { return avatar(gtx, hue(s.ID), gtx.Dp(22), d.c.bg) })},
					part{gap: gtx.Dp(12), flex: true, w: func(gtx layout.Context) layout.Dimensions {
						ws := []layout.FlexChild{layout.Rigid(d.text(medium(d.th.UIFont), d.th.Sp(theme.Large), d.c.fg, s.Name, 1))}
						if s.Running() {
							doing := "Thinking"
							if n := len(s.Calls); n > 0 {
								doing = strings.TrimSpace(s.Calls[n-1].Tool + " " + s.Calls[n-1].Arg)
							}
							ws = append(ws, layout.Rigid(pad(3, 0, 0, 0, d.ui(12, d.c.muted, doing))))
						} else if s.Failed {
							ws = append(ws, layout.Rigid(pad(3, 0, 0, 0, d.ui(12, d.c.red, "Failed"))))
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
					}},
					part{gap: gtx.Dp(12), w: pad(2, 0, 0, 0, d.text(d.th.MonoFont, d.th.Sp(theme.Small), d.c.quiet, when, 1))},
				)
			}, func(sz image.Point) {
				if btn.Hovered() {
					rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(8), d.th.SurfaceSecondary, color.NRGBA{})
				}
			})
		})
	}
}

func (d *drawer) detail(s flow.Subagent) []layout.Widget {
	status, stCol := "Done in "+duration(s.End.Sub(s.Start)), d.c.green
	switch {
	case s.Running():
		status, stCol = "Working · "+duration(d.in.Now.Sub(s.Start)), d.c.blue
		d.p.tick = true
	case s.Failed:
		status, stCol = "Failed after "+duration(s.End.Sub(s.Start)), d.c.red
	}
	out := []layout.Widget{
		pad(0, 0, 10, 0, func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, &d.p.back, func(gtx layout.Context) layout.Dimensions {
				col := d.c.muted
				if d.p.back.Hovered() {
					col = d.c.fg
				}
				return boxed(gtx, layout.Inset{Top: 4, Bottom: 4, Left: 6, Right: 6}, d.ui(12.5, col, "‹ Subagents"), func(sz image.Point) {
					if d.p.back.Hovered() {
						rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(6), d.th.SurfaceSecondary, color.NRGBA{})
					}
				})
			})
		}),
		func(gtx layout.Context) layout.Dimensions {
			return row(gtx,
				part{w: func(gtx layout.Context) layout.Dimensions { return avatar(gtx, hue(s.ID), gtx.Dp(34), d.c.bg) }},
				part{gap: gtx.Dp(12), flex: true, w: func(gtx layout.Context) layout.Dimensions {
					meta := func(gtx layout.Context) layout.Dimensions {
						var ps []part
						if s.Type != "" {
							ps = append(ps, part{w: d.text(d.th.MonoFont, d.th.Sp(theme.Small), d.c.purple, s.Type, 1)}, part{w: d.ui(12, d.c.muted, " · ")})
						}
						ps = append(ps, part{w: d.ui(12, stCol, status)})
						if !s.Running() {
							ps = append(ps, part{w: d.ui(12, d.c.muted, " · "+sidebar.RelTime(d.in.Now, s.End))})
						}
						return rowFit(gtx, ps...)
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(d.text(semibold(d.th.UIFont), d.th.Sp(theme.Title), d.c.fg, s.Name, 2)),
						layout.Rigid(pad(3, 0, 0, 0, meta)),
					)
				}},
			)
		},
		d.section("Asked to"),
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return boxed(gtx, layout.Inset{Top: 10, Bottom: 10, Left: 14, Right: 12}, func(gtx layout.Context) layout.Dimensions {
				dm := label(gtx, d.th, d.th.UIFont, d.th.Sp(theme.Body), theme.Mix(d.c.muted, d.c.fg, 0.6), s.Prompt, 0)
				dm.Size.X = gtx.Constraints.Max.X
				return dm
			}, func(sz image.Point) {
				rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(8), d.c.card2, color.NRGBA{})
				paint.FillShape(gtx.Ops, d.c.border2, clip.Rect{Max: image.Pt(gtx.Dp(2), sz.Y)}.Op())
			})
		},
	}
	said, saidCol := "Result", d.c.fg
	if s.Running() {
		said, saidCol = "Latest message", theme.Mix(d.c.fg, d.c.blue, 0.25)
	}
	latest := s.Latest
	if latest == "" {
		latest = "Nothing yet."
	}
	out = append(out, d.section(said), d.text(d.th.UIFont, d.th.Sp(theme.Large), saidCol, latest, 0))
	out = append(out, d.section(fmt.Sprintf("Tool calls · %d", len(s.Calls))))
	for _, c := range s.Calls {
		out = append(out, d.callRow(clock(c.Time.Sub(s.Start)), c))
	}
	return out
}

func (d *drawer) callRow(at string, c flow.Call) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		argCol, toolCol := d.c.muted, d.c.fg
		if c.Failed {
			toolCol = d.c.red
		}
		tool := d.text(semibold(d.th.UIFont), d.th.Sp(theme.Small), toolCol, c.Tool, 1)
		arg := c.Arg
		if c.Running {
			argCol = d.c.blue
			arg = strings.TrimSpace(c.Tool + " " + c.Arg)
			tool = func(gtx layout.Context) layout.Dimensions {
				d.p.live = true
				off := op.Offset(image.Pt(0, gtx.Dp(3))).Push(gtx.Ops)
				defer off.Pop()
				return spinner(gtx, gtx.Dp(9), d.c.bg, d.c.blue, d.t)
			}
		}
		dims := layout.Inset{Top: 5, Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return row(gtx,
				part{w: fixed(gtx.Dp(36), pad(1.5, 0, 0, 0, d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.quiet, at, 1)))},
				part{gap: gtx.Dp(8), w: fixed(gtx.Dp(64), tool)},
				part{gap: gtx.Dp(8), flex: true, w: d.text(d.th.MonoFont, d.th.Sp(theme.Small), argCol, arg, 1)},
			)
		})
		paint.FillShape(gtx.Ops, d.c.soft, clip.Rect{Min: image.Pt(0, dims.Size.Y-gtx.Dp(1)), Max: dims.Size}.Op())
		return dims
	}
}

// --- Plan ---

func (d *drawer) plan() []layout.Widget {
	var plan []flow.Step
	if d.in.Feed != nil {
		plan = d.in.Feed.Plan
	}
	if len(plan) == 0 {
		return []layout.Widget{d.note("No plan yet. " + sidebar.AgentName(d.in.agent()) + " writes one for longer work, and its steps show here as they finish.")}
	}
	done, total := planCounts(plan)
	head := func(gtx layout.Context) layout.Dimensions {
		segs := func(gtx layout.Context) layout.Dimensions {
			w, h, g := gtx.Dp(18), gtx.Dp(4), gtx.Dp(3)
			for i, s := range plan {
				col := theme.Mix(d.c.bg, d.c.fg, 0.11)
				switch s.State {
				case flow.StepDone:
					col = d.c.green
				case flow.StepActive:
					d.p.live = true
					col = theme.Mix(d.c.bg, d.c.blue, pulse(d.t))
				}
				x := i * (w + g)
				paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(x, gtx.Dp(6), x+w, gtx.Dp(6)+h), h/2).Op(gtx.Ops))
			}
			return layout.Dimensions{Size: image.Pt(len(plan)*(w+g)-g, gtx.Dp(16))}
		}
		return row(gtx,
			part{w: d.text(semibold(d.th.UIFont), d.th.Sp(theme.Small), d.c.fg, fmt.Sprint(done), 1)},
			part{flex: true, w: d.ui(12.5, d.c.muted, fmt.Sprintf(" of %d done", total))},
			part{w: segs},
		)
	}
	out := []layout.Widget{pad(2, 0, 14, 0, head)}
	for i, s := range plan {
		out = append(out, d.step(i, s, i == len(plan)-1))
	}
	return out
}

func (d *drawer) step(i int, s flow.Step, last bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		dot := gtx.Dp(21)
		txtCol, f := theme.Mix(d.c.muted, d.c.fg, 0.4), d.th.UIFont
		switch s.State {
		case flow.StepDone:
			txtCol = d.c.quiet
		case flow.StepActive:
			txtCol, f = d.c.fg, semibold(f)
		}
		m := op.Record(gtx.Ops)
		dims := layout.Inset{Bottom: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return row(gtx,
				part{w: fixed(dot, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(dot, dot)} })},
				part{gap: gtx.Dp(12), flex: true, w: pad(2, 0, 0, 0, func(gtx layout.Context) layout.Dimensions {
					ws := []layout.FlexChild{layout.Rigid(d.text(f, d.th.Sp(theme.Large), txtCol, s.Text, 0))}
					if s.State == flow.StepActive {
						ws = append(ws, layout.Rigid(pad(3, 0, 0, 0, d.ui(11.5, d.c.blue, "In progress"))))
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ws...)
				})},
			)
		})
		text := m.Stop()
		if !last {
			col := theme.Mix(d.c.bg, d.c.fg, 0.11)
			if s.State == flow.StepDone {
				col = theme.Mix(d.c.bg, d.c.green, 0.45)
			}
			x := gtx.Dp(10)
			paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, dot), Max: image.Pt(x+gtx.Dp(1.5), dims.Size.Y)}.Op())
		}
		switch s.State {
		case flow.StepDone:
			circle(gtx, image.Point{}, dot, theme.Mix(d.c.bg, d.c.green, 0.18))
			off := op.Offset(image.Pt((dot-gtx.Dp(11))/2, (dot-gtx.Dp(11))/2)).Push(gtx.Ops)
			check(gtx, gtx.Dp(11), float32(gtx.Dp(1.5)), d.c.green)
			off.Pop()
		case flow.StepActive:
			d.p.live = true
			a := pulse(d.t)
			g := gtx.Dp(4)
			circle(gtx, image.Pt(-g, -g), dot+2*g, theme.Mix(d.c.bg, d.c.blue, 0.12*a))
			circle(gtx, image.Point{}, dot, d.c.bg)
			ring(gtx, image.Point{}, dot, float32(gtx.Dp(1.5)), theme.Mix(d.c.bg, d.c.blue, a))
			r := gtx.Dp(7)
			circle(gtx, image.Pt((dot-r)/2, (dot-r)/2), r, theme.Mix(d.c.bg, d.c.blue, a))
		default:
			circle(gtx, image.Point{}, dot, d.c.bg)
			ring(gtx, image.Point{}, dot, float32(gtx.Dp(1.5)), theme.Mix(d.c.bg, d.c.fg, 0.17))
			mm := op.Record(gtx.Ops)
			ld := label(gtx, d.th, semibold(d.th.MonoFont), d.th.Sp(theme.Caption), d.c.quiet, fmt.Sprint(i+1), 1)
			c := mm.Stop()
			off := op.Offset(image.Pt((dot-ld.Size.X)/2, (dot-ld.Size.Y)/2)).Push(gtx.Ops)
			c.Add(gtx.Ops)
			off.Pop()
		}
		text.Add(gtx.Ops)
		return dims
	}
}

// --- Changes ---

func (d *drawer) changes() []layout.Widget {
	in := d.in
	if !in.Git {
		return []layout.Widget{d.note("Not in a git repository, or git has not answered yet.")}
	}
	add, del := 0, 0
	for _, f := range in.Files {
		add, del = add+f.Add, del+f.Del
	}
	base := gitstat.BranchName(in.Base)
	if base == "" {
		base = "base"
	}
	sb := semibold(d.th.UIFont)
	out := []layout.Widget{pad(0, 2, 4, 2, func(gtx layout.Context) layout.Dimensions {
		parts := []part{
			{w: d.ui(12, d.c.quiet, "Worktree vs "+base+" · ")},
			{w: d.text(sb, d.th.Sp(theme.Small), d.c.green, fmt.Sprintf("+%d", add), 1)},
			{gap: gtx.Dp(4), w: d.text(sb, d.th.Sp(theme.Small), d.c.red, fmt.Sprintf("-%d", del), 1)},
		}
		if p := in.Ports.First; p != 0 {
			// A worktree tab's first port, at the right.
			parts = append(parts, part{flex: true, gap: gtx.Dp(8), w: func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.E.Layout(gtx, d.text(d.th.MonoFont, d.th.Sp(theme.Caption), d.c.quiet, fmt.Sprint("PORT ", p), 1))
			}})
			return row(gtx, parts...)
		}
		return rowFit(gtx, parts...)
	})}
	if len(in.Files) == 0 {
		return append(out, d.note("No changes from "+base+"."))
	}
	// A click on a file opens its diff.
	for i, f := range in.Files {
		btn := &d.p.fileBtn[i]
		out = append(out, func(gtx layout.Context) layout.Dimensions {
			dir, name := splitPath(f.Path)
			stCol := map[byte]color.NRGBA{'A': d.c.green, 'D': d.c.red, 'M': d.c.yellow, 'R': d.c.purple}[f.Status]
			if stCol.A == 0 {
				stCol = d.c.quiet
			}
			mono := d.th.MonoFont
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return clickable(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				return boxed(gtx, layout.Inset{Top: 4, Bottom: 4, Left: 4, Right: 4}, func(gtx layout.Context) layout.Dimensions {
					return row(gtx,
						part{w: fixed(gtx.Dp(14), d.text(mono, d.th.Sp(theme.Caption), stCol, string(f.Status), 1))},
						part{gap: gtx.Dp(4), flex: true, w: func(gtx layout.Context) layout.Dimensions {
							return row(gtx, part{w: d.text(mono, d.th.Sp(theme.Small), d.c.quiet, dir, 1)}, part{flex: true, w: d.text(mono, d.th.Sp(theme.Small), theme.Mix(d.c.muted, d.c.fg, 0.7), name, 1)})
						}},
						part{gap: gtx.Dp(8), w: d.text(mono, d.th.Sp(theme.Caption), d.c.green, fmt.Sprintf("+%d", f.Add), 1)},
						part{gap: gtx.Dp(6), w: d.text(mono, d.th.Sp(theme.Caption), d.c.red, fmt.Sprintf("-%d", f.Del), 1)},
					)
				}, func(sz image.Point) {
					if btn.Hovered() {
						rrect(gtx, image.Rectangle{Max: sz}, gtx.Dp(6), d.th.SurfaceSecondary, color.NRGBA{})
					}
				})
			})
		})
	}
	return out
}

// --- Timeline ---

func (d *drawer) timeline() []layout.Widget {
	evs := timeline(d.in)
	if len(evs) == 0 {
		if d.in.Feed == nil {
			return []layout.Widget{d.note("No transcript yet.")}
		}
		return []layout.Widget{d.note("Nothing has happened in this session yet.")}
	}
	out := make([]layout.Widget, len(evs))
	for i, e := range evs {
		out[i] = d.event(e.at.Local().Format("15:04"), e.text, e.note, e.bad)
	}
	return out
}
