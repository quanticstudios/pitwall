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
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/timelog"
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

// split is one stacked bar of the window's turn time by part, with a
// legend of each part's share and hours.
func (p *Page) split(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	total := s.Agent + s.Wait
	var ss []share
	for _, pt := range p.timeParts() {
		v := pt.of(s.Parts)
		if v <= 0 {
			continue
		}
		tip := []string{pt.name, timelog.Hours(v) + " · " + timelog.Pct(v, total) + " of turn time"}
		if len(pt.kinds) > 1 {
			for _, k := range pt.kinds {
				if v := s.Parts[k]; v > 0 {
					tip = append(tip, timelog.Label(k)+" "+timelog.Hours(v))
				}
			}
		}
		ss = append(ss, share{pt.name, pt.col, v, timelog.Pct(v, total) + " · " + timelog.Hours(v), tip})
	}
	if total <= 0 || len(ss) == 0 {
		return p.chartEmpty(gtx, "The turns in this window took no measurable time.")
	}
	return p.splitBar(gtx, "time", ss, total, 4)
}

// dayHours is a bar per day of the agents' hours, stacked by part with
// the model at the base, on hour gridlines. Hovering a day shows its
// hours.
func (p *Page) dayHours(gtx gl.Context, s *timelog.Summary) gl.Dimensions {
	ps := p.timeParts()
	ps = ps[:len(ps)-1] // agent time: no waiting on you
	c := dayChart{id: "time", per: 3.6e6,
		label: func(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + "h" }}
	for _, pt := range ps {
		c.series = append(c.series, share{name: pt.name, col: pt.col})
	}
	var peak int64
	for _, d := range s.Days {
		vs := make([]int64, len(ps))
		var a int64
		for j, pt := range ps {
			vs[j] = pt.of(d.Parts)
			a += vs[j]
		}
		peak = max(peak, a)
		c.days, c.vals = append(c.days, d.Date), append(c.vals, vs)
	}
	if peak <= 0 {
		return p.chartEmpty(gtx, "No agent time in this window.")
	}
	c.tip = func(i int) []string {
		var a int64
		for _, v := range c.vals[i] {
			a += v
		}
		lines := []string{c.days[i].Format("Mon Jan 2") + " · " + timelog.Hours(a)}
		type pv struct {
			name string
			v    int64
		}
		var top []pv
		for j, pt := range ps {
			if v := c.vals[i][j]; v > 0 {
				top = append(top, pv{pt.name, v})
			}
		}
		slices.SortStableFunc(top, func(a, b pv) int { return cmp.Compare(b.v, a.v) })
		for _, t := range top[:min(4, len(top))] {
			lines = append(lines, t.name+" "+timelog.Hours(t.v)+" · "+timelog.Pct(t.v, a))
		}
		return lines
	}
	p.focusFrame(gtx)
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions { return p.dayBars(gtx, c) })
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
