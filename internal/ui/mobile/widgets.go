package mobile

import (
	"image"
	"image/color"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Sizes, in the app's dp, which zoom scales: barH is the top bar, touch
// the least a control takes.
const (
	barH  unit.Dp = 40
	touch unit.Dp = 32
	pad   unit.Dp = 12
)

type (
	C = layout.Context
	D = layout.Dimensions
)

func colorOp(gtx C, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

func weight(f font.Font, w font.Weight) font.Font {
	f.Weight = w
	return f
}

// label draws s, wrapped, in at most lines lines (0 for any).
func label(gtx C, th *theme.Theme, f font.Font, size unit.Sp, col color.NRGBA, lines int, s string) D {
	return widget.Label{MaxLines: lines, Truncator: "…", WrapPolicy: text.WrapWords}.Layout(gtx, th.Shaper, f, size, s, colorOp(gtx, col))
}

func body(gtx C, th *theme.Theme, col color.NRGBA, s string) D {
	return label(gtx, th, th.UIFont, th.Sp(theme.Body), col, 0, s)
}

func small(gtx C, th *theme.Theme, col color.NRGBA, s string) D {
	return label(gtx, th, th.UIFont, th.Sp(theme.Small), col, 0, s)
}

// box draws w on a fill with radius r and a ring, padded by in.
func box(gtx C, fill, ring color.NRGBA, r unit.Dp, in layout.Inset, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := in.Layout(gtx, w)
	call := m.Stop()
	rect := image.Rectangle{Max: d.Size}
	rr := gtx.Dp(r)
	if ring.A > 0 {
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, rr).Op(gtx.Ops))
		rect = rect.Inset(1)
		rr--
	}
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

// button is a kit button tall enough to touch.
func button(gtx C, th *theme.Theme, c *widget.Clickable, k kit.Kind, s string) D {
	return kit.Button(gtx, th, c, k, kit.Large, s)
}

// iconButton is a touch-sized ghost button around icon.
func iconButton(gtx C, th *theme.Theme, c *widget.Clickable, icon func(gtx C, col color.NRGBA, size int)) D {
	side := gtx.Dp(touch)
	return c.Layout(gtx, func(gtx C) D {
		col := th.Muted
		if c.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rect(0, 0, side, side), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			col = th.Fg
		}
		is := gtx.Dp(16)
		o := op.Offset(image.Pt((side-is)/2, (side-is)/2)).Push(gtx.Ops)
		icon(gtx, col, is)
		o.Pop()
		return D{Size: image.Pt(side, side)}
	})
}

// back is a left-pointing chevron: the sidebar's right one, turned.
func back(gtx C, col color.NRGBA, size int) {
	s := float32(size) / 2
	defer op.Affine(f32.AffineId().Rotate(f32.Pt(s, s), 3.14159265)).Push(gtx.Ops).Pop()
	sidebar.Icon(gtx, "chevron-right", size, col)
}

func chevronDown(gtx C, col color.NRGBA, size int) { sidebar.Icon(gtx, "chevron-down", size, col) }

// field draws ed in a ringed box with hint while it is empty. A tap
// anywhere in the box focuses ed, which brings up the soft keyboard.
func field(gtx C, th *theme.Theme, ed *widget.Editor, hint string, f font.Font) D {
	ring := th.BorderStrong
	if gtx.Focused(ed) {
		ring = theme.Mix(th.Bg, th.Primary, 0.6)
	}
	fs := fields[ed]
	if fs == nil {
		fs = new(widget.Clickable)
		fields[ed] = fs
	}
	if fs.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: ed})
		gtx.Execute(key.SoftKeyboardCmd{Show: true})
	}
	return fs.Layout(gtx, func(gtx C) D {
		return box(gtx, th.Bg, ring, theme.RadiusControl, layout.UniformInset(10), func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			size := th.Sp(theme.Body)
			return layout.Stack{}.Layout(gtx,
				layout.Stacked(func(gtx C) D {
					if ed.Len() > 0 {
						return D{}
					}
					return label(gtx, th, f, size, th.Readable(th.TextQuiet, th.Bg), 1, hint)
				}),
				layout.Stacked(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return ed.Layout(gtx, th.Shaper, f, size, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Bg, th.Primary, 0.35)))
				}),
			)
		})
	})
}

// fields are the clickables around each field's box. ponytail: never
// freed; an app has a handful of editors.
var fields = map[*widget.Editor]*widget.Clickable{}

// sentence is s with its first letter capitalized, for an error shown on
// screen.
func sentence(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// divider is a 1px line across the width.
func divider(gtx C, col color.NRGBA) D {
	size := image.Pt(gtx.Constraints.Max.X, 1)
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: size}.Op())
	return D{Size: size}
}

// fillMax paints col under the whole of the constraints.
func fillMax(gtx C, col color.NRGBA) {
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: gtx.Constraints.Max}.Op())
}
