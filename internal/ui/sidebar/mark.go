package sidebar

import (
	_ "embed"
	"image"
	"sync"

	"gioui.org/layout"
	"gioui.org/op/paint"
	xdraw "golang.org/x/image/draw"
)

// skillshareMark is skillshare's own mark, in its colors.
// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/mark.svg
//
//go:embed skillshare-mark.svg
var skillshareMark []byte

var (
	markOnce sync.Once
	markSrc  image.Image // svgPx square, nil when it did not decode
	markMu   sync.Mutex
	markOps  = map[int]paint.ImageOp{} // by size in px
)

// drawMark draws skillshare's mark in a size px square, rasterized once
// as SVG favicons are and scaled per size.
func drawMark(gtx layout.Context, size int) layout.Dimensions {
	markOnce.Do(func() { markSrc = decodeSVG(skillshareMark) })
	if markSrc == nil || size <= 0 {
		return layout.Dimensions{Size: image.Pt(size, size)}
	}
	markMu.Lock()
	o, ok := markOps[size]
	if !ok {
		dst := image.NewNRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), markSrc, markSrc.Bounds(), xdraw.Over, nil)
		o = paint.NewImageOp(dst)
		markOps[size] = o
	}
	markMu.Unlock()
	o.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(size, size)}
}
