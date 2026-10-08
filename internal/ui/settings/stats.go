package settings

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decisionlog"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Windows of the Decision stats category.
const (
	week  = "7 days"
	month = "30 days"
	ever  = "All time"
)

// statsPage is the Decision stats category's state: the log's stats over
// the chosen window, worked out off the UI goroutine.
type statsPage struct {
	path   string // decisions.jsonl; tests set it
	window string // week, month or ever; "" is week
	read   bool   // a read has started since the page opened

	mu      sync.Mutex
	gen     int // the latest read; an older one's result is dropped
	loading bool
	stats   *decisionlog.Stats
	any     bool // the log has an event at all
	err     string
}

// Lines the category shows instead of stats.
const (
	statsEmpty   = "Appears after your agents ask for their first approval."
	statsLoading = "Reading the decisions log…"
)

func (p *Page) journal() string {
	if p.st.path == "" {
		p.st.path = filepath.Join(config.StateDir(), "decisions.jsonl")
	}
	return p.st.path
}

// readLog reads decisions.jsonl; tests replace it.
var readLog = decisionlog.Read

// readStats reads the log and works out the window's stats in the
// background. Frames poll for the result while it runs.
func (p *Page) readStats() {
	p.st.read = true
	win, path := p.st.window, p.journal()
	p.st.mu.Lock()
	p.st.gen++
	gen := p.st.gen
	p.st.loading = true
	p.st.mu.Unlock()
	go func() {
		evs, err := readLog(path, time.Time{})
		s := windowStats(evs, win, time.Now())
		p.st.mu.Lock()
		if gen == p.st.gen {
			p.st.stats, p.st.any, p.st.loading, p.st.err = &s, len(evs) > 0, false, ""
			if err != nil {
				p.st.err = err.Error()
			}
		}
		p.st.mu.Unlock()
	}()
}

// windowStats is the stats of the events in win up to now. evs is oldest
// first, so all time starts at the first.
func windowStats(evs []decisionlog.Event, win string, now time.Time) decisionlog.Stats {
	from := now.AddDate(0, 0, -7)
	switch win {
	case month:
		from = now.AddDate(0, 0, -30)
	case ever:
		from = now
		if len(evs) > 0 {
			from = evs[0].T
		}
	}
	var in []decisionlog.Event
	for _, e := range evs {
		if !e.T.Before(from) {
			in = append(in, e)
		}
	}
	return decisionlog.Compute(in, from, now)
}

// statsNote is the line shown instead of stats, "" when there are some.
func statsNote(s *decisionlog.Stats, any, loading bool, win string) string {
	switch {
	case s == nil && loading:
		return statsLoading
	case s == nil || !any:
		return statsEmpty
	case s.Calls == 0 && win == ever:
		return "Nothing logged yet. pitwall logs while a decision provider is on."
	case s.Calls == 0:
		return "Nothing logged in the last " + win + "."
	}
	return ""
}

