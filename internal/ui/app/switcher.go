package app

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Ported from aide's WorkspaceSwitcherOverlay (workspace-switcher mode): a
// 288dp floating card, 16dp from the right edge, vertically centred, fading
// and sliding in over 120ms.
const fadeIn = 120 * time.Millisecond

func projectName(st *model.State, id string) string {
	for _, p := range st.Projects {
		if p.ID == id {
			return p.Name
		}
	}
	return "Unknown project"
}

// textCall records one line of text, cut to the constraints' width.
func textCall(gtx gl.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, s string) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	mat := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	matCall := mat.Stop()
	gtx.Constraints.Min = image.Point{}
	dims := widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, f, size, s, matCall)
	return m.Stop(), dims.Size
}

// drawText draws one line with its top-left at p and returns its height.
func drawText(gtx gl.Context, th *theme.Theme, p image.Point, f font.Font, size unit.Sp, c color.NRGBA, s string) int {
	gtx.Constraints.Max.X -= p.X
	call, sz := textCall(gtx, th, f, size, c, s)
	defer op.Offset(p).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return sz.Y
}

func semibold(f font.Font) font.Font { f.Weight = font.SemiBold; return f }

// chipColors follows aide's WorkspaceStateBadges.
func chipColors(th *theme.Theme, s model.AgentState) (bg, fg color.NRGBA) {
	fg = th.Green
	switch s {
	case model.StatePendingApproval, model.StateAwaitingInput:
		fg = th.Yellow
	case model.StateError:
		fg = th.Red
	case model.StateWorking, model.StateConnecting:
		fg = th.Blue
	case model.StatePlanReady:
		fg = th.Purple
	}
	bg = fg
	bg.A = 0x26 // the -soft tokens are 15% alpha
	return bg, fg
}

// chip records a rounded label and returns it with its size.
func chip(gtx gl.Context, th *theme.Theme, bg, border, fg color.NRGBA, s string) (op.CallOp, image.Point) {
	m := op.Record(gtx.Ops)
	call, ts := textCall(gtx, th, semibold(th.UIFont), th.SmallSize, fg, s)
	pad := image.Pt(gtx.Dp(7), gtx.Dp(2))
	sz := ts.Add(pad.Mul(2))
	r := sz.Y / 2
	if border.A > 0 {
		paint.FillShape(gtx.Ops, border, clip.UniformRRect(image.Rectangle{Max: sz}, r).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rect(1, 1, sz.X-1, sz.Y-1), r-1).Op(gtx.Ops))
	o := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return m.Stop(), sz
}

