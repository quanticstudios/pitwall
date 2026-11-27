package sidebar

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Every tab is a tile with no outline: a faint fill while live, none once
// finished, a tint of Primary when selected, of amber or red when it needs
// the user. Its state sits in a right column under the time: a 6dp dot
// and a word.

// tileGap is the space between two tiles.
const tileGap unit.Dp = 4

// finished reports whether a's tab reads as finished: Done, Plan Ready or
// Parked. Its tile drops the fill and its title dims.
func finished(a *model.Activity) bool {
	return a != nil && (a.State == model.StateCompleted || a.State == model.StatePlanReady || a.State == model.StateParked)
}

// tileStyle is a tab tile's fill, the sidebar's for none. Attention
// outranks selection, which outranks finished.
func tileStyle(th *theme.Theme, a *model.Activity, selected bool) color.NRGBA {
	switch {
	case model.Tier(a) == model.TierAttention:
		col := th.Yellow
		if a.State == model.StateError {
			col = th.Red
		}
		return theme.Mix(th.Sidebar, col, 0.10)
	case selected:
		return theme.Mix(th.Sidebar, th.Primary, 0.13)
	case finished(a):
		return th.Sidebar
	}
	return theme.Mix(th.Sidebar, th.Fg, 0.025)
}

// tileHairline is the 1dp rule and corner guides inside a tile of fill.
func tileHairline(th *theme.Theme, fill color.NRGBA) color.NRGBA {
	return theme.Mix(fill, th.Fg, 0.06)
}

// tileHover is a tile's fill under the pointer.
func tileHover(th *theme.Theme, fill color.NRGBA) color.NRGBA {
	return theme.Mix(fill, th.Fg, 0.04)
}

// titleStyle is a tab title's color and weight: dimmer and lighter once
// finished.
func titleStyle(th *theme.Theme, a *model.Activity) (color.NRGBA, bool) {
	if finished(a) {
		return theme.Mix(th.Fg, th.Muted, 0.56), false
	}
	return th.Fg, true
}

// stateMark is what the right column's line 2 says of an activity.
type stateMark struct {
	word   string      // the state, or the program a shell runs
	dot    color.NRGBA // the state's color; Muted for a ring
	loud   bool        // the word takes the dot's color; else Muted
	ring   bool        // the dot is a ring: Background, Watching
	pulses bool        // the dot pulses: Working, Connecting
}

// markOf is a's stateMark.
func markOf(th *theme.Theme, a model.Activity) stateMark {
	m := stateMark{word: PillText(a), dot: StateColor(th, a.State), pulses: model.Pulses(&a)}
	if a.State == model.StatePROpen {
		m.dot = th.Purple
	}
	switch a.State {
	case model.StateWorking:
		m.loud, m.ring = !a.Background, a.Background
	case model.StateConnecting, model.StatePendingApproval, model.StateAwaitingInput, model.StateError:
		m.loud = true
	case model.StateWatching:
		m.ring = true
	}
	if m.ring {
		m.dot = th.Muted // why: blue means mid-turn; a ring's tab waits at its prompt while something it started runs
	}
	return m
}

// whyLine is the line a tab that needs the user adds for the reason, in
// its state's color: "" for none.
func whyLine(a *model.Activity) string {
	if model.Tier(a) != model.TierAttention {
		return ""
	}
	return firstLine(a.Detail)
}

// stateDot draws m's 6dp dot on base, t seconds into its pulse.
func stateDot(gtx layout.Context, m stateMark, base color.NRGBA, t float64) layout.Dimensions {
	d := gtx.Dp(6)
	col := m.dot
	if m.pulses {
		col = theme.Mix(base, col, pulse(t))
	}
	if m.ring {
		w := max(gtx.Dp(1.5), 1)
		path := clip.Ellipse{Min: image.Pt(0, 0), Max: image.Pt(d, d)}.Path(gtx.Ops)
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: path, Width: float32(w)}.Op())
	} else {
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
	}
	return layout.Dimensions{Size: image.Pt(d, d)}
}

// stateWord draws m's dot then its word, 5dp apart, h high.
func stateWord(gtx layout.Context, th *theme.Theme, m stateMark, base color.NRGBA, h int, t float64) layout.Dimensions {
	col := th.Readable(th.Muted, base)
	if m.loud {
		col = th.Readable(m.dot, base)
	}
	gtx.Constraints.Min = image.Point{}
	return hrowFit(gtx, h, gtx.Dp(5),
		item{w: func(gtx layout.Context) layout.Dimensions { return stateDot(gtx, m, base, t) }},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), col, m.word)
		}})
}

// tileRadius is a tile's corner radius.
const tileRadius unit.Dp = 10

// drawTile paints a tile's fill in rect, unless it is the sidebar's.
func drawTile(gtx layout.Context, th *theme.Theme, rect image.Rectangle, fill color.NRGBA) {
	if fill != th.Sidebar {
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(tileRadius)).Op(gtx.Ops))
	}
}

// subsTop and subsBottom are the space a tile keeps above its sub-rows,
// the hairline included, and below them.
func subsTop(gtx layout.Context) int    { return max(gtx.Dp(1), 1) + gtx.Dp(3) }
func subsBottom(gtx layout.Context) int { return gtx.Dp(4) }

// subsHairline draws the 1dp rule between a tab and its sub-rows at y,
// across the content of a tile filled fill, w wide.
func subsHairline(gtx layout.Context, th *theme.Theme, fill color.NRGBA, y, w int) {
	in := gtx.Dp(12)
	paint.FillShape(gtx.Ops, tileHairline(th, fill), clip.Rect{Min: image.Pt(in, y), Max: image.Pt(w-in, y+max(gtx.Dp(1), 1))}.Op())
}