func (p *Page) stats() []section {
	th := p.th
	p.st.mu.Lock()
	s, any, loading, err := p.st.stats, p.st.any, p.st.loading || !p.st.read, p.st.err
	p.st.mu.Unlock()
	win := p.st.window
	if win == "" {
		win = week
	}
	note := statsNote(s, any, loading, win)

	head := func(gtx gl.Context) gl.Dimensions {
		if !p.st.read {
			p.readStats()
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var kids []gl.FlexChild
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Alignment: gl.Start}.Layout(gtx,
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
					if note != "" {
						d := gl.Inset{Top: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
							return p.para(gtx, th.UIFont, p.sp(14), th.Muted, note)
						})
						d.Size.X = gtx.Constraints.Max.X // keeps the window picker on the right
						return d
					}
					return p.verdict(gtx, s)
				}),
				gl.Rigid(gl.Spacer{Width: 16}.Layout),
				gl.Rigid(p.segmented("stats-window", []string{week, month, ever}, win, func(o string) {
					p.st.window = o
					p.readStats()
				})),
			)
		}))
		if err != "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, p.sp(12), th.Red, err)
			}))
		}
		if note == "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 24}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.tiles(gtx, s) }))
		}
		d := gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
		// After the window picker, which may have started a read.
		p.st.mu.Lock()
		busy := p.st.loading
		p.st.mu.Unlock()
		if busy {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
		}
		return d
	}
	secs := []section{{rows: []row{{bare: true, control: head}}}}
	if note != "" {
		return secs
	}

	if a := s.Approvals; len(a.Shown)+len(a.Held)+a.Unknown > 0 {
		secs = append(secs, section{title: "Approvals",
			desc: "pitwall hides the suggestion on a share of permission prompts at random (holdout in config.toml), so the two can be compared.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.approvalsPanel(gtx, a) }}}})
	}

	peak := s.Days[0]
	for _, d := range s.Days {
		if d.Calls > peak.Calls {
			peak = d
		}
	}
	secs = append(secs, section{title: "Calls per day",
		desc: fmt.Sprintf("Most in a day: %s, on %s.", count(peak.Calls), peak.Date.Format("Jan 2")),
		rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.dayChart(gtx, s.Days) }}}})

	if t := s.Turns; t.CheckN+t.DoneN > 0 {
		rows := [][]string{
			{"Marked Check", fmt.Sprintf("%d of %d", t.Check, t.CheckN), decisionlog.Pct(t.Check, t.CheckN)},
			{"Marked Done", fmt.Sprintf("%d of %d", t.Done, t.DoneN), decisionlog.Pct(t.Done, t.DoneN)},
		}
		if t.CheckN > 0 && t.DoneN > 0 {
			rows = append(rows, []string{"Lift", "", fmt.Sprintf("%+.0f points", t.Lift())})
		}
		secs = append(secs, section{title: "Turn check",
			desc: fmt.Sprintf("Finished turns you prompted again within %d minutes, by what the model said. Check should be higher.", int(decisionlog.FollowUp.Minutes())),
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.table(gtx, nil, rows) }}}})
	}

	if t := s.Triage; len(t.Levels) > 0 || t.Unfocused > 0 {
		var rows [][]string
		for _, l := range t.Levels {
			name := strings.ToUpper(l.Name[:1]) + l.Name[1:]
			if l.Name == "fyi" {
				name = "FYI"
			}
			rows = append(rows, []string{name, count(l.N), decisionlog.Secs(l.Median)})
		}
		if t.Unfocused > 0 {
			rows = append(rows, []string{"Not focused before the pane moved on", count(t.Unfocused), ""})
		}
		secs = append(secs, section{title: "Triage",
			desc: "How soon you focused a pane, by the urgency the model gave it. Now should be fastest.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions {
				return p.table(gtx, []string{"Urgency", "Panes", "Median to focus"}, rows)
			}}}})
	}
	return secs
}

// verdict is the headline: the finding on answer time, or how many
// answers each arm still needs.
func (p *Page) verdict(gtx gl.Context, s *decisionlog.Stats) gl.Dimensions {
	th := p.th
	a := s.Approvals
	head, dot := "Not enough data yet", th.Muted
	var sub string
	if sp := s.Speed(); sp != "" {
		head = strings.ToUpper(sp[:1]) + sp[1:]
		switch {
		case a.Hi < 0:
			dot = th.Green
		case a.Lo > 0:
			dot = th.Red
		}
		sub = fmt.Sprintf("From %s answered approvals over %d days. The model matched your answer on %s.",
			count(len(a.Shown)+len(a.Held)), s.SpanDays(), decisionlog.Pct(a.Agree, a.N))
	} else {
		sub = fmt.Sprintf("A verdict needs %d answered approvals with the suggestion shown and %d held out. %s",
			decisionlog.MinPerArm, decisionlog.MinPerArm, toGo(len(a.Shown), len(a.Held)))
		if p.s.Decisions.Holdout <= 0 {
			sub += " With holdout = 0 under [decisions.approvals], nothing is held out."
		}
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			f, size := weight(th.UIFont, font.SemiBold), p.sp(24)
			return gl.Flex{}.Layout(gtx,
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					// the dot on the first line's middle, however many lines wrap
					m := op.Record(gtx.Ops)
					line := p.text(gtx, f, size, th.Fg, "X").Size.Y
					m.Stop()
					d := gtx.Dp(10)
					rrect(gtx, dot, image.Rect(0, (line-d)/2, d, (line+d)/2), d/2)
					return gl.Dimensions{Size: image.Pt(d, line)}
				}),
				gl.Rigid(gl.Spacer{Width: 10}.Layout),
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return p.para(gtx, f, size, th.Fg, head) }),
			)
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.sp(13), th.Muted, sub)
		}),
	)
}

