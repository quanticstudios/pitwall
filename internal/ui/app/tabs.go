package app

import (
	"image"
	"strings"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Tabs are the sidebar's rows; the window draws only the tab-mode
// indicator.

// tabTitle is what a tab's row shows as its name.
func tabTitle(w model.Workspace) string { return sidebar.Title(w) }

// firstChord is action's first chord as text, "" when it is unbound.
func firstChord(cs []config.Chord) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].String()
}

// modePill is the tab-mode indicator: a "TAB" chip and the keys it takes.
func modePill(gtx gl.Context, th *theme.Theme, b *config.Bindings) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	x, h := 0, gtx.Dp(28)
	tag, tsz := textCall(gtx, th, semibold(th.UIFont), 11, th.Primary, "TAB")
	cw := tsz.X + gtx.Dp(16)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Primary, 0.16), clip.UniformRRect(image.Rect(0, 0, cw, h), h/2).Op(gtx.Ops))
	o := op.Offset(image.Pt(gtx.Dp(8), (h-tsz.Y)/2)).Push(gtx.Ops)
	tag.Add(gtx.Ops)
	o.Pop()
	x += cw + gtx.Dp(10)
	move := strings.Trim(firstChord(b.Tab["prev"])+"/"+firstChord(b.Tab["next"]), "/")
	for _, k := range [][2]string{
		{firstChord(b.Tab["new"]), "new"}, {firstChord(b.Tab["close"]), "close"}, {firstChord(b.Tab["rename"]), "rename"},
		{move, "move"}, {"1-9", "go"}, {"Esc", "exit"},
	} {
		if k[0] == "" {
			continue
		}
		kc, ks := keycap(gtx, th, k[0])
		o := op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		x += ks.X + gtx.Dp(5)
		tc, tsz := textCall(gtx, th, th.UIFont, 12, th.Muted, k[1])
		o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X + gtx.Dp(12)
	}
	return m.Stop(), image.Pt(x-gtx.Dp(12), h)
}

// drawModePill floats the tab-mode indicator at the bottom left of the
// pane area, on a surface so it reads over terminal text.
func (u *ui) drawModePill(gtx gl.Context, area image.Rectangle) {
	call, sz := modePill(gtx, u.th, u.nav.bind())
	pad := gtx.Dp(6)
	box := image.Rectangle{Max: sz.Add(image.Pt(2*pad, 2*pad))}
	at := image.Pt(area.Min.X+gtx.Dp(12), area.Max.Y-gtx.Dp(12)-box.Dy())
	defer op.Offset(at).Push(gtx.Ops).Pop()
	r := box.Dy() / 2
	paint.FillShape(gtx.Ops, theme.Mix(u.th.Surface, u.th.Fg, 0.14), clip.UniformRRect(box, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, u.th.Surface, clip.UniformRRect(box.Inset(1), r-1).Op(gtx.Ops))
	o := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}
