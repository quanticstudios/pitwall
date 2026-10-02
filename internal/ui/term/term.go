// Package term draws a vt.Grid with the monospace font and turns key, text,
// paste and mouse input on the pane into PTY bytes.
package term

import (
	"image"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type View struct{}

// Layout draws g filling gtx.Constraints.Max. It returns the input bytes for
// the PTY from this frame, and the cols/rows that fit, which the caller sends
// as a Resize when they differ from g.
func (v *View) Layout(gtx layout.Context, th *theme.Theme, g *vt.Grid, m vt.Modes, focused bool) (dims layout.Dimensions, input []byte, cols, rows int) {
	panic("unimplemented")
}

// CellSize is the pixel size of one cell at the current scale.
func (v *View) CellSize(gtx layout.Context, th *theme.Theme) image.Point { panic("unimplemented") }
