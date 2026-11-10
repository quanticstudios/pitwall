package sidebar

import (
	"image/color"

	"gioui.org/layout"
)

// Path strokes d, a lucide path in its 24x24 box, at size px, for icons the
// sidebar does not have.
func Path(gtx layout.Context, d string, size int, col color.NRGBA) layout.Dimensions {
	return drawIcon(gtx, d, size, col, 0)
}
