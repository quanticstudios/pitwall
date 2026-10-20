package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A tab whose agent opened other tabs (model.State.Root) is a fleet: its
// row grows a strip under line 2, a bar with one segment per worker in its
// state's color and a chip per state with its count, and the workers' rows
// hang under it, kidIndent further in, until the strip folds them away.

// kidIndent is how much further in a worker's row starts than its root's.
const kidIndent unit.Dp = 14

// Worker states as the strip counts them, in chip order.
const (
	fleetNeed = iota // a permission prompt, a question or a plan
	fleetError
	fleetWorking
	fleetDone
	fleetIdle // no agent running: not started yet, or exited
	fleetStates
)

func fleetState(a *model.Activity) int {
	switch {
	case a == nil:
		return fleetIdle
	case a.State == model.StateError:
		return fleetError
	case a.State == model.StatePendingApproval, a.State == model.StateAwaitingInput, a.State == model.StatePlanReady:
		return fleetNeed
	case a.State == model.StateCompleted:
		return fleetDone
	}
	return fleetWorking
}

func fleetColor(th *theme.Theme, state int, base color.NRGBA) color.NRGBA {
	switch state {
	case fleetNeed:
		return th.Yellow
	case fleetError:
		return th.Red
	case fleetWorking:
		return th.Blue
	case fleetDone:
		return th.Green
	}
	return theme.Mix(base, th.Muted, 0.45)
}

var fleetIcons = [...]string{fleetNeed: icCircleAlert, fleetError: icX, fleetWorking: icLoader, fleetDone: icCheck, fleetIdle: icPower}

// fleetHeight is the strip's height: 6dp, the 4dp bar, 5dp, the chips.
func fleetHeight(gtx layout.Context, th *theme.Theme) int {
	_, l2 := rowLines(gtx, th)
	return gtx.Dp(15) + l2
}

// kidsShown is tab id's workers as the sidebar lists them under it: none
// while its strip is folded.
func (s *Sidebar) kidsShown(v *view, id string) []model.Workspace {
	if s.kidsFolded[id] {
		return nil
	}
	return v.kids[id]
}

// fleetStrip draws tab id's strip in gtx's exact size: the fold chevron
// and the bar, then the chips. Clicking it folds and unfolds the workers.
func (s *Sidebar) fleetStrip(gtx layout.Context, v *view, id string, base color.NRGBA, ghost bool) layout.Dimensions {
	th := v.th
	r := s.row(id)
	kids := v.kids[id]
	size := gtx.Constraints.Max
	_, l2 := rowLines(gtx, th)
	draw := func(gtx layout.Context) layout.Dimensions {
		col := th.Readable(th.Muted, base)
		if !ghost && r.kids.Hovered() {
			col = th.Fg
		}
		cs := gtx.Dp(12)
		o := op.Offset(image.Pt(gtx.Dp(1), gtx.Dp(8)-cs/2)).Push(gtx.Ops)
		chevron(gtx, &r.kidsT, !s.kidsFolded[id], cs, col)
		o.Pop()

		x0, gap := gtx.Dp(20), gtx.Dp(2)
		barW := size.X - x0
		var count [fleetStates]int
		for i, k := range kids {
			st := fleetState(v.activity[k.ID])
			count[st]++
			// Segments share the width; the last one takes the rounding.
			a, b := x0+i*barW/len(kids), x0+(i+1)*barW/len(kids)-gap
			if i == len(kids)-1 {
				b = size.X
			}
			seg := image.Rect(a, gtx.Dp(6), max(b, a+1), gtx.Dp(10))
			paint.FillShape(gtx.Ops, fleetColor(th, st, base), clip.UniformRRect(seg, gtx.Dp(1)).Op(gtx.Ops))
		}

		var chips []item
		for st := range fleetStates {
			if count[st] == 0 {
				continue
			}
			c := fleetColor(th, st, base)
			var rot float32
			if st == fleetWorking && !ghost && !anim.Reduced() {
				// animate-spin, as the connecting loader: 1s linear. The
				// row may not pulse (a done root, folded workers), so the
				// strip asks for its own frames.
				_, frac := math.Modf(v.t(s))
				rot = float32(frac * 2 * math.Pi)
				gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
			}
			chips = append(chips, item{w: func(gtx layout.Context) layout.Dimensions {
				return fleetChip(gtx, th, fleetIcons[st], rot, count[st], c, base, l2)
			}})
		}
		o = op.Offset(image.Pt(x0, gtx.Dp(15))).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(barW, l2))
		hrowFit(cg, l2, gtx.Dp(5), chips...)
		o.Pop()
		return layout.Dimensions{Size: size}
	}
	if ghost {
		return draw(gtx)
	}
	return clickable(gtx, &r.kids, draw)
}

// fleetChip is one state's chip: its icon, turned rot radians, and count
// in col on a col tint.
func fleetChip(gtx layout.Context, th *theme.Theme, icon string, rot float32, n int, col, base color.NRGBA, h int) layout.Dimensions {
	px := gtx.Dp(6)
	gtx.Constraints = layout.Constraints{Max: image.Pt(1<<16, h)}
	m := op.Record(gtx.Ops)
	d := hrowFit(gtx, h, gtx.Dp(3),
		item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icon, gtx.Dp(11), col, rot) }},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.MonoFont), th.Sp(theme.Caption), col, fmt.Sprint(n))
		}})
	call := m.Stop()
	size := image.Pt(d.Size.X+2*px, h)
	paint.FillShape(gtx.Ops, theme.Mix(base, col, 0.14), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(5)).Op(gtx.Ops))
	o := op.Offset(image.Pt(px, 0)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: size}
}

// workerGuide draws the line down the left of a worker's row, h tall, in
// the gap kidIndent leaves.
func workerGuide(gtx layout.Context, th *theme.Theme, h int) {
	x, lw := -gtx.Dp(kidIndent)/2, max(gtx.Dp(1), 1)
	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Muted, 0.35), clip.Rect{Min: image.Pt(x, -gtx.Dp(2)), Max: image.Pt(x+lw, h)}.Op())
}
