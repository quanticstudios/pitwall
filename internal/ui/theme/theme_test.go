package theme

import (
	"image/color"
	"testing"
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
}
