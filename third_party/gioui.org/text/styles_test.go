package text

import (
	"testing"

	giofont "gioui.org/font"
	"gioui.org/font/gofont"
	"golang.org/x/image/math/fixed"
)

// pitwall: Parameters.Styles set each range's size and slant, across
// paragraphs, and leave the rest of the text alone.
func TestStyles(t *testing.T) {
	sh := NewShaper(NoSystemFonts(), WithCollection(gofont.Collection()))
	base := giofont.Font{Typeface: "Go"}
	italic := base
	italic.Style = giofont.Italic
	params := Parameters{Font: base, PxPerEm: fixed.I(20), MaxWidth: 1000, Styles: []Style{
		{Start: 2, End: 4, Font: base, Scale: 0.5},
		{Start: 6, End: 8, Font: italic},
	}}
	sh.LayoutString(params, "aabb\nccdd")
	var ppem []fixed.Int26_6
	var faces []int
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		if g.Flags&FlagParagraphBreak != 0 {
			continue
		}
		p, f, _ := splitGlyphID(g.ID)
		ppem, faces = append(ppem, p), append(faces, f)
	}
	if len(ppem) != 8 {
		t.Fatalf("got %d glyphs, want 8", len(ppem))
	}
	for i, want := range []fixed.Int26_6{20, 20, 10, 10, 20, 20, 20, 20} {
		if ppem[i] != fixed.I(int(want)) {
			t.Errorf("glyph %d: ppem %v, want %d", i, ppem[i], want)
		}
	}
	// Go has an italic face: dd uses it, not a slanted copy.
	if faces[6] == faces[4] || sh.shaper.slanted[faces[6]] {
		t.Errorf("dd: face %d (slanted %v), cc face %d", faces[6], sh.shaper.slanted[faces[6]], faces[4])
	}

	// A family with no italic face draws a slanted copy of the upright one.
	sh = NewShaper(NoSystemFonts(), WithCollection(gofont.Regular()))
	sh.LayoutString(Parameters{Font: base, PxPerEm: fixed.I(20), MaxWidth: 1000, Styles: []Style{{Start: 1, End: 2, Font: italic}}}, "ab")
	var got []int
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		_, f, _ := splitGlyphID(g.ID)
		got = append(got, f)
	}
	if len(got) < 2 || got[0] == got[1] {
		t.Fatalf("faces %v: b should differ from a", got)
	}
	if !sh.shaper.slanted[got[1]] {
		t.Errorf("b's face %d is not slanted", got[1])
	}
}
