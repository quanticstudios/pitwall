package kit

import (
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
)

// Clickable lays out w inside c and shows the pointer cursor over its hit
// area while enabled.
func Clickable(gtx layout.Context, c *widget.Clickable, w layout.Widget) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := w(gtx)
		if gtx.Enabled() {
			defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
		}
		return d
	})
}
