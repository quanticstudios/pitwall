package settings

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Windows of the Agent errors and Timing categories.
const (
	week  = "7 days"
	month = "30 days"
	ever  = "All time"
)

// windowFrom is when win starts: first, the oldest event, for all time.
func windowFrom(win string, now, first time.Time) time.Time {
	switch win {
	case month:
		return now.AddDate(0, 0, -30)
	case ever:
		return first
	}
	return now.AddDate(0, 0, -7)
}

// tile is one headline number: a muted label, the value and a line under.
type tile struct{ label, value, sub string }

// tileGrid sets ts out in four columns, two when narrow.
func (p *Page) tileGrid(gtx gl.Context, ts []tile) gl.Dimensions {
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
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, t.label)
				}),
				gl.Rigid(gl.Spacer{Height: 6}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, weight(th.UIFont, font.SemiBold), p.sp(22), th.Fg, t.value)
				}),
				gl.Rigid(gl.Spacer{Height: 2}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, t.sub)
				}),
			)
		})
		o.Pop()
		rowH = max(rowH, d.Size.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// panel is a settings card around w, the full width it is given.
func (p *Page) panel(gtx gl.Context, w gl.Widget) gl.Dimensions { return p.framed(gtx, p.th.Border, w) }

// framed is a panel with a border of col.
func (p *Page) framed(gtx gl.Context, col color.NRGBA, w gl.Widget) gl.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return boxed(gtx, p.th.SurfaceSecondary, col, gtx.Dp(8), image.Pt(gtx.Dp(16), gtx.Dp(14)), w)
}

// count is n with thousands separated: 12,345.
func count(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
