package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Loading is a view's loading state, centred in the width: a pulsing
// Primary dot before a muted line such as "Reading git…".
func Loading(gtx layout.Context, th *theme.Theme, line string) layout.Dimensions {
	w := gtx.Constraints.Max.X
	d, gap := gtx.Dp(6), gtx.Dp(theme.SpaceS)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-d-gap), gtx.Constraints.Max.Y)}
	td := widget.Label{MaxLines: 1}.Layout(g, th.Shaper, th.UIFont, th.Sp(theme.Body), line, colorOp(gtx, th.Muted))
	call := m.Stop()
	x := (w - d - gap - td.Size.X) / 2
	dot := image.Rect(x, (td.Size.Y-d)/2, x+d, (td.Size.Y+d)/2)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Primary, anim.Pulse(gtx)), clip.Ellipse(dot).Op(gtx.Ops))
	o := op.Offset(image.Pt(x+d+gap, 0)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, td.Size.Y)}
}

// IconFunc draws an icon in a size box in col.
type IconFunc func(gtx layout.Context, size int, col color.NRGBA) layout.Dimensions

// Empty is a view's empty state, centred in the width: icon in the quiet
// text color (nil for none), a muted line under it, and action under that
// when there is one.
func Empty(gtx layout.Context, th *theme.Theme, icon IconFunc, line string, action layout.Widget) layout.Dimensions {
	w := gtx.Constraints.Max.X
	y := 0
	centre := func(wd layout.Widget) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
		d := wd(g)
		call := m.Stop()
		o := op.Offset(image.Pt((w-d.Size.X)/2, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		y += d.Size.Y
	}
	if icon != nil {
		centre(func(gtx layout.Context) layout.Dimensions { return icon(gtx, gtx.Dp(20), th.TextQuiet) })
		y += gtx.Dp(theme.SpaceS)
	}
	centre(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(360))
		return widget.Label{Alignment: text.Middle}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), line, colorOp(gtx, th.Muted))
	})
	if action != nil {
		y += gtx.Dp(theme.SpaceM)
		centre(action)
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}
