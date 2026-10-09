// Package theme holds colors, fonts and metrics ported from aide's renderer
// tokens, so pitwall looks like aide.
package theme

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	fontapi "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"

	"github.com/quanticstudios/pitwall/internal/config"
)

type Theme struct {
	Shaper *text.Shaper

	Bg, Sidebar, Surface, SurfaceSecondary, SurfaceElevated color.NRGBA
	Border, Fg, Muted, Primary, OnPrimary                   color.NRGBA
	Red, Yellow, Green, Blue, Purple                        color.NRGBA
	Tokens

	// Terminal colors: ANSI 0-15, and the default foreground, background
	// and cursor.
	ANSI                    [16]color.NRGBA
	TermFg, TermBg, TermCur color.NRGBA

	// Shaper is loaded with both faces.
	UIFont, MonoFont              font.Font
	TextSize, SmallSize, MonoSize unit.Sp
	LineHeight                    float32 // terminal cell height multiplier; 0 means 1
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
	t, _ := config.Builtin("aide-dark")
	th, _ := New(t, config.Font{})
	return th
}

// New builds a theme from fully resolved colors (config.Settings.Theme) and
// fonts; zero font fields take the defaults. A font that cannot be found
// falls back to the default and is reported in the error.
func New(c config.Theme, f config.Font) (*Theme, error) {
	col := func(s config.Color) color.NRGBA {
		if s == "" {
			return color.NRGBA{A: 0xff}
		}
		return Hex(string(s))
	}
	k := c.Colors
	t := &Theme{
		Bg: col(k.Bg), Sidebar: col(k.Sidebar), Surface: col(k.Surface),
		SurfaceSecondary: col(k.SurfaceSecondary),
		// aide has no --surface-elevated; its floating surfaces use the
		// tertiary step, so that is what "elevated" means here.
		SurfaceElevated: col(k.SurfaceElevated),
		Border:          col(k.Border), Fg: col(k.Fg), Muted: col(k.Muted),
		Primary: col(k.Primary), OnPrimary: col(k.OnPrimary),
		Red: col(k.Red), Yellow: col(k.Yellow), Green: col(k.Green), Blue: col(k.Blue), Purple: col(k.Purple),

		TermFg: col(c.Terminal.Foreground), TermBg: col(c.Terminal.Background), TermCur: col(c.Terminal.Cursor),

		TextSize:   unit.Sp(or(f.UISize, config.DefaultUISize)),
		SmallSize:  unit.Sp(or(f.UISize, config.DefaultUISize) - 2),
		MonoSize:   unit.Sp(or(f.MonoSize, config.DefaultMonoSize)),
		LineHeight: float32(or(f.LineHeight, 1)),
	}
	for i, a := range c.Terminal.ANSI {
		if i < len(t.ANSI) {
			t.ANSI[i] = col(a)
		}
	}
	t.derive()
	var err error
	t.Shaper, t.UIFont, t.MonoFont, err = fonts(f.UIFamily, f.MonoFamily, f.MonoFallback)
	return t, err
}

