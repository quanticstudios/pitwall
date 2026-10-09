package theme

import (
	"image/color"
	"math"

	"gioui.org/unit"
)

// Tokens are the colors derived from a theme's palette, computed once in
// New so every surface draws hover, borders and quiet text the same way.
type Tokens struct {
	Hover, Pressed             color.NRGBA // a ghost control's fill under the pointer and held down
	BorderSubtle, BorderStrong color.NRGBA // a card's ring, a floating surface's ring
	TextQuiet                  color.NRGBA // the quietest text that still reads at 4.5:1
	OnDanger                   color.NRGBA // text on a Red fill
	Scrim                      color.NRGBA // the backdrop under a dialog or overlay
	SelectedBg                 color.NRGBA // a picked option's fill
}

// ScrimAlpha is the one backdrop alpha every dialog and overlay uses.
const ScrimAlpha = 0xa6

// MinContrast is WCAG AA for body text.
const MinContrast = 4.5

func (t *Theme) derive() {
	t.Hover = t.SurfaceElevated
	t.Pressed = Mix(t.SurfaceElevated, t.Fg, 0.06)
	t.BorderSubtle = Mix(t.Surface, t.Fg, 0.07)
	t.BorderStrong = Mix(t.Sidebar, t.Fg, 0.14)
	t.TextQuiet = Readable(t.Fg, Mix(t.Sidebar, t.Muted, 0.7), t.Bg, t.Sidebar, t.Surface, t.SurfaceSecondary)
	t.OnDanger = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	if Contrast(t.Bg, t.Red) > Contrast(t.OnDanger, t.Red) {
		t.OnDanger = t.Bg
	}
	t.Scrim = color.NRGBA{A: ScrimAlpha}
	t.SelectedBg = Mix(t.SurfaceSecondary, t.Primary, 0.15)
}

// Readable is c moved toward toward, in 5% steps, until it reaches
// MinContrast on every one of bgs; toward itself when no step does.
func Readable(toward, c color.NRGBA, bgs ...color.NRGBA) color.NRGBA {
	for i := 0; i <= 20; i++ {
		m := Mix(c, toward, float32(i)/20)
		ok := true
		for _, bg := range bgs {
			ok = ok && Contrast(m, bg) >= MinContrast
		}
		if ok {
			return m
		}
	}
	return toward
}

// Readable is c moved toward the theme's Fg until it reads on bg.
func (t *Theme) Readable(c, bg color.NRGBA) color.NRGBA { return Readable(t.Fg, c, bg) }

// Chip is a status chip's fill and text for accent c over base: the
// accent at 14% and the accent text, darkened or lightened to read.
func (t *Theme) Chip(c, base color.NRGBA) (bg, fg color.NRGBA) {
	bg = Mix(base, c, 0.14)
	return bg, t.Readable(c, bg)
}

// Contrast is the WCAG contrast ratio of two opaque colors, 1 to 21.
func Contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c color.NRGBA) float64 {
	ch := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// Step is a step of the type scale, named by its size at the default 13px
// UI text.
type Step float32

// The type scale.
const (
	Caption Step = 11
	Small   Step = 12
	Body    Step = 13
	Large   Step = 14
	Title   Step = 16
)

// Sp is step s scaled by the configured UI text size.
func (t *Theme) Sp(s Step) unit.Sp { return t.TextSize * unit.Sp(float32(s)/13) }

// The spacing scale.
const (
	Space2XS unit.Dp = 2
	SpaceXS  unit.Dp = 4
	SpaceS   unit.Dp = 8
	SpaceM   unit.Dp = 12
	SpaceL   unit.Dp = 16
	SpaceXL  unit.Dp = 24
	Space2XL unit.Dp = 32
)

// Corner radii: dialogs and overlays, popovers and menus, controls.
const (
	RadiusCard    unit.Dp = 12
	RadiusPopover unit.Dp = 8
	RadiusControl unit.Dp = 6
)
