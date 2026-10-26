package settings

import (
	"cmp"
	"fmt"
	"image/color"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/timelog"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// errorsPage is the Agent errors category's state: errors.jsonl over the
// chosen window, grouped off the UI goroutine.
type errorsPage struct {
	path   string // errors.jsonl; tests set it
	window string // week, month or ever; "" is week
	order  string // byCount or byTokens; "" is byCount
	read   bool   // a read has started since the page opened

	mu      sync.Mutex
	gen     int // the latest read; an older one's result is dropped
	loading bool
	sum     *errorSum
	any     bool // the log has an event at all
	err     string
}

// errorSum is one window of errors.jsonl.
type errorSum struct {
	Total     int
	Tokens    int
	Kinds     map[string]int
	Providers map[string]int
	Groups    []errlog.Group
	Days      []errlog.Day
}

// Lines the category shows instead of errors, and how many groups it
// lists and copies.
const (
	errorsEmpty   = "Appears after an agent's command, tool call or turn fails."
	errorsLoading = "Reading the errors log…"
	topGroups     = 15
	byCount       = "Most frequent"
	byTokens      = "Most tokens"
)

// readErrors reads errors.jsonl; tests replace it.
var readErrors = errlog.Read

func (p *Page) errorsPath() string {
	if p.er.path == "" {
		p.er.path = filepath.Join(config.StateDir(), "errors.jsonl")
	}
	return p.er.path
}

// readErrorLog reads the log and groups the window's errors in the
// background. Frames poll for the result while it runs.
func (p *Page) readErrorLog() {
	p.er.read = true
	win, path := p.er.window, p.errorsPath()
	p.er.mu.Lock()
	p.er.gen++
	gen := p.er.gen
	p.er.loading = true
	p.er.mu.Unlock()
	go func() {
		evs, err := readErrors(path, time.Time{})
		s := errorWindow(evs, win, time.Now())
		p.er.mu.Lock()
		if gen == p.er.gen {
			p.er.sum, p.er.any, p.er.loading, p.er.err = &s, len(evs) > 0, false, ""
			if err != nil {
				p.er.err = err.Error()
			}
		}
		p.er.mu.Unlock()
	}()
}

// errorWindow counts and groups the events in win up to now. evs is
// oldest first.
func errorWindow(evs []errlog.Event, win string, now time.Time) errorSum {
	first := now
	if len(evs) > 0 {
		first = evs[0].T
	}
	from := windowFrom(win, now, first)
	s := errorSum{Kinds: map[string]int{}, Providers: map[string]int{}}
	var in []errlog.Event
	for _, e := range evs {
		if !e.T.Before(from) {
			in = append(in, e)
			s.Total++
			s.Tokens += e.Tokens()
			s.Kinds[e.Kind]++
			s.Providers[e.Provider]++
		}
	}
	s.Groups = errlog.Groups(in)
	s.Days = errlog.Days(in, from, now, time.Local)
	return s
}

// errorsNote is the line shown instead of errors, "" when there are some.
func errorsNote(s *errorSum, any, loading bool, win string) string {
	switch {
	case s == nil && loading:
		return errorsLoading
	case s == nil || !any:
		return errorsEmpty
	case s.Total == 0 && win != ever:
		return "No errors in the last " + win + "."
	}
	return ""
}

// errorKinds are the kinds of error and what one is called, in the
// order the charts stack them.
var errorKinds = [][2]string{{errlog.Command, "command"}, {errlog.Tool, "tool call"}, {errlog.API, "API error"}}

// errorSplit is the totals by kind and by agent: "9 commands, 1 API
// error · Claude Code 6, Codex 4".
func errorSplit(s *errorSum) string {
	var kinds []string
	for _, k := range errorKinds {
		if n := s.Kinds[k[0]]; n > 0 {
			kinds = append(kinds, plural(n, k[1]))
		}
	}
	pvs := slices.Collect(maps.Keys(s.Providers))
	slices.SortFunc(pvs, func(a, b string) int { return cmp.Or(s.Providers[b]-s.Providers[a], strings.Compare(a, b)) })
	var provs []string
	for _, pv := range pvs {
		provs = append(provs, agentName(model.Provider(pv))+" "+count(s.Providers[pv]))
	}
	return strings.Join(kinds, ", ") + " · " + strings.Join(provs, ", ")
}

// span names win for the report.
func span(win string) string {
	if win == ever {
		return "all time"
	}
	return "the last " + win
}

func (p *Page) agentErrors() []section {
	th := p.th
	p.er.mu.Lock()
	s, any, loading, err := p.er.sum, p.er.any, p.er.loading || !p.er.read, p.er.err
	p.er.mu.Unlock()
	win := p.er.window
	if win == "" {
		win = week
	}
	note := errorsNote(s, any, loading, win)

	head := func(gtx gl.Context) gl.Dimensions {
		if !p.er.read {
			p.readErrorLog()
		}
		if note == errorsLoading || note == errorsEmpty {
			d := p.blank(gtx, note == errorsLoading, "wrench", note, nil)
			p.pollErrors(gtx)
			return d
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []gl.FlexChild{gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Alignment: gl.Start}.Layout(gtx,
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
					if note != "" {
						d := gl.Inset{Top: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
							return p.para(gtx, th.UIFont, p.th.Sp(theme.Large), th.Muted, note)
						})
						d.Size.X = gtx.Constraints.Max.X // keeps the window picker on the right
						return d
					}
					return p.errorTotals(gtx, s, win)
				}),
				gl.Rigid(gl.Spacer{Width: 16}.Layout),
				gl.Rigid(p.segmented("errors-window", []string{week, month, ever}, win, func(o string) {
					p.er.window = o
					p.readErrorLog()
				})),
			)
		})}
		if err != "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Red, err)
			}))
		}
		if note == "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 24}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.errorTiles(gtx, s) }))
		}
		d := gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
		p.pollErrors(gtx)
		return d
	}
	secs := []section{{rows: []row{{bare: true, control: head}}}}
	if note != "" {
		return secs
	}
	causes := p.errorCauses(s)
	order, gs := p.errorOrder(s)
	var rows []row
	for _, g := range gs[:min(topGroups, len(gs))] {
		rows = append(rows, row{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.errorGroup(gtx, g, causes) }})
	}
	secs = append(secs,
		section{title: "Errors per day",
			desc: "Each day's errors by kind. Hover a day for its counts and tokens.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.errorDays(gtx, s) }}}},
		section{title: "Where errors come from",
			desc: "The most frequent causes and their share of the errors, the rest together. Hover a cause to pick out its group below.",
			rows: []row{{bare: true, control: func(gtx gl.Context) gl.Dimensions {
				if len(causes) == 0 {
					return p.chartEmpty(gtx, "No errors in this window.")
				}
				return p.splitBar(gtx, "cause", causes, int64(s.Total), 1)
			}}}},
	)
	return append(secs, section{title: order,
		desc: "Errors grouped by the line that says what went wrong, with numbers, paths and quoted text set aside. Each shows its latest example. " +
			"Tokens are about what the commands and output took, at four characters a token. Commands and output are redacted before they are saved.",
		rows: rows})
}