func or(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
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
	// emojiFamily is the color emoji face, listed after the mono family in
	// MonoFont.Typeface so the shaper falls back to it per glyph.
	emojiFamily = "emoji"
	emojiFile   = "/usr/share/fonts/noto/NotoColorEmoji.ttf"
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

var (
	fontMu    sync.Mutex
	fontCache = map[string]fontSet{}
)

type fontSet struct {
	faces    []font.FontFace
	ui, mono font.Font
	err      error
}

// fonts returns a new shaper over the faces of the two families, parsed
// once per pair. Each theme gets its own shaper because a shaper is not
// safe to share between windows.
func fonts(uiFam, monoFam string, fallback []string) (*text.Shaper, font.Font, font.Font, error) {
	if uiFam == "" {
		uiFam = config.DefaultUIFamily
	}
	if monoFam == "" {
		monoFam = config.DefaultMonoFamily
	}
	fontMu.Lock()
	k := strings.Join(append([]string{uiFam, monoFam}, fallback...), "\x00")
	fs, ok := fontCache[k]
	if !ok {
		fs = loadFonts(uiFam, monoFam, fallback)
		fontCache[k] = fs
	}
	fontMu.Unlock()
	return text.NewShaper(text.NoSystemFonts(), text.WithCollection(fs.faces)), fs.ui, fs.mono, fs.err
}

var (
	sysOnce  sync.Once
	sysFonts *fontscan.FontMap
)

// SystemFonts is the system font index, built once and cached on disk by
// fontscan. The terminal resolves glyphs through it and New finds
// configured families in it.
func SystemFonts() *fontscan.FontMap {
	sysOnce.Do(func() {
		sysFonts = fontscan.NewFontMap(log.New(io.Discard, "", 0))
		dir, _ := os.UserCacheDir()
		if err := sysFonts.UseSystemFonts(dir); err != nil {
			log.Printf("theme: system fonts: %q", err)
		}
	})
	return sysFonts
}

// NerdAlias is the short family name fontscan indexes Nerd Fonts v3 under
// ("JetBrainsMono Nerd Font" is found only as "JetBrainsMono NF"); fontconfig
// knows both. Returns name unchanged for other fonts.
func NerdAlias(name string) string {
	for long, short := range map[string]string{" Nerd Font Mono": " NFM", " Nerd Font Propo": " NFP"} {
		if strings.HasSuffix(name, long) {
			return strings.TrimSuffix(name, long) + short
		}
	}
	if strings.HasSuffix(name, " Nerd Font") {
		return strings.TrimSuffix(name, " Nerd Font") + " NF"
	}
	return name
}

// systemFaces loads family's installed faces, one per weight asked for: the
// closest weight it has, pinned to the asked one when the font is variable.
// They are named family so font.Font{Typeface: family} selects them.
func systemFaces(family string, weights ...font.Weight) []font.FontFace {
	fm := SystemFonts()
	locs := fm.FindSystemFonts(family)
	if len(locs) == 0 {
		locs = fm.FindSystemFonts(NerdAlias(family))
	}
	type cand struct {
		f      *fontapi.Font
		weight float32
	}
	var cands []cand
	for _, l := range locs {
		file, err := os.Open(l.File)
		if err != nil {
			continue
		}
		faces, err := fontapi.ParseTTC(file)
		file.Close()
		if err != nil || int(l.Index) >= len(faces) {
			continue
		}
		f := faces[l.Index].Font
		d := f.Describe()
		if d.Aspect.Style != fontapi.StyleNormal {
			continue
		}
		cands = append(cands, cand{f, float32(d.Aspect.Weight)})
	}
	var out []font.FontFace
	for _, w := range weights {
		want := float32(400 + w)
		best := -1
		for i, c := range cands {
			if best < 0 || abs(c.weight-want) < abs(cands[best].weight-want) {
				best = i
			}
		}
		if best >= 0 {
			out = append(out, font.FontFace{Font: font.Font{Typeface: font.Typeface(family), Weight: w}, Face: varFace{f: cands[best].f, wght: want}})
		}
	}
	return out
}

func abs(x float32) float32 { return max(x, -x) }

func loadFonts(uiFam, monoFam string, fallback []string) fontSet {
	var faces []font.FontFace
	var errs []error
	ui := font.Font{Typeface: uiFamily}
	if !strings.EqualFold(uiFam, uiFamily) {
		faces = systemFaces(uiFam, font.Normal, font.Medium, font.SemiBold, font.Bold)
		if len(faces) > 0 {
			ui = font.Font{Typeface: font.Typeface(uiFam)}
		} else {
			errs = append(errs, fmt.Errorf("font.ui_family: %q is not installed; using %s", uiFam, uiFamily))
		}
	}
	for _, w := range []font.Weight{font.Normal, font.Medium, font.SemiBold, font.Bold} {
		if ui.Typeface != uiFamily {
			break
		}
		ld, err := ot.NewLoader(bytes.NewReader(geistTTF))
		if err != nil {
			log.Printf("theme: geist: %q", err)
			break
		}
		f, err := fontapi.NewFont(ld)
		if err != nil {
			log.Printf("theme: geist: %q", err)
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
	if !strings.EqualFold(monoFam, monoFamily) {
		monoFaces = systemFaces(monoFam, font.Normal, font.Bold)
		if len(monoFaces) > 0 {
			mono = font.Font{Typeface: font.Typeface(monoFam)}
		} else {
			errs = append(errs, fmt.Errorf("font.mono_family: %q is not installed; using %s", monoFam, monoFamily))
		}
	}
	for file, w := range map[string]font.Weight{
		"JetBrainsMonoNerdFont-Regular.ttf": font.Normal,
		"JetBrainsMonoNerdFont-Bold.ttf":    font.Bold,
	} {
		if mono.Typeface != monoFamily {
			break
		}
		b, err := os.ReadFile(monoDir + file)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(b)
		if err != nil {
			log.Printf("theme: %q: %q", file, err)
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
	for _, fb := range fallback {
		fs := systemFaces(fb, font.Normal, font.Bold)
		if len(fs) == 0 {
			errs = append(errs, fmt.Errorf("font.mono_fallback: %q is not installed", fb))
			continue
		}
		faces = append(faces, fs...)
		mono.Typeface += font.Typeface(", " + fb)
	}
	if b, err := os.ReadFile(emojiFile); err == nil {
		if f, err := opentype.Parse(b); err == nil {
			faces = append(faces, font.FontFace{Font: font.Font{Typeface: emojiFamily}, Face: f})
			mono.Typeface += ", " + emojiFamily
		} else {
			log.Printf("theme: emoji: %q", err)
		}
	}
	return fontSet{faces, ui, mono, errors.Join(errs...)}
}
