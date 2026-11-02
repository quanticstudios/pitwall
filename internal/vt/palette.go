package vt

import (
	"fmt"
	"math"
	"sync"
)

// Palette is the terminal's own colors: what OSC 10/11/12 and OSC 4 queries
// report to programs, and what the GUI draws for default and ANSI colors.
// Values are 0xRRGGBB.
type Palette struct {
	Fg, Bg, Cursor uint32
	ANSI           [16]uint32
}

// DefaultPalette is aide's dark terminal palette. The emulator answers color
// queries from it and the GUI theme draws with it, so the two always agree.
var DefaultPalette = Palette{
	Fg: 0xf4f4f6, Bg: 0x08090c, Cursor: 0xffffff,
	ANSI: [16]uint32{
		0x0d0d0d, 0xff6161, 0x59d499, 0xffc533, 0x57c1ff, 0xbb9af7, 0x7dcfff, 0xcdcdcd,
		0x242728, 0xff6161, 0x59d499, 0xffc533, 0x57c1ff, 0xbb9af7, 0x7dcfff, 0xffffff,
	},
}

var (
	paletteMu sync.Mutex
	palette   = DefaultPalette
)

// SetPalette sets the palette emulators made from now on start with. A
// running one takes a new palette through its SetPalette method.
func SetPalette(p Palette) {
	paletteMu.Lock()
	defer paletteMu.Unlock()
	palette = p
}

func currentPalette() Palette {
	paletteMu.Lock()
	defer paletteMu.Unlock()
	return palette
}

// Light reports whether the background is light: black text reads better
// on it than white.
func (p Palette) Light() bool {
	lum := func(v uint32) float64 {
		s := float64(v&0xff) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	l := 0.2126*lum(p.Bg>>16) + 0.7152*lum(p.Bg>>8) + 0.0722*lum(p.Bg)
	return (l+0.05)/0.05 > 1.05/(l+0.05)
}

// schemeReport is the color scheme report, CSI ? 997 ; 1 n for a dark
// palette and 2 for a light one, as contour, kitty and Ghostty send it.
func (p Palette) schemeReport() string {
	n := 1
	if p.Light() {
		n = 2
	}
	return fmt.Sprintf("\x1b[?997;%dn", n)
}
