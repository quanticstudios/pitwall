package sidebar

import (
	"image/color"

	"gioui.org/layout"
)

// IconPath draws an icon from its 24-unit SVG path d, as Icon draws a
// named one: for glyphs a single view keeps to itself.
func IconPath(gtx layout.Context, d string, size int, col color.NRGBA) layout.Dimensions {
	return drawIcon(gtx, d, size, col, 0)
}
