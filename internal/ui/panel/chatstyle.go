package panel

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The chat window's spacing, on the 4 dp grid of the type and spacing
// spec, built from the theme's steps.
const (
	gapTurn    = theme.Space2XL                 // the end of a turn to the next prompt
	gapPrompt  = theme.SpaceL                   // a prompt to its work log
	gapInTurn  = theme.SpaceM                   // the work log to the reply, the reply to a card
	gapFoot    = theme.SpaceS                   // a reply to its footer
	gapPara    = theme.SpaceS + theme.Space2XS  // between a reply's paragraphs
	gapItem    = theme.SpaceXS                  // between list items
	gapHeadTop = theme.SpaceL + theme.SpaceXS   // above a heading
	gapHeadEnd = theme.SpaceXS + theme.Space2XS // below a heading
	gapCode    = theme.SpaceM                   // around a code block
	gapShowAll = theme.SpaceXS                  // a box to its Show all
	rowPad     = theme.SpaceM                   // a tool row's sides
	hit        = theme.SpaceXL + theme.SpaceXS  // the smallest control, both ways
	rowH       = hit + theme.Space2XS           // a tool row and a header bar
	cardPadX   = theme.SpaceL
	cardPadY   = theme.SpaceM
	listIndent = theme.SpaceL + theme.SpaceXS // a list item's indent
)

// tstyle is how a run of text draws: font, size, color, line height.
type tstyle struct {
	f    font.Font
	size unit.Sp
	c    color.NRGBA
	lh   float32
}

// prose is reply text: Large, Fg a little toward Bg so long light text
// does not glare; the user's own message keeps the prompt's style.
func (d *cdraw) prose() tstyle {
	if d.own {
		return d.promptStyle()
	}
	return tstyle{d.th.UIFont, d.th.Sp(theme.Large), theme.Mix(d.th.Fg, d.th.Bg, 0.10), 1.6}
}

// promptStyle is a user prompt in its bubble.
func (d *cdraw) promptStyle() tstyle {
	return tstyle{d.th.UIFont, d.th.Sp(theme.Large), d.th.Fg, 1.55}
}

// mono is code, diffs, commands and tool output, at the code size.
func (d *cdraw) mono() tstyle {
	return tstyle{d.th.MonoFont, d.codeSp(d.th.Sp(theme.Small)), theme.Mix(d.th.Fg, d.th.Bg, 0.15), 1.55}
}

// codeSp is size scaled by the transcript's CodeScale.
func (d *cdraw) codeSp(size unit.Sp) unit.Sp {
	if d.t == nil || d.t.CodeScale <= 0 {
		return size
	}
	return unit.Sp(float32(size) * d.t.CodeScale)
}

// body is a card's text.
func (d *cdraw) body(c color.NRGBA) tstyle { return tstyle{d.th.UIFont, d.th.Sp(theme.Body), c, 1.45} }

// small is a description or a meta line.
func (d *cdraw) small(c color.NRGBA) tstyle {
	return tstyle{d.th.UIFont, d.th.Sp(theme.Small), c, 1.45}
}
