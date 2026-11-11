package sidebar

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestIconResidue draws the gear, a path of many segments, on the GPU at
// the phone's size and looks for lit pixels away from its edges. Drawn as a
// GPU path, half-float coverage sums left alpha up to 3 in columns below
// it, plain on a dark background after linear blending. It skips where no
// headless GPU context opens, as in CI.
func TestIconResidue(t *testing.T) {
	const size, pad = 50, 4
	w, err := headless.NewWindow(size+2*pad, size+2*pad)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer w.Release()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	off := op.Offset(image.Pt(pad, pad)).Push(&ops)
	drawIcon(gtx, icSettings, size, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, 0)
	off.Pop()
	if err := w.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, size+2*pad, size+2*pad))
	if err := w.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	// invariant: an edge's antialiasing has a pixel over a quarter covered within 2 px.
	near := func(x, y int) bool {
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				if img.RGBAAt(x+dx, y+dy).A > 64 {
					return true
				}
			}
		}
		return false
	}
	stray := 0
	for y := range size + 2*pad {
		for x := range size + 2*pad {
			if img.RGBAAt(x, y).A > 0 && !near(x, y) {
				stray++
			}
		}
	}
	if stray > 0 {
		t.Fatalf("%d lit pixels away from the gear's edges; want none", stray)
	}
}

// TestIconMask checks the CPU stroke: full on the gear's outline, nothing
// beyond the stroke's half width and a pixel of antialiasing.
func TestIconMask(t *testing.T) {
	const size = 48
	m := iconMask(icSettings, size, false)
	hw := float32(size) / 24
	polys := flatten(parsePath(icSettings), float32(size)/24)
	dist := func(p f32.Point) float32 {
		d := float32(size)
		for _, pl := range polys {
			for i := 1; i < len(pl); i++ {
				d = min(d, segDist(p, pl[i-1], pl[i]))
			}
		}
		return d
	}
	for y := range size {
		for x := range size {
			d, a := dist(f32.Pt(float32(x)+0.5, float32(y)+0.5)), m.AlphaAt(x, y).A
			switch {
			case d < hw-0.5 && a != 255:
				t.Fatalf("(%d,%d), %.2f px from the outline: alpha %d, want 255", x, y, d, a)
			case d > hw+0.5 && a != 0:
				t.Fatalf("(%d,%d), %.2f px from the outline: alpha %d, want 0", x, y, d, a)
			}
		}
	}
	if f := iconMask(icClaude, size, true); f.AlphaAt(size/2, size/2).A != 255 || f.AlphaAt(0, 0).A != 0 {
		t.Fatalf("the Claude fill: center %d, corner %d; want 255 and 0", f.AlphaAt(size/2, size/2).A, f.AlphaAt(0, 0).A)
	}
}