// errorTotals is the headline: how many errors, split by kind and agent,
// and the Copy report button.
func (p *Page) errorTotals(gtx gl.Context, s *errorSum, win string) gl.Dimensions {
	th := p.th
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, weight(th.UIFont, font.SemiBold), p.sp(24), th.Fg, plural(s.Total, "error"))
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, errorSplit(s)+" · about "+compact(int64(s.Tokens))+" tokens")
		}),
		gl.Rigid(gl.Spacer{Height: 14}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			order, gs := p.errorOrder(s)
			report := func() string { return errlog.Report(gs, topGroups, span(win), strings.ToLower(order)+" first") }
			c, ask := p.btn("errors-copy"), p.btn("errors-ask")
			for c.Clicked(gtx) {
				p.copy(gtx, "errors-copy", report())
			}
			for ask.Clicked(gtx) {
				p.prompt, p.result = errlog.Ask(report()), AskAgent
			}
			return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.button(gtx, c, secondary, p.copyLabel("errors-copy", "Copy report"))
				}),
				gl.Rigid(gl.Spacer{Width: 8}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.button(gtx, ask, secondary, "Ask an agent to fix these")
				}),
				gl.Rigid(gl.Spacer{Width: 12}.Layout),
				gl.Rigid(p.segmented("errors-order", []string{byCount, byTokens}, cmp.Or(p.er.order, byCount), func(o string) { p.er.order = o })),
			)
		}),
	)
}

