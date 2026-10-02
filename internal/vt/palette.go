package vt

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
