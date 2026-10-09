package theme

import (
	"image/color"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
)

// TestTokenContrast checks every built-in theme's text tokens on the
// surfaces they are drawn on, and the status chips at their size.
func TestTokenContrast(t *testing.T) {
	for _, name := range config.Themes {
		c, _ := config.Builtin(name)
		th, _ := New(c, config.Font{})
		surfaces := map[string]color.NRGBA{"bg": th.Bg, "sidebar": th.Sidebar, "surface": th.Surface, "surface_secondary": th.SurfaceSecondary}
		check := func(what string, fg, bg color.NRGBA) {
			t.Helper()
			if r := Contrast(fg, bg); r < MinContrast {
				t.Errorf("%s: %s is %.2f:1", name, what, r)
			}
		}
		for sn, bg := range surfaces {
			check("fg on "+sn, th.Fg, bg)
			check("muted on "+sn, th.Muted, bg)
			check("text_quiet on "+sn, th.TextQuiet, bg)
		}
		check("on_primary on primary", th.OnPrimary, th.Primary)
		check("on_danger on red", th.OnDanger, th.Red)
		for _, acc := range []color.NRGBA{th.Red, th.Yellow, th.Green, th.Blue, th.Purple} {
			for sn, base := range surfaces {
				bg, fg := th.Chip(acc, base)
				check("chip text on "+sn, fg, bg)
			}
		}
	}
}

func TestReadable(t *testing.T) {
	bg := Hex("#08090c")
	if got := Readable(Hex("#f2f3f5"), Hex("#8e939c"), bg); got != Hex("#8e939c") {
		t.Errorf("a readable color moved: %v", got)
	}
	dim := Hex("#3a3c40")
	got := Readable(Hex("#f2f3f5"), dim, bg)
	if Contrast(got, bg) < MinContrast || got == Hex("#f2f3f5") {
		t.Errorf("Readable(%v) = %v at %.2f:1", dim, got, Contrast(got, bg))
	}
	if r := Contrast(Hex("#000000"), Hex("#ffffff")); r < 20.9 || r > 21.1 {
		t.Errorf("black on white = %.2f", r)
	}
}
