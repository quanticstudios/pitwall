package sidebar

import (
	"image/color"

	"gioui.org/layout"
)

// IconPath draws lucide path data d, in its 24 box, in a size box: an
// icon that only one view uses, kept beside the view instead of in Icon.
func IconPath(gtx layout.Context, d string, size int, col color.NRGBA) layout.Dimensions {
	return drawIcon(gtx, d, size, col, 0)
}