// toGo says how many answers each arm still needs: "3 more held out to
// go."
func toGo(shown, held int) string {
	var parts []string
	if n := decisionlog.MinPerArm - shown; n > 0 {
		parts = append(parts, fmt.Sprintf("%d more with it shown", n))
	}
	if n := decisionlog.MinPerArm - held; n > 0 {
		parts = append(parts, fmt.Sprintf("%d more held out", n))
	}
	return strings.Join(parts, " and ") + " to go."
}

// tiles are the headline numbers: calls, cost, cost per day, latency.
func (p *Page) tiles(gtx gl.Context, s *decisionlog.Stats) gl.Dimensions {
	failed := 0
	for _, f := range s.Features {
		failed += f.Failed
	}
	days := s.SpanDays()
	type tile struct{ label, value, sub string }
	ts := []tile{
		{"Calls", count(s.Calls), fmt.Sprintf("%s failed", decisionlog.Pct(failed, s.Calls))},
		{"Total cost", decisionlog.Money(s.Dollars), count(s.Tokens) + " input tokens"},
		{"Cost per day", decisionlog.Money(s.Dollars / float64(days)), fmt.Sprintf("over %d days", days)},
		{"Median latency", fmt.Sprintf("%.0f ms", s.P50), "answered calls"},
	}
	w := gtx.Constraints.Max.X
	cols := 4
	if w < gtx.Dp(560) {
		cols = 2
	}
	gap := gtx.Dp(12)
	cw := (w - gap*(cols-1)) / cols
	y, rowH := 0, 0
	for i, t := range ts {
		if i > 0 && i%cols == 0 {
			y, rowH = y+rowH+gap, 0
		}
		o := op.Offset(image.Pt((i%cols)*(cw+gap), y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(cw, gtx.Constraints.Max.Y))
		g.Constraints.Min.Y = 0
		d := p.panel(g, func(gtx gl.Context) gl.Dimensions {
			th := p.th
			return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
				gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.text(gtx, th.UIFont, p.sp(12), th.Muted, t.label) }),
				gl.Rigid(gl.Spacer{Height: 6}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, weight(th.UIFont, font.SemiBold), p.sp(22), th.Fg, t.value)
				}),
				gl.Rigid(gl.Spacer{Height: 2}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.text(gtx, th.UIFont, p.sp(12), th.Muted, t.sub) }),
			)
		})
		o.Pop()
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// panel is a settings card around w, the full width it is given.
func (p *Page) panel(gtx gl.Context, w gl.Widget) gl.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return boxed(gtx, p.th.SurfaceSecondary, p.th.Border, gtx.Dp(8), image.Pt(gtx.Dp(16), gtx.Dp(14)), w)
}

// approvalsPanel sets the two holdout arms side by side, with the median
// difference under them.
func (p *Page) approvalsPanel(gtx gl.Context, a decisionlog.Approvals) gl.Dimensions {
	th := p.th
	med := func(xs []float64) string {
		if len(xs) == 0 {
			return "-"
		}
		return decisionlog.Secs(decisionlog.Median(xs))
	}
	rows := [][]string{
		{"Answered", count(len(a.Shown)), count(len(a.Held))},
		{"Median time to answer", med(a.Shown), med(a.Held)},
		{"Your answer matched the model", decisionlog.Pct(a.Agree-a.HeldAgree, a.N-a.HeldN), decisionlog.Pct(a.HeldAgree, a.HeldN)},
	}
	note := fmt.Sprintf("The median difference and its 95%% interval appear once each side has %d answers.", decisionlog.MinPerArm)
	if a.Ready() {
		note = fmt.Sprintf("Median difference %+.1fs, 95%% interval %+.1fs to %+.1fs. Below zero is faster with the suggestion shown.", a.Diff, a.Lo, a.Hi)
	}
	if a.Unknown > 0 {
		note += fmt.Sprintf(" Left out: %s with no event showing the answer.", count(a.Unknown))
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.table(gtx, []string{"", "Suggestion shown", "Held out"}, rows)
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.para(gtx, th.UIFont, p.sp(12), th.Muted, note) }),
	)
}

