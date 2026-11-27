package mobile

import (
	"image/color"
	"strconv"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// reportSeqs places each worker card the app has seen, by its key, under
// the newest entry its chat had then: the phone's log has no times, so a
// card sits where it came in. It lasts while the app runs.
var reportSeqs = map[string]int64{}

// reportState is the open chat's worker cards: their taps, which are
// open in place, and which go under each entry, by its seq.
type reportState struct {
	click map[string]*widget.Clickable
	open  map[string]bool
	under map[int64][]model.Report
}

// reportsMark places the tab's cards this frame: a card new to the app
// goes under the newest entry loaded.
func (c *chatView) reportsMark(ce chatEnv) {
	c.reps.under = nil
	n := len(c.data.Entries)
	if !ce.Open || n == 0 {
		return
	}
	for _, r := range ce.Tab.Reports {
		seq, ok := reportSeqs[r.Key()]
		if !ok {
			seq = c.data.Entries[n-1].Seq
			reportSeqs[r.Key()] = seq
		}
		if c.reps.under == nil {
			c.reps.under = map[int64][]model.Report{}
		}
		c.reps.under[seq] = append(c.reps.under[seq], r)
	}
}

// withReports draws list row i, then the cards that go under it when it
// is an entry.
func (c *chatView) withReports(gtx C, th *theme.Theme, ce chatEnv, i int, w layout.Widget) D {
	j := i - 1
	if c.data.None {
		j--
	}
	if c.data.Gone {
		j--
	}
	if j < 0 || j >= len(c.data.Entries) || len(c.reps.under[c.data.Entries[j].Seq]) == 0 {
		return w(gtx)
	}
	kids := []layout.FlexChild{layout.Rigid(w)}
	for _, r := range c.reps.under[c.data.Entries[j].Seq] {
		kids = append(kids, layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D { return c.reportCard(gtx, th, ce, r) })
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// reportColor is a status's color: Done green, In progress blue, Blocked red.
func reportColor(th *theme.Theme, s model.ReportStatus) color.NRGBA {
	switch s {
	case model.ReportDone:
		return th.Green
	case model.ReportBlocked:
		return th.Red
	}
	return th.Blue
}

// reportCard is worker report r: a status dot, the worker's name and its
// pull request, then the status and a line or two of the report. A tap
// opens the worker's tab, or, once that tab is gone, the report in place.
func (c *chatView) reportCard(gtx C, th *theme.Theme, ce chatEnv, r model.Report) D {
	key := r.Key()
	if c.reps.click == nil {
		c.reps.click, c.reps.open = map[string]*widget.Clickable{}, map[string]bool{}
	}
	b := c.reps.click[key]
	if b == nil {
		b = new(widget.Clickable)
		c.reps.click[key] = b
	}
	for b.Clicked(gtx) {
		if ce.OpenTab == nil || !ce.OpenTab(r.Tab) {
			c.reps.open[key] = !c.reps.open[key]
		}
	}
	col := reportColor(th, r.Status)
	fill := th.Surface
	if b.Pressed() {
		fill = th.Pressed
	}
	return kit.Clickable(gtx, b, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Dp(48)
		return box(gtx, fill, th.BorderSubtle, 12, layout.Inset{Left: 14, Right: 14, Top: 11, Bottom: 12}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			kids := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return circle(gtx, col, 8) }),
						layout.Rigid(layout.Spacer{Width: 9}.Layout),
						layout.Flexed(1, func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, th.Fg, 1, r.Name)
						}),
						layout.Rigid(func(gtx C) D {
							if r.PR == 0 {
								return D{}
							}
							return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
								return label(gtx, th, th.UIFont, 13.5, th.Muted, 1, "PR #"+strconv.Itoa(r.PR))
							})
						}),
					)
				}),
				layout.Rigid(gap(4)),
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 14.5, col, 1, r.Status.Label())
						}),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, 14.5, th.Muted, 2, r.Line) }),
					)
				}),
			}
			if c.reps.open[key] && r.Body != "" {
				kids = append(kids, layout.Rigid(gap(8)), layout.Rigid(func(gtx C) D {
					return label(gtx, th, th.UIFont, 14.5, th.Fg, 30, r.Body)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
	})
}

// openWorker opens the chat of tab on machine m after this frame, the
// chat it leaves drawing to its end; false when the tab is gone.
func (a *App) openWorker(m, tab string) bool {
	id := phone.NS(m, tab)
	if _, _, ok := a.fleet.Tab(id); !ok {
		return false
	}
	a.post(func() {
		a.leaveChat()
		a.openChat(id)
	})
	return true
}

// demoReports are three workers' cards, Done, Blocked and In progress,
// the first two under entry 4, the last under entry 6.
func demoReports(now time.Time) []model.Report {
	reps := []model.Report{
		{Tab: "w1", Pane: "a1", Name: "phone-polish", PR: 566, Status: model.ReportDone, Turn: 1, At: now.Add(-20 * time.Minute),
			Line: "4 gaps fixed and 11 found in the audit. Suite green with the network blocked."},
		{Tab: "w2", Pane: "a2", Name: "since-you-left", Status: model.ReportBlocked, Turn: 2, At: now.Add(-16 * time.Minute),
			Line: "Needs the card's placement before it builds the UI."},
		{Tab: "w3", Pane: "a3", Name: "chat-type", Status: model.ReportProgress, Turn: 1, At: now.Add(-2 * time.Minute),
			Line: "Reply prose moved to the 14 sp scale; tool rows next."},
	}
	for i, seq := range []int64{4, 4, 6} {
		reportSeqs[reps[i].Key()] = seq
	}
	return reps
}
