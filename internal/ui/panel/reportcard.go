package panel

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// reportRows adds, from Reports[*next] on, the worker cards that came
// before content item i, or all that are left when i is past the end.
func (t *Transcript) reportRows(s *session.Snapshot, next *int, i int) {
	for ; *next < len(t.Reports); *next++ {
		if i < len(s.Content) {
			if at := itemTime(&s.Content[i]); at.IsZero() || t.Reports[*next].At.After(at) {
				return
			}
		}
		t.rows = append(t.rows, trow{rep: &t.Reports[*next]})
	}
}

// reportsKey changes when a card comes in.
func (t *Transcript) reportsKey() string {
	n := len(t.Reports)
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n) + "/" + t.Reports[n-1].Key()
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

// reportCard draws worker report r as wide as the column: a status dot,
// the worker's name, its pull request and the time, then the status and
// a line or two of the report. A click opens the worker's tab, or, once
// that tab is gone, the report in place.
func (d *cdraw) reportCard(gtx layout.Context, r *model.Report) layout.Dimensions {
	th, t := d.th, d.t
	key := "report:" + r.Key()
	c := t.click(key)
	for c.Clicked(gtx) {
		if t.OpenReport == nil || !t.OpenReport(*r) {
			t.open[key] = !t.open[key]
		}
	}
	open := t.open[key] && r.Body != ""
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS+theme.Space2XS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	small, body := th.Sp(theme.Small), th.Sp(theme.Body)
	col := reportColor(th, r.Status)
	head := func(gtx layout.Context) layout.Dimensions {
		dot := gtx.Dp(7)
		return row(gtx,
			part{w: func(gtx layout.Context) layout.Dimensions {
				lh := gtx.Sp(body) * 5 / 4
				y := (lh - dot) / 2
				paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: image.Pt(0, y), Max: image.Pt(dot, y+dot)}.Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(dot, lh)}
			}},
			part{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, semibold(th.UIFont), body, th.Fg, r.Name, 1)
			}, flex: true, gap: gtx.Dp(theme.SpaceS)},
			part{w: func(gtx layout.Context) layout.Dimensions { return d.reportTag(gtx, r) }, gap: gtx.Dp(theme.SpaceS)},
			part{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, small, th.Muted, r.At.Local().Format("15:04"), 1)
			}, gap: gtx.Dp(theme.SpaceS)},
		)
	}
	status := func(gtx layout.Context) layout.Dimensions {
		return row(gtx,
			part{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, semibold(th.UIFont), body, col, r.Status.Label(), 1)
			}},
			part{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, body, th.Muted, r.Line, 2)
			}, flex: true, gap: gtx.Dp(theme.SpaceS)},
		)
	}
	ws := []layout.Widget{head, status}
	if open {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, body, th.Fg, r.Body, 24)
			})
		})
	}
	call, cd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceXS), ws...)
	})
	size := image.Pt(w, cd.Size.Y+2*py)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		fill := th.Surface
		switch {
		case c.Pressed():
			fill = th.Pressed
		case c.Hovered():
			fill = th.Hover
		}
		rrect(gtx, image.Rectangle{Max: size}, gtx.Dp(10), fill, th.BorderSubtle)
		at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return cd })
		return layout.Dimensions{Size: size}
	})
}

// reportTag is the card's pull request pill, "PR #566"; none without one.
func (d *cdraw) reportTag(gtx layout.Context, r *model.Report) layout.Dimensions {
	if r.PR == 0 {
		return layout.Dimensions{}
	}
	th := d.th
	px := gtx.Dp(theme.SpaceS - theme.Space2XS)
	call, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "PR #"+strconv.Itoa(r.PR), 1)
	})
	size := image.Pt(ld.Size.X+2*px, ld.Size.Y)
	kit.Border(gtx, image.Rectangle{Max: size}, size.Y/2, gtx.Dp(1), th.BorderSubtle)
	at(gtx, image.Pt(px, 0), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return ld })
	return layout.Dimensions{Size: size}
}