// table draws rows in a card: the first column as muted labels, the rest
// right-aligned values. head, when set, is a muted first row.
func (p *Page) table(gtx gl.Context, head []string, rows [][]string) gl.Dimensions {
	th := p.th
	if head != nil {
		rows = append([][]string{head}, rows...)
	}
	w := gtx.Constraints.Max.X
	rh, pad, r := gtx.Dp(38), gtx.Dp(16), gtx.Dp(8)
	n := len(rows[0])
	colW := min(gtx.Dp(150), (w-2*pad)/n)
	h := len(rows) * rh
	rrect(gtx, th.Border, image.Rect(0, 0, w, h), r)
	rrect(gtx, th.SurfaceSecondary, image.Rect(1, 1, w-1, h-1), r-1)
	for i, cells := range rows {
		y := i * rh
		if i > 0 {
			paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(1, y), Max: image.Pt(w-1, y+1)}.Op())
		}
		for j, cell := range cells {
			col, size, f := th.Fg, p.sp(13), th.UIFont
			switch {
			case head != nil && i == 0:
				col, size, f = th.Muted, p.sp(12), weight(f, font.Medium)
			case j == 0:
				col = th.Muted
			}
			g := gtx
			g.Constraints.Max.X = colW - gtx.Dp(8)
			if j == 0 {
				g.Constraints.Max.X = w - 2*pad - (n-1)*colW
			}
			m := op.Record(gtx.Ops)
			d := p.text(g, f, size, col, cell)
			call := m.Stop()
			x := pad
			if j > 0 {
				x = w - pad - (n-1-j)*colW - d.Size.X
			}
			o := op.Offset(image.Pt(x, y+(rh-d.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
		}
	}
	return gl.Dimensions{Size: image.Pt(w, h)}
}

// dayChart is a bar per day of calls, the first and last day under it.
func (p *Page) dayChart(gtx gl.Context, days []decisionlog.Day) gl.Dimensions {
	th := p.th
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		w := gtx.Constraints.Max.X
		label := func(d decisionlog.Day) gl.Widget {
			return func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, th.UIFont, p.sp(11), th.Muted, d.Date.Format("Jan 2"))
			}
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				h := gtx.Dp(96)
				bars(gtx, days, image.Pt(w, h), th.Primary, theme.Mix(th.SurfaceSecondary, th.Border, 0.6), th.Border)
				return gl.Dimensions{Size: image.Pt(w, h)}
			}),
			gl.Rigid(gl.Spacer{Height: 6}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				gtx.Constraints.Min.X = w
				return gl.Flex{}.Layout(gtx,
					gl.Rigid(label(days[0])),
					gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					gl.Rigid(label(days[len(days)-1])),
				)
			}),
		)
	})
}

// bars fills one bar per day in sz, scaled to the busiest day, over a
// top grid line and a baseline.
func bars(gtx gl.Context, days []decisionlog.Day, sz image.Point, fill, grid, base color.NRGBA) {
	paint.FillShape(gtx.Ops, grid, clip.Rect{Max: image.Pt(sz.X, 1)}.Op())
	paint.FillShape(gtx.Ops, base, clip.Rect{Min: image.Pt(0, sz.Y-1), Max: sz}.Op())
	peak := 1
	for _, d := range days {
		peak = max(peak, d.Calls)
	}
	slot := float32(sz.X) / float32(len(days))
	bw := max(1, slot*0.6)
	var path clip.Path
	path.Begin(gtx.Ops)
	for i, d := range days {
		if d.Calls == 0 {
			continue
		}
		x := float32(i)*slot + (slot-bw)/2
		top := float32(sz.Y-1) * (1 - float32(d.Calls)/float32(peak))
		path.MoveTo(f32.Pt(x, float32(sz.Y-1)))
		path.LineTo(f32.Pt(x, top))
		path.LineTo(f32.Pt(x+bw, top))
		path.LineTo(f32.Pt(x+bw, float32(sz.Y-1)))
		path.Close()
	}
	paint.FillShape(gtx.Ops, fill, clip.Outline{Path: path.End()}.Op())
}

// count is n with thousands separated: 12,345.
func count(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