func (u *ui) drawSwitcher(gtx gl.Context, st *model.State) {
	th := u.th
	ws := ordered(st)
	if len(ws) == 0 {
		return
	}
	t := min(1, float32(gtx.Now.Sub(u.shownAt))/float32(fadeIn))
	if t < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
	ease := 1 - (1-t)*(1-t)*(1-t)

	activities := map[string][]model.Activity{}
	for _, a := range st.Activities {
		activities[a.WorkspaceID] = append(activities[a.WorkspaceID], a)
	}

	pad := gtx.Dp(12)
	width := gtx.Dp(288)
	inner := width - 2*pad
	cgtx := gtx
	cgtx.Constraints = gl.Constraints{Max: image.Pt(inner, gtx.Constraints.Max.Y)}

	// Record the card body first; its height places the card.
	body := op.Record(gtx.Ops)
	y := 0
	project := ""
	if w := findWorkspace(st, u.nav.workspace); w != nil {
		project = projectName(st, w.ProjectID)
	}
	y += drawText(cgtx, th, image.Pt(0, y), semibold(th.UIFont), th.SmallSize, th.Muted, "WORKSPACE SWITCHER")
	y += gtx.Dp(4)
	titleY := y
	y += drawText(cgtx, th, image.Pt(0, y), semibold(th.UIFont), unit.Sp(14), th.Fg, project)
	kx := inner
	for _, k := range []string{"Space", "Alt"} {
		call, sz := chip(cgtx, th, th.SurfaceElevated, th.Border, th.TermFg, k)
		kx -= sz.X
		o := op.Offset(image.Pt(kx, titleY-gtx.Dp(8))).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		kx -= gtx.Dp(4)
	}
	y += pad

	lastProject := ""
	for i, w := range ws {
		if w.ProjectID != lastProject {
			lastProject = w.ProjectID
			if i > 0 {
				y += gtx.Dp(6)
			}
			y += drawText(cgtx, th, image.Pt(gtx.Dp(4), y), semibold(th.UIFont), th.SmallSize, th.Muted,
				strings.ToUpper(projectName(st, w.ProjectID)))
			y += gtx.Dp(4)
		}
		rowPad := image.Pt(gtx.Dp(12), gtx.Dp(8))
		current := w.ID == u.nav.workspace
		nameC := th.Muted
		if current {
			nameC = th.Fg
		}

		// Right side: the activity chip, then the Alt+digit index.
		rgtx := cgtx
		var chips []op.CallOp
		var sizes []image.Point
		// aide wraps up to two chips onto a second line; 288dp only fits one
		// beside the name, so show the one that needs attention most.
		if a := model.Aggregate(activities[w.ID]); a != nil {
			bg, fg := chipColors(th, a.State)
			c, s := chip(rgtx, th, bg, color.NRGBA{}, fg, model.PillLabel(*a))
			chips, sizes = append(chips, c), append(sizes, s)
		}
		c, s := chip(rgtx, th, th.SurfaceElevated, th.Border, th.Muted, strconv.Itoa(i+1))
		chips, sizes = append(chips, c), append(sizes, s)
		chipsW := 0
		for _, s := range sizes {
			chipsW += s.X + gtx.Dp(6)
		}

		lgtx := cgtx
		lgtx.Constraints.Max.X = max(0, inner-2*rowPad.X-chipsW)
		nameCall, nameSz := textCall(lgtx, th, semibold(th.UIFont), unit.Sp(14), nameC, w.Name)
		brCall, brSz := textCall(lgtx, th, th.MonoFont, th.SmallSize, th.Muted, w.Branch)
		rowH := nameSz.Y + brSz.Y + 2*rowPad.Y
		if current {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, y, inner, y+rowH), gtx.Dp(4)).Op(gtx.Ops))
		}
		o := op.Offset(image.Pt(rowPad.X, y+rowPad.Y)).Push(gtx.Ops)
		nameCall.Add(gtx.Ops)
		op.Offset(image.Pt(0, nameSz.Y)).Add(gtx.Ops)
		brCall.Add(gtx.Ops)
		o.Pop()
		x := inner - rowPad.X
		for j := len(chips) - 1; j >= 0; j-- {
			x -= sizes[j].X
			o := op.Offset(image.Pt(x, y+(rowH-sizes[j].Y)/2)).Push(gtx.Ops)
			chips[j].Add(gtx.Ops)
			o.Pop()
			x -= gtx.Dp(6)
		}
		y += rowH + gtx.Dp(4)
	}
	bodyCall := body.Stop()

	h := min(y+2*pad, gtx.Constraints.Max.Y-2*pad)
	x := gtx.Constraints.Max.X - gtx.Dp(16) - width + int(float32(gtx.Dp(12))*(1-ease))
	top := (gtx.Constraints.Max.Y - h) / 2
	defer paint.PushOpacity(gtx.Ops, ease).Pop()
	defer op.Offset(image.Pt(x, top)).Push(gtx.Ops).Pop()

	card := image.Rectangle{Max: image.Pt(width, h)}
	r := gtx.Dp(12)
	// floating-surface: a soft drop shadow, a 16% white hairline, popover fill.
	for i, a := range []uint8{0x30, 0x20, 0x10} {
		g := gtx.Dp(unit.Dp(2 * (i + 1)))
		paint.FillShape(gtx.Ops, color.NRGBA{A: a}, clip.UniformRRect(card.Inset(-g).Add(image.Pt(0, g)), r+g).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x29}, clip.UniformRRect(card, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(card.Inset(1), r-1).Op(gtx.Ops))
	defer clip.UniformRRect(card.Inset(1), r-1).Push(gtx.Ops).Pop()
	defer op.Offset(image.Pt(pad, pad)).Push(gtx.Ops).Pop()
	bodyCall.Add(gtx.Ops)
}
