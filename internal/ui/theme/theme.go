// Package theme holds colors, fonts and metrics ported from aide's renderer
// tokens, so pitwall looks like aide.
package theme

import (
	"bytes"
	_ "embed"
	"image/color"
	"log"
	"os"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	fontapi "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
)

type Theme struct {
	Shaper *text.Shaper

	Bg, Sidebar, Surface, SurfaceSecondary, SurfaceElevated color.NRGBA
	Border, Fg, Muted, Primary                              color.NRGBA
	Red, Yellow, Green, Blue, Purple                        color.NRGBA

	// Terminal colors: ANSI 0-15, and the default foreground, background
	// and cursor.
	ANSI                    [16]color.NRGBA
	TermFg, TermBg, TermCur color.NRGBA

	// Shaper is loaded with both faces.
	UIFont, MonoFont              font.Font
	TextSize, SmallSize, MonoSize unit.Sp
}

// Hex parses "#rrggbb" or "#rrggbbaa". It panics on bad input, because every
// caller passes a literal.
func Hex(s string) color.NRGBA {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		panic("theme: bad hex color " + s)
	}
	n := func(i int) uint8 {
		var v uint8
		for _, c := range s[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= uint8(c - '0')
			case c >= 'a' && c <= 'f':
				v |= uint8(c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				v |= uint8(c - 'A' + 10)
			default:
				panic("theme: bad hex color " + s)
			}
		}
		return v
	}
	c := color.NRGBA{R: n(1), G: n(3), B: n(5), A: 0xff}
	if len(s) == 9 {
		c.A = n(7)
	}
	return c
}

// Mix returns the opaque color a browser shows for fg at opacity a (0..1)
// over bg, the way Tailwind's `bg-x/12` composites. Gio blends translucent
// colors in linear light, which makes aide's 3-15% tints several times too
// strong, so translucent tokens are flattened against their background.
func Mix(bg, fg color.NRGBA, a float32) color.NRGBA {
	l := func(b, f uint8) uint8 { return uint8(float32(b) + (float32(f)-float32(b))*a + 0.5) }
	return color.NRGBA{R: l(bg.R, fg.R), G: l(bg.G, fg.G), B: l(bg.B, fg.B), A: 0xff}
}

// Dark is aide's default "dark" palette (styles.css :root[data-palette="dark"]).
func Dark() *Theme {
	t := &Theme{
		Bg:               Hex("#08090c"),
		Sidebar:          Hex("#08090c"),
		Surface:          Hex("#14161b"),
		SurfaceSecondary: Hex("#1c1f26"),
		// aide has no --surface-elevated; its floating surfaces use the
		// tertiary step, so that is what "elevated" means here.
		SurfaceElevated: Hex("#252932"),
		Border:          Mix(Hex("#08090c"), Hex("#ffffff"), 0.07), // rgba(255,255,255,.07)
		Fg:              Hex("#f2f3f5"),
		Muted:           Hex("#8e939c"),
		Primary:         Hex("#2997ff"),
		Red:             Hex("#ff6b6b"),
		Yellow:          Hex("#ffc533"),
		Green:           Hex("#59d499"),
		Blue:            Hex("#57c1ff"),
		Purple:          Hex("#bd93ff"),

		// aide TerminalPane.tsx xterm theme.
		TermFg:  Hex("#f4f4f6"),
		TermBg:  Hex("#08090c"),
		TermCur: Hex("#ffffff"),

		TextSize:  13,
		SmallSize: 11,
		MonoSize:  13,
	}
	for i, h := range []string{
		"#0d0d0d", "#ff6161", "#59d499", "#ffc533", "#57c1ff", "#bb9af7", "#7dcfff", "#cdcdcd",
		"#242728", "#ff6161", "#59d499", "#ffc533", "#57c1ff", "#bb9af7", "#7dcfff", "#ffffff",
	} {
		t.ANSI[i] = Hex(h)
	}
	t.Shaper, t.UIFont, t.MonoFont = loadFonts()
	return t
}

var projectColors = map[string]string{
	"red": "#fb2c36", "orange": "#ff6900", "yellow": "#fdc700", "lime": "#7ccf00",
	"emerald": "#00bc7d", "teal": "#00bba7", "cyan": "#00b8db", "sky": "#00a6f4",
	"indigo": "#615fff", "violet": "#a684ff", "purple": "#ad46ff", "fuchsia": "#e12afb",
	"pink": "#f6339a", "rose": "#ff2056",
}

// ProjectColor maps an aide project color id ("red", "sky", ...) to a color.
// It follows aide's ProjectIcon.tsx: blue, green and amber use the accent
// tokens, the rest are Tailwind v4 500/400 shades, unknown ids are muted.
func (t *Theme) ProjectColor(id string) color.NRGBA {
	switch id {
	case "blue":
		return t.Blue
	case "green":
		return t.Green
	case "amber":
		return t.Yellow
	}
	if h, ok := projectColors[id]; ok {
		return Hex(h)
	}
	return t.Muted
}

// Geist is aide's UI font (@fontsource-variable/geist, OFL). It is not a
// system font here, so the latin subset ships converted from woff2 to TTF.
//
//go:embed fonts/Geist.ttf
var geistTTF []byte

const (
	uiFamily   = "Geist"
	monoFamily = "JetBrainsMono Nerd Font"
	monoDir    = "/usr/share/fonts/TTF/"
)

// varFace pins a variable font to one weight. Each weight needs its own
// parsed *Font because Gio keys faces by that pointer.
type varFace struct {
	f    *fontapi.Font
	wght float32
}

func (v varFace) Face() *fontapi.Face {
	fc := fontapi.NewFace(v.f)
	fc.SetVariations([]fontapi.Variation{{Tag: ot.MustNewTag("wght"), Value: v.wght}})
	return fc
}

func loadFonts() (*text.Shaper, font.Font, font.Font) {
	var faces []font.FontFace
	ui := font.Font{Typeface: uiFamily}
	for _, w := range []font.Weight{font.Normal, font.Medium, font.SemiBold, font.Bold} {
		ld, err := ot.NewLoader(bytes.NewReader(geistTTF))
		if err != nil {
			log.Printf("theme: geist: %v", err)
			break
		}
		f, err := fontapi.NewFont(ld)
		if err != nil {
			log.Printf("theme: geist: %v", err)
			break
		}
		faces = append(faces, font.FontFace{
			Font: font.Font{Typeface: uiFamily, Weight: w},
			Face: varFace{f: f, wght: float32(400 + w)},
		})
	}
	if len(faces) == 0 {
		ui = font.Font{Typeface: "Go"}
		faces = append(faces, gofont.Collection()...)
	}

	mono := font.Font{Typeface: monoFamily}
	var monoFaces []font.FontFace
	for file, w := range map[string]font.Weight{
		"JetBrainsMonoNerdFont-Regular.ttf": font.Normal,
		"JetBrainsMonoNerdFont-Bold.ttf":    font.Bold,
	} {
		b, err := os.ReadFile(monoDir + file)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(b)
		if err != nil {
			log.Printf("theme: %s: %v", file, err)
			continue
		}
		monoFaces = append(monoFaces, font.FontFace{Font: font.Font{Typeface: monoFamily, Weight: w}, Face: f})
	}
	if len(monoFaces) == 0 {
		mono = font.Font{Typeface: "Go Mono"}
		if ui.Typeface != "Go" {
			monoFaces = gofont.Collection()
		}
	}
	faces = append(faces, monoFaces...)
	return text.NewShaper(text.NoSystemFonts(), text.WithCollection(faces)), ui, mono
}
