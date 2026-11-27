package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// TestBorderCrisp: at scale 1 a 1px Border is one pixel of full colour on
// every side, the fill under it shows inside, and the background outside.
func TestBorderCrisp(t *testing.T) {
	const W, H = 60, 40
	win, err := headless.NewWindow(W, H)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	bg := color.NRGBA{R: 10, G: 10, B: 10, A: 255}
	fill := color.NRGBA{R: 40, G: 40, B: 40, A: 255}
	ring := color.NRGBA{R: 250, G: 180, B: 20, A: 255}
	box := image.Rect(10, 8, 50, 32)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(W, H))}
	paint.Fill(gtx.Ops, bg)
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(box, 6).Op(gtx.Ops))
	Border(gtx, box, 6, gtx.Dp(1), ring)
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) color.NRGBA {
		c := img.RGBAAt(x, y)
		return color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A}
	}
	near := func(a, b color.NRGBA) bool { // within the renderer's rounding
		d := func(x, y uint8) bool { return int(x)-int(y) <= 2 && int(y)-int(x) <= 2 }
		return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
	}
	for _, c := range []struct {
		name     string
		edge, in image.Point // the edge pixel and the one inside it
		out      image.Point
	}{
		{"top", image.Pt(30, 8), image.Pt(30, 9), image.Pt(30, 7)},
		{"bottom", image.Pt(30, 31), image.Pt(30, 30), image.Pt(30, 32)},
		{"left", image.Pt(10, 20), image.Pt(11, 20), image.Pt(9, 20)},
		{"right", image.Pt(49, 20), image.Pt(48, 20), image.Pt(50, 20)},
	} {
		if got := at(c.edge.X, c.edge.Y); !near(got, ring) {
			t.Errorf("%s edge %v = %v, want %v", c.name, c.edge, got, ring)
		}
		if got := at(c.in.X, c.in.Y); !near(got, fill) {
			t.Errorf("%s inside %v = %v, want the fill %v", c.name, c.in, got, fill)
		}
		if got := at(c.out.X, c.out.Y); !near(got, bg) {
			t.Errorf("%s outside %v = %v, want the background %v", c.name, c.out, got, bg)
		}
	}
}
