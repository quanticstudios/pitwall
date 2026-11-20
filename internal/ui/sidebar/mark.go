package sidebar

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"
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

// antigravityMark is Antigravity's own mark, its arch filled with blurred
// Google colors. From lobe-icons (MIT), @lobehub/icons-static-svg 1.104.0
// icons/antigravity-color.svg, rendered to a 128 px PNG with rsvg-convert:
// its mask and blur filters are beyond oksvg.
//
//go:embed antigravity-mark.png
var antigravityMark []byte

var (
	skillshareRaster  = &rasterMark{decode: func() image.Image { return decodeSVG(skillshareMark) }}
	antigravityRaster = &rasterMark{decode: func() image.Image {
		img, _ := png.Decode(bytes.NewReader(antigravityMark))
		return img
	}}
)

// rasterMark is a mark in its own colors, decoded once and scaled per size.
type rasterMark struct {
	decode func() image.Image // nil when it does not decode
	once   sync.Once
	src    image.Image
	mu     sync.Mutex
	ops    map[int]paint.ImageOp // by size in px
}

// drawMark draws skillshare's mark in a size px square.
func drawMark(gtx layout.Context, size int) layout.Dimensions {
	return skillshareRaster.draw(gtx, size)
}

// draw draws the mark in a size px square at the current offset.
func (m *rasterMark) draw(gtx layout.Context, size int) layout.Dimensions {
	m.once.Do(func() { m.src = m.decode() })
	if m.src == nil || size <= 0 {
		return layout.Dimensions{Size: image.Pt(size, size)}
	}
	m.mu.Lock()
	o, ok := m.ops[size]
	if !ok {
		dst := image.NewNRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), m.src, m.src.Bounds(), xdraw.Over, nil)
		o = paint.NewImageOp(dst)
		if m.ops == nil {
			m.ops = map[int]paint.ImageOp{}
		}
		m.ops[size] = o
	}
	m.mu.Unlock()
	o.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(size, size)}
}
