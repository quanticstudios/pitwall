package daemon

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

type palettePane struct {
	Pane
	got vt.Palette
}

func (p *palettePane) SetPalette(x vt.Palette) { p.got = x }

// TestSetPalette: a new palette reaches every running pane.
func TestSetPalette(t *testing.T) {
	defer vt.SetPalette(vt.DefaultPalette)
	a, b := &palettePane{}, &palettePane{}
	d := &Daemon{panes: map[string]Pane{"a": a, "b": b}}
	p := vt.Palette{Fg: 0x100f0f, Bg: 0xfffcf0}
	d.SetPalette(p)
	if a.got != p || b.got != p {
		t.Fatalf("panes got %v and %v", a.got, b.got)
	}
}
