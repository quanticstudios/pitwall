package app

import (
	"image"
	"strings"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
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

// modeKeys are the chip and the key hints of the active mode: tab mode,
// else pane mode.
func modeKeys(b *config.Bindings, pane bool) (string, [][2]string) {
	if pane {
		p := b.Pane
		join := func(names ...string) string {
			var ks []string
			for _, n := range names {
				if k := firstChord(p[n]); k != "" {
					ks = append(ks, k)
				}
			}
			return strings.Join(ks, "/")
		}
		split := join("split_down", "split_right")
		focus := join("focus_left", "focus_down", "focus_up", "focus_right")
		return "PANE", [][2]string{
			{firstChord(p["new"]), "new"}, {split, "down/right"}, {firstChord(p["close"]), "close"},
			{focus, "focus"}, {firstChord(p["fullscreen"]), "fullscreen"}, {firstChord(p["next"]), "next"}, {"Esc", "exit"},
		}
	}
	move := strings.Trim(firstChord(b.Tab["prev"])+"/"+firstChord(b.Tab["next"]), "/")
	return "TAB", [][2]string{
		{firstChord(b.Tab["new"]), "new"}, {firstChord(b.Tab["close"]), "close"}, {firstChord(b.Tab["rename"]), "rename"},
		{move, "move"}, {"1-9", "go"}, {"Esc", "exit"},
	}
}

// modePill is the mode indicator: a "TAB" or "PANE" chip and the keys the
// mode takes.
func modePill(gtx gl.Context, th *theme.Theme, b *config.Bindings, pane bool) (op.CallOp, image.Point) {
	name, keys := modeKeys(b, pane)
	return pill(gtx, th, name, keys)
}

// pill is a mode chip named name and the keys the mode takes, each a key
// and what it does; keys without a key are left out.
func pill(gtx gl.Context, th *theme.Theme, name string, keys [][2]string) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	x, h := 0, gtx.Dp(28)
	tag, tsz := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Primary, name)
	cw := tsz.X + gtx.Dp(16)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Primary, 0.16), clip.UniformRRect(image.Rect(0, 0, cw, h), h/2).Op(gtx.Ops))
	o := op.Offset(image.Pt(gtx.Dp(8), (h-tsz.Y)/2)).Push(gtx.Ops)
	tag.Add(gtx.Ops)
	o.Pop()
	x += cw + gtx.Dp(10)
	for _, k := range keys {
		if k[0] == "" {
			continue
		}
		kc, ks := keycap(gtx, th, k[0])
		o := op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		x += ks.X + gtx.Dp(5)
		tc, tsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, k[1])
		o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X + gtx.Dp(12)
	}
	return m.Stop(), image.Pt(x-gtx.Dp(12), h)
}

// drawModePill floats the mode indicator at the bottom left of the
// pane area, on a surface so it reads over terminal text.
func (u *ui) drawModePill(gtx gl.Context, area image.Rectangle) {
	call, sz := modePill(gtx, u.th, u.nav.bind(), u.nav.paneMode)
	pad := gtx.Dp(6)
	box := image.Rectangle{Max: sz.Add(image.Pt(2*pad, 2*pad))}
	u.drawPill(gtx, call, box, image.Pt(area.Min.X+gtx.Dp(12), area.Max.Y-gtx.Dp(12)-box.Dy()))
}

// drawPill draws a pill's call in a box at at, on a surface so it reads
// over terminal text.
func (u *ui) drawPill(gtx gl.Context, call op.CallOp, box image.Rectangle, at image.Point) {
	pad := gtx.Dp(6)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	r := box.Dy() / 2
	paint.FillShape(gtx.Ops, theme.Mix(u.th.Surface, u.th.Fg, 0.14), clip.UniformRRect(box, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, u.th.Surface, clip.UniformRRect(box.Inset(1), r-1).Op(gtx.Ops))
	o := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}

// drawZoomHint marks a fullscreen pane: a small "fullscreen" chip in the
// top right corner of its frame r.
func (u *ui) drawZoomHint(gtx gl.Context, r layout.Rect) {
	th := u.th
	label := "fullscreen"
	if k := firstChord(u.nav.bind().Pane["fullscreen"]); k != "" {
		if p := firstChord(u.nav.bind().Global["pane_prefix"]); p != "" {
			label += " · " + p + " " + k
		}
	}
	call, tsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Caption), theme.Mix(th.Muted, th.Primary, 0.5), label)
	pad := image.Pt(gtx.Dp(8), gtx.Dp(3))
	box := image.Rectangle{Max: tsz.Add(pad.Mul(2))}
	at := image.Pt(r.X+r.W-box.Dx()-gtx.Dp(8), r.Y+gtx.Dp(6))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rr := box.Dy() / 2
	paint.FillShape(gtx.Ops, theme.Mix(th.TermBg, th.Primary, 0.45), clip.UniformRRect(box, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, theme.Mix(th.TermBg, th.Primary, 0.1), clip.UniformRRect(box.Inset(1), rr-1).Op(gtx.Ops))
	o := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}
