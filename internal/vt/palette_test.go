package vt

import (
	"strings"
	"testing"
	"time"
)

// light is a light palette: Omarchy's Flexoki Light terminal colors.
var light = Palette{
	Fg: 0x100f0f, Bg: 0xfffcf0, Cursor: 0x100f0f,
	ANSI: [16]uint32{0xfffcf0, 0xd14d41, 0x879a39, 0xd0a215, 0x205ea6, 0xce5d97, 0x3aa99f, 0x100f0f,
		0xb7b5ac, 0xd14d41, 0x879a39, 0xd0a215, 0x4385be, 0xce5d97, 0x3aa99f, 0x100f0f},
}

// TestSetPaletteQueries: after SetPalette, OSC 4, 10, 11 and 12 report the
// new colors, and a new emulator starts with the palette vt.SetPalette set.
func TestSetPaletteQueries(t *testing.T) {
	r := &replyBuf{}
	e := New(10, 3, r)
	e.(interface{ SetPalette(Palette) }).SetPalette(light)
	for _, c := range []struct{ q, want string }{
		{"\x1b]10;?\x07", "\x1b]10;rgb:1010/0f0f/0f0f\x07"},
		{"\x1b]11;?\x1b\\", "\x1b]11;rgb:ffff/fcfc/f0f0\x1b\\"},
		{"\x1b]12;?\x07", "\x1b]12;rgb:1010/0f0f/0f0f\x07"},
		{"\x1b]4;4;?;12;?\x07", "\x1b]4;4;rgb:2020/5e5e/a6a6\x07\x1b]4;12;rgb:4343/8585/bebe\x07"},
	} {
		feed(e, c.q)
		r.waitFor(t, c.want)
	}

	SetPalette(light)
	defer SetPalette(DefaultPalette)
	r2 := &replyBuf{}
	feed(New(10, 3, r2), "\x1b]11;?\x07")
	r2.waitFor(t, "\x1b]11;rgb:ffff/fcfc/f0f0\x07")
}

// TestColorSchemeReports: CSI ? 996 n gets the scheme, and a program with
// mode 2031 on gets a report when the background turns light or dark, and
// none for a change that keeps it dark or with the mode off.
func TestColorSchemeReports(t *testing.T) {
	r := &replyBuf{}
	e := New(10, 3, r)
	sp := e.(interface{ SetPalette(Palette) })
	feed(e, "\x1b[?996n")
	r.waitFor(t, "\x1b[?997;1n")
	feed(e, "\x1b[?2031$p") // DECRQM: recognized, reset
	r.waitFor(t, "\x1b[?2031;2$y")

	quiet := func(why string) {
		t.Helper()
		time.Sleep(20 * time.Millisecond)
		r.mu.Lock()
		defer r.mu.Unlock()
		if got := r.b.String(); strings.Contains(got, "997") {
			t.Fatalf("%s: report %q", why, got)
		}
	}
	sp.SetPalette(light)
	quiet("mode 2031 off")
	feed(e, "\x1b[?2031h")
	dark := DefaultPalette
	dark.Fg = 0xeeeeee
	sp.SetPalette(dark)
	r.waitFor(t, "\x1b[?997;1n")
	dark.Fg = 0xdddddd
	sp.SetPalette(dark)
	quiet("still dark")
	sp.SetPalette(light)
	r.waitFor(t, "\x1b[?997;2n")
}

func TestPaletteLight(t *testing.T) {
	if DefaultPalette.Light() || !light.Light() {
		t.Fatal("aide-dark should be dark and Flexoki Light light")
	}
}