// errorOrder is the order the groups are listed in, and the groups so.
func (p *Page) errorOrder(s *errorSum) (string, []errlog.Group) {
	if p.er.order == byTokens {
		return byTokens, errlog.ByTokens(s.Groups)
	}
	return byCount, s.Groups
}

// errorGroup is one group's card: its signature and count, the agents
// and when last seen, a likely fix when one is known, then its latest
// command and the end of its output. Its border takes its cause's color while the cause chart picks it out.
func (p *Page) errorGroup(gtx gl.Context, g errlog.Group, causes []share) gl.Dimensions {
	th := p.th
	p.focusFrame(gtx)
	border := th.Border
	if i := slices.IndexFunc(causes, func(c share) bool { return c.name == g.Sig }); i >= 0 && p.focused("cause", g.Sig) {
		border = causes[i].col
	}
	e, fix := g.Example, errlog.LikelyFix(g)
	var names []string
	for _, pv := range g.Providers {
		names = append(names, agentName(model.Provider(pv)))
	}
	meta := strings.Join(append(names, "about "+compact(int64(g.Tokens))+" tokens", ago(gtx.Now.Sub(g.Last))), " · ")
	cmd := e.Command
	if e.Kind == errlog.Tool {
		cmd = strings.TrimSpace(e.Tool + " " + cmd)
	} else if cmd != "" {
		cmd = "$ " + cmd
	}
	var out []string
	for l := range strings.Lines(e.Output) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	out = out[max(0, len(out)-3):]
	mono := func(col color.NRGBA, s string) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.text(gtx, th.MonoFont, p.th.Sp(theme.Small), col, s) })
	}
	return gl.Inset{Bottom: 8}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return p.framed(gtx, border, func(gtx gl.Context) gl.Dimensions {
			kids := []gl.FlexChild{
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return gl.Flex{Alignment: gl.Start}.Layout(gtx,
						gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
							d := p.para(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), th.Fg, g.Sig)
							d.Size.X = gtx.Constraints.Max.X // keeps the count on the right
							return d
						}),
						gl.Rigid(gl.Spacer{Width: 12}.Layout),
						gl.Rigid(func(gtx gl.Context) gl.Dimensions {
							return p.text(gtx, weight(th.UIFont, font.SemiBold), p.th.Sp(theme.Body), th.Fg, "×"+count(g.Count))
						}),
					)
				}),
				gl.Rigid(gl.Spacer{Height: 2}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, meta)
				}),
			}
			if fix != "" {
				kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return gl.Flex{Alignment: gl.Baseline}.Layout(gtx,
						gl.Rigid(func(gtx gl.Context) gl.Dimensions {
							return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Small), th.Fg, "Likely fix")
						}),
						gl.Rigid(gl.Spacer{Width: 8}.Layout),
						gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
							return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Fg, fix)
						}),
					)
				}))
			}
			if cmd != "" || len(out) > 0 {
				kids = append(kids, gl.Rigid(gl.Spacer{Height: 10}.Layout))
			}
			if cmd != "" {
				kids = append(kids, mono(th.Fg, cmd))
			}
			for _, l := range out {
				kids = append(kids, mono(th.Muted, l))
			}
			return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
		})
	})
}

// topCauses is how many causes the cause chart names; the rest share a
// part.
const topCauses = 4

// errorCauses are the cause chart's parts: the most frequent groups, then
// the rest, each colored in an order whose neighbors stay apart for
// color-blind readers. They take the hues the kinds of error leave.
func (p *Page) errorCauses(s *errorSum) []share {
	th := p.th
	cols := []color.NRGBA{th.Green, theme.Mix(th.Red, th.Purple, 0.5), th.Yellow, theme.Mix(th.Green, th.Blue, 0.6)}
	top, rest := errlog.Top(s.Groups, topCauses)
	part := func(name string, col color.NRGBA, n, tokens int) share {
		pct := timelog.Pct(int64(n), int64(s.Total))
		return share{name, col, int64(n), pct + " · " + plural(n, "error"),
			[]string{name, plural(n, "error") + " · " + pct, "about " + compact(int64(tokens)) + " tokens"}}
	}
	var out []share
	for i, g := range top {
		out = append(out, part(g.Sig, cols[i], g.Count, g.Tokens))
	}
	if rest.Count > 0 {
		out = append(out, part("Other causes", theme.Mix(th.SurfaceSecondary, th.Muted, 0.75), rest.Count, rest.Tokens))
	}
	return out
}

