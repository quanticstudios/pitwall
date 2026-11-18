package panel

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The chat window's spacing, on the 4 dp grid of the type and spacing
// spec.
const (
	gapTurn    = theme.SpaceXL + theme.SpaceS // the end of a turn to the next prompt
	gapPrompt  = theme.SpaceL                 // a prompt to its work log
	gapInTurn  = theme.SpaceM                 // the work log to the reply, the reply to a card
	gapFoot    = theme.SpaceS                 // a reply to its footer
	gapPara    = 10                           // between a reply's paragraphs
	gapItem    = theme.SpaceXS                // between list items
	gapHeadTop = 20                           // above a heading
	gapHeadEnd = 6                            // below a heading
	gapCode    = theme.SpaceM                 // around a code block
	rowPad     = theme.SpaceM                 // a tool row's sides
	hit        = 28                           // the smallest control, both ways
	rowH       = 30                           // a tool row and a header bar
	cardPadX   = theme.SpaceL
	cardPadY   = theme.SpaceM
	listIndent = 20
)

// tstyle is how a run of text draws: font, size, color, line height.
type tstyle struct {
	f    font.Font
	size unit.Sp
	c    color.NRGBA
	lh   float32
}

// prose is reply text: Large, Fg a little toward Bg so long light text
// does not glare.
func (d *cdraw) prose() tstyle {
	return tstyle{d.th.UIFont, d.th.Sp(theme.Large), theme.Mix(d.th.Fg, d.th.Bg, 0.10), 1.6}
}

// promptStyle is a user prompt in its bubble.
func (d *cdraw) promptStyle() tstyle {
	return tstyle{d.th.UIFont, d.th.Sp(theme.Large), d.th.Fg, 1.55}
}

// mono is code, diffs, commands and tool output.
func (d *cdraw) mono() tstyle {
	return tstyle{d.th.MonoFont, d.th.Sp(theme.Small), theme.Mix(d.th.Fg, d.th.Bg, 0.15), 1.55}
}

// body is a card's text.
func (d *cdraw) body(c color.NRGBA) tstyle { return tstyle{d.th.UIFont, d.th.Sp(theme.Body), c, 1.45} }

// small is a description or a meta line.
func (d *cdraw) small(c color.NRGBA) tstyle {
	return tstyle{d.th.UIFont, d.th.Sp(theme.Small), c, 1.45}
}
