package sidebar

import "testing"

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
