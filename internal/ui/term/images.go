package term

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// picKey is one image op: an image and the part of it shown.
type picKey struct {
	key uint64
	src image.Rectangle
}

// drawImages paints g's image placements over rows [0,h), n cells wide,
// each scaled to its cells and clipped to the grid, then the selection
// over them. Ops are kept while the grid shows them, so an image is
// uploaded to the GPU once.
func (v *View) drawImages(ops *op.Ops, g *vt.Grid, n, h int) {
	if len(g.Images) == 0 || v.Images == nil || n == 0 {
		clear(v.pics)
		return
	}
	if v.pics == nil {
		v.pics = map[picKey]paint.ImageOp{}
	}
	cw, ch := v.cell.X, v.cell.Y
	defer clip.Rect(image.Rect(v.pad, v.pad, v.pad+n*cw, v.pad+h*ch)).Push(ops).Pop()
	shown := map[picKey]bool{}
	for _, p := range g.Images {
		k := picKey{p.Image, p.Src}
		pic, ok := v.pics[k]
		if !ok {
			img, ok := v.Images(p.Image)
			if !ok || p.Src.Empty() || !p.Src.In(image.Rect(0, 0, img.W, img.H)) || len(img.Pix) < img.W*img.H*4 {
				continue
			}
			m := &image.NRGBA{Pix: img.Pix, Stride: img.W * 4, Rect: image.Rect(0, 0, img.W, img.H)}
			pic = paint.NewImageOp(m.SubImage(p.Src))
			v.pics[k] = pic
		}
		shown[k] = true
		sz := pic.Size()
		scale := f32.Pt(float32(p.Cols*cw)/float32(sz.X), float32(p.Rows*ch)/float32(sz.Y))
		at := f32.Pt(float32(v.pad+p.X*cw), float32(v.pad+p.Y*ch))
		t := op.Affine(f32.AffineId().Scale(f32.Point{}, scale).Offset(at)).Push(ops)
		pic.Add(ops)
		paint.PaintOp{}.Add(ops)
		t.Pop()
	}
	for k := range v.pics {
		if !shown[k] {
			delete(v.pics, k)
		}
	}
	if !v.selOn {
		return
	}
	// The selection tints the image as selected() tints a cell's color.
	tint := color.NRGBA{R: 255, G: 255, B: 255, A: 41}
	if bg := v.th.TermBg; 299*int(bg.R)+587*int(bg.G)+114*int(bg.B) > 140*1000 {
		tint = color.NRGBA{A: 41}
	}
	sel := v.sel.Expand(vt.GridLines(g, v.top))
	for y := range h {
		s0, s1 := sel.Cols(v.top+uint64(y), n)
		for _, p := range g.Images {
			if y < p.Y || y >= p.Y+p.Rows {
				continue
			}
			if a, b := max(s0, p.X), min(s1, p.X+p.Cols); a < b {
				fill(ops, tint, image.Rect(v.pad+a*cw, v.pad+y*ch, v.pad+b*cw, v.pad+(y+1)*ch))
			}
		}
	}
}
