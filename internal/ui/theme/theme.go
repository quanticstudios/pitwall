// Package theme holds colors, fonts and metrics ported from aide's renderer
// tokens, so pitwall looks like aide.
package theme

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/text"
	"gioui.org/unit"
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

func Dark() *Theme { panic("unimplemented") }

// ProjectColor maps an aide project color id ("red", "sky", ...) to a color.
func (t *Theme) ProjectColor(id string) color.NRGBA { panic("unimplemented") }
