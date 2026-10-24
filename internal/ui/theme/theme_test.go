package theme

import (
	"image/color"
	"os"
	"strings"
	"testing"

	"gioui.org/font"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestProjectColor(t *testing.T) {
	th := &Theme{Blue: Hex("#57c1ff"), Muted: Hex("#8e939c"), Yellow: Hex("#ffc533"), Green: Hex("#59d499")}
	for id, want := range map[string]color.NRGBA{
		"blue":    th.Blue,
		"amber":   th.Yellow,
		"sky":     Hex("#00a6f4"),
		"rose":    Hex("#ff2056"),
		"neutral": th.Muted,
		"":        th.Muted,
	} {
		if got := th.ProjectColor(id); got != want {
			t.Errorf("ProjectColor(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestHexAndMix(t *testing.T) {
	if got := Hex("#2997ff80"); got != (color.NRGBA{0x29, 0x97, 0xff, 0x80}) {
		t.Errorf("Hex = %v", got)
	}
	// rgba(255,255,255,.07) over #08090c, as a browser composites it.
	if got := Mix(Hex("#08090c"), Hex("#ffffff"), 0.07); got != Hex("#191a1d") {
		t.Errorf("Mix = %v", got)
	}
}

func TestDarkFillsFonts(t *testing.T) {
	th := Dark()
	if th.Shaper == nil || th.UIFont.Typeface == "" || th.MonoFont.Typeface == "" {
		t.Fatalf("fonts not loaded: %+v %+v", th.UIFont, th.MonoFont)
	}
	if th.ANSI[15] != Hex("#ffffff") || th.TermBg != th.Bg {
		t.Error("terminal colors not filled")
	}
	for i, c := range vt.DefaultPalette.ANSI {
		if th.ANSI[i] != rgb(c) {
			t.Errorf("ANSI[%d] = %v, want palette %06x", i, th.ANSI[i], c)
		}
	}
	if _, err := os.Stat(emojiFile); err == nil && !strings.HasSuffix(string(th.MonoFont.Typeface), ", "+emojiFamily) {
		t.Errorf("mono typeface %q lacks the emoji fallback", th.MonoFont.Typeface)
	}
}

func TestUIFontFallsBackForMissingGlyphs(t *testing.T) {
	th := Dark()
	// omp titles its tab "π > dir", Geist has no Greek, and row titles are
	// Medium or SemiBold.
	for _, w := range []font.Weight{font.Normal, font.Medium, font.SemiBold, font.Bold} {
		f := th.UIFont
		f.Weight = w
		th.Shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(16), MaxWidth: 1000}, "π")
		if g, ok := th.Shaper.NextGlyph(); !ok || g.ID&(1<<32-1) == 0 {
			t.Errorf("π shapes to .notdef at weight %v", w)
		}
	}
}