// errorKindColors are the kinds' colors, in errorKinds' order: the first
// hues of Timing's order, so neighbors stay apart.
func (p *Page) errorKindColors() []color.NRGBA {
	th := p.th
	return []color.NRGBA{th.Blue, theme.Mix(th.Red, th.Yellow, 0.5), th.Purple}
}

// errorTiles are the errors and their causes, the tokens they took, the
// errors on a day with any, and the top cause's share.
func (p *Page) errorTiles(gtx gl.Context, s *errorSum) gl.Dimensions {
	active := 0
	for _, d := range s.Days {
		if d.Count > 0 {
			active++
		}
	}
	perDay, per := "0", "none"
	if active > 0 {
		perDay = strconv.FormatFloat(float64(s.Total)/float64(active), 'f', 1, 64)
	}
	if s.Total > 0 {
		per = "about " + compact(int64(s.Tokens/s.Total)) + " an error"
	}
	top := tile{"Top cause", "-", "no errors"}
	if len(s.Groups) > 0 {
		g := s.Groups[0]
		top = tile{"Top cause", timelog.Pct(int64(g.Count), int64(s.Total)), g.Sig}
	}
	return p.tileGrid(gtx, []tile{
		{"Errors", count(s.Total), "from " + plural(len(s.Groups), "cause")},
		{"Tokens wasted", compact(int64(s.Tokens)), per},
		{"Per active day", perDay, fmt.Sprintf("errors, on %d of %s", active, plural(len(s.Days), "day"))},
		top,
	})
}

// errorDays is a bar per day of the errors, stacked by kind with
// commands at the base, and a legend of the kinds.
func (p *Page) errorDays(gtx gl.Context, s *errorSum) gl.Dimensions {
	c := dayChart{id: "kind", per: 1, step: 1, label: func(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }}
	totals := make([]int, len(errorKinds))
	for _, d := range s.Days {
		vs := make([]int64, len(errorKinds))
		for j, k := range errorKinds {
			vs[j] = int64(d.Kinds[k[0]])
			totals[j] += d.Kinds[k[0]]
		}
		c.days, c.vals = append(c.days, d.Date), append(c.vals, vs)
	}
	cols := p.errorKindColors()
	var legend []share
	for j, k := range errorKinds {
		sh := share{name: strings.ToUpper(k[1][:1]) + k[1][1:] + "s", col: cols[j]}
		c.series = append(c.series, sh)
		if totals[j] > 0 {
			sh.legend = timelog.Pct(int64(totals[j]), int64(s.Total)) + " · " + count(totals[j])
			legend = append(legend, sh)
		}
	}
	if s.Total == 0 {
		return p.chartEmpty(gtx, "No errors in this window.")
	}
	c.tip = func(i int) []string {
		d := s.Days[i]
		lines := []string{d.Date.Format("Mon Jan 2") + " · " + plural(d.Count, "error")}
		for _, k := range errorKinds {
			if n := d.Kinds[k[0]]; n > 0 {
				lines = append(lines, plural(n, k[1]))
			}
		}
		return append(lines, "about "+compact(int64(d.Tokens))+" tokens")
	}
	p.focusFrame(gtx)
	defer p.focusMoved(gtx)
	return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.dayBars(gtx, c) }),
			gl.Rigid(gl.Spacer{Height: 16}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.legend(gtx, c.id, legend, 4) }),
		)
	})
}

// pollErrors asks for frames while a read runs.
func (p *Page) pollErrors(gtx gl.Context) {
	p.er.mu.Lock()
	busy := p.er.loading
	p.er.mu.Unlock()
	if busy {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
}
