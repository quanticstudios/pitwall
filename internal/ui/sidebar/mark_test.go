package sidebar

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
)

// The embedded mark decodes, with its blue arc in it.
func TestSkillshareMarkDecodes(t *testing.T) {
	img := decodeSVG(skillshareMark)
	if img == nil {
		t.Fatal("the mark did not decode")
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, g, bl, a := img.At(x, y).RGBA(); a > 0xf000 && bl > 0xe000 && r < 0x6000 && g > 0x8000 {
				return
			}
		}
	}
	t.Fatal("no blue in the mark")
}

// Antigravity's embedded mark decodes, blue down the right leg of its arch and
// clear between its legs, and draws at every mark size.
func TestAntigravityMark(t *testing.T) {
	img := antigravityRaster.decode()
	if img == nil || img.Bounds().Dx() != 128 {
		t.Fatalf("the mark did not decode: %v", img)
	}
	if r, _, b, a := img.At(104, 100).RGBA(); a < 0xc000 || b < r {
		t.Errorf("the right leg is not blue: %x %x %x", r, b, a)
	}
	if _, _, _, a := img.At(64, 120).RGBA(); a != 0 {
		t.Errorf("the gap under the arch is filled: alpha %x", a)
	}
	for _, size := range []int{12, 16, 24} {
		gtx := layout.Context{Ops: new(op.Ops)}
		if d := antigravityRaster.draw(gtx, size); d.Size != image.Pt(size, size) {
			t.Errorf("drawn at %d: %v", size, d.Size)
		}
	}
}
