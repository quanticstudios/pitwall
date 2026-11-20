package panel

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// isSummary is whether it is the server's summary of Claude's words
// between tool calls, which comes as a thinking block and Claude Code
// shows as a line tagged summary: a row of its own, not a thinking row.
func isSummary(it *session.Item) bool {
	return it.Kind == session.KindReasoningText && it.Phase == session.PhaseSummary
}

// summaryRow is a summary in a work log: a Summary tag, then the summary
// as quiet prose, where it came among the turn's calls.
func (d *cdraw) summaryRow(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	txt := strings.TrimSpace(it.Text)
	return layout.Inset{Top: 7, Bottom: 7, Left: rowPad, Right: rowPad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		iw := gtx.Constraints.Max.X
		bg, fg := th.Chip(th.Muted, th.Bg)
		px, tagH := gtx.Dp(theme.SpaceXS+theme.Space2XS), gtx.Dp(18)
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), fg, "Summary")
		})
		tw := ld.Size.X + 2*px
		paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rect(0, 0, tw, tagH), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		at(gtx, image.Pt(px, (tagH-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		x := tw + gtx.Dp(theme.SpaceS)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, iw-x), gtx.Constraints.Max.Y)}
		// The text's first line sits on the tag's middle.
		line := gtx.Sp(th.Sp(theme.Body)) * 145 / 100
		bd := at(g, image.Pt(x, (tagH-line)/2), func(gtx layout.Context) layout.Dimensions {
			return d.selectable(gtx, d.t.sel(it.ID), d.body(th.Muted), txt)
		})
		return layout.Dimensions{Size: image.Pt(iw, max(tagH, bd.Size.Y+(tagH-line)/2))}
	})
}
