package daemon

import (
	"maps"
	"slices"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// SetPalette makes p the terminal palette: new panes start with it, and the
// running ones answer color queries with it from now on and report the
// change to programs that asked with mode 2031.
func (d *Daemon) SetPalette(p vt.Palette) {
	vt.SetPalette(p)
	d.mu.Lock()
	panes := slices.Collect(maps.Values(d.panes))
	d.mu.Unlock()
	for _, pn := range panes {
		if s, ok := pn.(interface{ SetPalette(vt.Palette) }); ok {
			s.SetPalette(p)
		}
	}
}
