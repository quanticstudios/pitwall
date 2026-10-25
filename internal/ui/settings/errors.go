package settings

import (
	"cmp"
	"image/color"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// errorsPage is the Agent errors category's state: errors.jsonl over the
// chosen window, grouped off the UI goroutine.
type errorsPage struct {
	path   string // errors.jsonl; tests set it
	window string // week, month or ever; "" is week
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
	Kinds     map[string]int
	Providers map[string]int
	Groups    []errlog.Group
}

// Lines the category shows instead of errors, and how many groups it
// lists and copies.
const (
	errorsEmpty   = "Appears after an agent's command, tool call or turn fails."
	errorsLoading = "Reading the errors log…"
	topGroups     = 15
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
			s.Kinds[e.Kind]++
			s.Providers[e.Provider]++
		}
	}
	s.Groups = errlog.Groups(in)
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

// errorSplit is the totals by kind and by agent: "9 commands, 1 API
// error · Claude Code 6, Codex 4".
func errorSplit(s *errorSum) string {
	var kinds []string
	for _, k := range [][2]string{{errlog.Command, "command"}, {errlog.Tool, "tool call"}, {errlog.API, "API error"}} {
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
		d := gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
		p.pollErrors(gtx)
		return d
	}
	secs := []section{{rows: []row{{bare: true, control: head}}}}
	if note != "" {
		return secs
	}
	var rows []row
	for _, g := range s.Groups[:min(topGroups, len(s.Groups))] {
		rows = append(rows, row{bare: true, control: func(gtx gl.Context) gl.Dimensions { return p.errorGroup(gtx, g) }})
	}
	return append(secs, section{title: "Most frequent",
		desc: "Errors grouped by the line that says what went wrong, with numbers, paths and quoted text set aside. Each shows its latest example. Commands and output are redacted before they are saved.",
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
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, errorSplit(s))
		}),
		gl.Rigid(gl.Spacer{Height: 14}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			c := p.btn("errors-copy")
			for c.Clicked(gtx) {
				p.copy(gtx, "errors-copy", errlog.Report(s.Groups, topGroups, span(win)))
			}
			return p.button(gtx, c, secondary, p.copyLabel("errors-copy", "Copy report"))
		}),
	)
}

// errorGroup is one group's card: its signature and count, the agents
// and when last seen, then its latest command and the end of its output.
func (p *Page) errorGroup(gtx gl.Context, g errlog.Group) gl.Dimensions {
	th := p.th
	e := g.Example
	var names []string
	for _, pv := range g.Providers {
		names = append(names, agentName(model.Provider(pv)))
	}
	meta := strings.Join(append(names, ago(gtx.Now.Sub(g.Last))), " · ")
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
		return p.panel(gtx, func(gtx gl.Context) gl.Dimensions {
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

// pollErrors asks for frames while a read runs.
func (p *Page) pollErrors(gtx gl.Context) {
	p.er.mu.Lock()
	busy := p.er.loading
	p.er.mu.Unlock()
	if busy {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
}
