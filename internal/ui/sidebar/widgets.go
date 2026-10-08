package sidebar

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// --- drawing helpers ---

func semibold(f font.Font) font.Font { f.Weight = font.SemiBold; return f }
func medium(f font.Font) font.Font   { f.Weight = font.Medium; return f }

func material(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// label draws one truncated line of text.
func label(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, txt string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	// WrapGraphemes fills the line before the ellipsis; the default policy
	// cuts a hyphenated name like swift-otter-… at its last hyphen.
	return widget.Label{MaxLines: 1, WrapPolicy: text.WrapGraphemes}.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
}

// listPad is the sidebar list's inset from both edges.
const listPad unit.Dp = 8

// clickable wraps c with a pointer cursor.
func clickable(gtx layout.Context, c *widget.Clickable, w layout.Widget) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := w(gtx)
		defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return d
	})
}

// iconButton is aide's square ghost icon button: muted glyph, surface-
// secondary fill and foreground glyph on hover.
func iconButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, icon string, size, glyph int, hoverFg bool) layout.Dimensions {
	gtx.Constraints = layout.Exact(image.Pt(size, size))
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		col := th.Muted
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, size, size), gtx.Dp(6)).Op(gtx.Ops))
			if hoverFg {
				col = th.Fg
			}
		}
		return drawCentered(gtx, size, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icon, glyph, col, 0)
		})
	})
}

func drawCentered(gtx layout.Context, size int, w layout.Widget) layout.Dimensions {
	return centered(gtx, image.Pt(size, size), w)
}

// centered draws w centered in a box of size.
func centered(gtx layout.Context, size image.Point, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	gtx.Constraints = layout.Constraints{Max: size}
	d := w(gtx)
	call := m.Stop()
	off := op.Offset(size.Sub(d.Size).Div(2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return layout.Dimensions{Size: size}
}

// item is one child of hrow. shrink marks the single child that takes the
// leftover width and truncates (CSS min-w-0 truncate); right pushes it and
// everything after it to the right edge (ml-auto); ml adds left margin.
type item struct {
	w      layout.Widget
	shrink bool
	right  bool
	ml     int
}

// hrow is a CSS `flex items-center` row of fixed height filling the width.
func hrow(gtx layout.Context, h, gap int, items ...item) layout.Dimensions {
	d := hrowFit(gtx, h, gap, items...)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y)}
}

// hrowFit is hrow sized to its content.
func hrowFit(gtx layout.Context, h, gap int, items ...item) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	calls := make([]op.CallOp, len(items))
	dims := make([]layout.Dimensions, len(items))
	measure := func(i, avail int) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(avail, 0), h)}
		dims[i] = items[i].w(g)
		calls[i] = m.Stop()
	}
	used := 0
	for i, it := range items {
		used += it.ml
		if i > 0 {
			used += gap
		}
		if !it.shrink {
			measure(i, maxW-used)
			used += dims[i].Size.X
		}
	}
	for i, it := range items {
		if it.shrink {
			measure(i, maxW-used)
			used += dims[i].Size.X
		}
	}
	x := 0
	for i, it := range items {
		if i > 0 {
			x += gap
		}
		x += it.ml
		if it.right {
			x = max(x, maxW-(used-x))
		}
		off := op.Offset(image.Pt(x, (h-dims[i].Size.Y)/2)).Push(gtx.Ops)
		calls[i].Add(gtx.Ops)
		off.Pop()
		x += dims[i].Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}
