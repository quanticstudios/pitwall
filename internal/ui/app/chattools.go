package app

import (
	"cmp"
	"image"
	"log"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// toolChoice is one entry in the chat header's Tools dropdown.
type toolChoice struct {
	mode  string // [chat] tool_calls value
	label string // button label
	name  string // menu row title
	desc  string // one-line description in the menu
}

var toolChoices = [...]toolChoice{
	{config.ToolsAll, "Tools", "All", "Show every tool call"},
	{config.ToolsFailed, "Failed only", "Failed only", "Hide calls once they succeed"},
	{config.ToolsNone, "No tools", "None", "Hide tool calls; keep thinking"},
	{config.ToolsZen, "Zen", "Zen", "Only your messages and final replies"},
	{config.ToolsHeadlines, "Headlines", "Headlines", "Each finished turn as one line; click to open"},
}

// toolsMenuMinW is the Tools dropdown's minimum inner width.
const toolsMenuMinW unit.Dp = 264

// toolsIndex is mode's index in toolChoices, 0 (All) when unset or unknown.
func toolsIndex(mode string) int {
	for i, c := range toolChoices {
		if c.mode == mode {
			return i
		}
	}
	return 0
}

// toolsLabel is the chat header's Tools button label for mode.
func toolsLabel(mode string) string {
	return toolChoices[toolsIndex(mode)].label
}

// toolsTip is the Tools button's tooltip for mode.
func toolsTip(mode string) string {
	return "Tool calls: " + strings.ToLower(toolChoices[toolsIndex(mode)].name)
}

// saveTools keeps mode in path's [chat] tool_calls, as the Settings
// control does: all, the default, removes the key.
func saveTools(path, mode string) error {
	if mode == config.ToolsAll {
		return config.RemoveKey(path, "chat", "tool_calls")
	}
	return config.SetKey(path, "chat", "tool_calls", config.Quote(mode))
}

// setTools applies mode to [chat] tool_calls, shown at once and written
// to config.toml.
func (u *ui) setTools(mode string) {
	mode = toolChoices[toolsIndex(mode)].mode
	u.cfg.Chat.ToolCalls = mode
	if err := saveTools(cmp.Or(u.cfg.Path, config.Path()), mode); err != nil {
		log.Printf("tool calls: %q", err)
	}
	if u.invalidate != nil {
		u.invalidate()
	}
}

// setTools opens or closes the Tools dropdown at now.
func (h *chatHead) setTools(on bool, now time.Time) {
	if h.toolsOpen != on {
		h.toolsOpen, h.toolsAt = on, now
		if on {
			h.toolsReq, h.toolsFocused, h.toolsHover = false, false, -1
			h.setMenu(false, now)
		}
	}
}

// closeTools closes the Tools dropdown at once without a fade, as when
// the chat leaves view.
func (h *chatHead) closeTools() {
	if h.toolsOpen {
		h.toolsOpen, h.toolsAt, h.toolsReq, h.toolsFocused = false, time.Time{}, false, false
	}
}

// toolsEvents handles keyboard navigation, focus loss, outside presses
// and item clicks for an open Tools dropdown before the header draws.
func (u *ui) toolsEvents(gtx gl.Context, h *chatHead) {
	if !h.toolsOpen {
		return
	}
	if !h.toolsReq {
		gtx.Execute(key.FocusCmd{Tag: &h.toolsTag})
		h.toolsReq = true
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &h.toolsPanel, Kinds: pointer.Press}); !ok {
			break
		}
	}
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: &h.toolsBackdrop, Kinds: pointer.Press},
			key.FocusFilter{Target: &h.toolsTag},
			key.Filter{Focus: &h.toolsTag, Name: key.NameEscape},
			key.Filter{Focus: &h.toolsTag, Name: key.NameUpArrow},
			key.Filter{Focus: &h.toolsTag, Name: key.NameDownArrow},
			key.Filter{Focus: &h.toolsTag, Name: key.NameReturn},
			key.Filter{Focus: &h.toolsTag, Name: key.NameEnter},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			// why: a press on the trigger button is left to h.tools.Clicked, which toggles it shut.
			if !e.Position.Round().In(h.toolsAnchor) {
				h.setTools(false, gtx.Now)
			}
		case key.FocusEvent:
			if h.toolsFocused && !e.Focus {
				h.setTools(false, gtx.Now)
			}
			h.toolsFocused = e.Focus
		case key.Event:
			if e.State != key.Press {
				continue
			}
			switch e.Name {
			case key.NameEscape:
				h.setTools(false, gtx.Now)
			case key.NameUpArrow:
				h.toolsSel = (h.toolsSel - 1 + len(toolChoices)) % len(toolChoices)
			case key.NameDownArrow:
				h.toolsSel = (h.toolsSel + 1) % len(toolChoices)
			case key.NameReturn, key.NameEnter:
				u.setTools(toolChoices[h.toolsSel].mode)
				h.setTools(false, gtx.Now)
			}
		}
	}
	for i := range toolChoices {
		for h.toolsItems[i].Clicked(gtx) {
			u.setTools(toolChoices[i].mode)
			h.setTools(false, gtx.Now)
		}
	}
}

// toolsMenu draws the Tools dropdown under its button, with every choice,
// its one-line description, and a check on the current one.
func (u *ui) toolsMenu(gtx gl.Context, h *chatHead) {
	th := u.th
	cur := toolsIndex(u.cfg.Chat.ToolCalls)
	pad := gtx.Dp(theme.SpaceXS)
	padX := gtx.Dp(theme.SpaceS)
	padY := gtx.Dp(theme.SpaceXS + theme.Space2XS)
	lineGap := gtx.Dp(theme.Space2XS)
	checkSize := gtx.Dp(headIcon)
	checkGap := gtx.Dp(theme.SpaceS)

	headCall, headSz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Tool calls")
	headTop, headBot := gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceXS)
	headH := headTop + headSz.Y + headBot

	type measured struct {
		titleCall, descCall op.CallOp
		titleSz, descSz     image.Point
		h                   int
	}
	var rows [len(toolChoices)]measured
	widest := max(gtx.Dp(toolsMenuMinW), headSz.X+2*padX)
	totalRowsH := 0
	for i, ch := range toolChoices {
		tc, ts := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, ch.name)
		dc, ds := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, ch.desc)
		rh := padY + ts.Y + lineGap + ds.Y + padY
		rows[i] = measured{tc, dc, ts, ds, rh}
		widest = max(widest, max(ts.X, ds.X)+padX+checkGap+checkSize+padX)
		totalRowsH += rh
	}
	margin := gtx.Dp(theme.SpaceS)
	maxRowW := max(gtx.Dp(120), gtx.Constraints.Max.X-2*margin-2*pad)
	rowW := min(widest, maxRowW)
	box := image.Pt(rowW+2*pad, headH+totalRowsH+2*pad)
	at := kit.Place(h.toolsAnchor, box, image.Rectangle{Max: gtx.Constraints.Max}, kit.BelowEnd, gtx.Dp(theme.SpaceXS), margin)

	t := anim.At(gtx, h.toolsAt, menuIn)
	if !h.toolsOpen {
		t = 1 - anim.At(gtx, h.toolsAt, menuOut)
	}
	hovered := -1
	m := op.Record(gtx.Ops)
	if h.toolsOpen {
		bd := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		pass := pointer.PassOp{}.Push(gtx.Ops)
		event.Op(gtx.Ops, &h.toolsBackdrop)
		pass.Pop()
		bd.Pop()
	}
	fade := paint.PushOpacity(gtx.Ops, t)
	sc := 0.97 + 0.03*t
	edge := f32.Pt(float32(at.X+box.X), float32(at.Y))
	grow := op.Affine(f32.AffineId().Scale(edge, f32.Pt(sc, sc))).Push(gtx.Ops)
	o := op.Offset(at).Push(gtx.Ops)
	rect := image.Rectangle{Max: box}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	cl := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &h.toolsPanel)
	event.Op(gtx.Ops, &h.toolsTag)
	cl.Pop()

	// Section heading.
	ho := op.Offset(image.Pt(pad+padX, pad+headTop)).Push(gtx.Ops)
	headCall.Add(gtx.Ops)
	ho.Pop()

	y := pad + headH
	textMaxW := max(0, rowW-padX-checkGap-checkSize-padX)
	checkCol := th.Readable(th.Primary, th.SurfaceElevated)
	for i := range toolChoices {
		rm := rows[i]
		rowSize := image.Pt(rowW, rm.h)
		b := &h.toolsItems[i]
		ro := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(rowSize)
		b.Layout(g, func(gtx gl.Context) gl.Dimensions {
			if b.Hovered() {
				hovered = i
			}
			rr := clip.UniformRRect(image.Rectangle{Max: rowSize}, gtx.Dp(theme.RadiusControl))
			switch {
			case b.Pressed():
				paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceElevated, th.Fg, 0.12), rr.Op(gtx.Ops))
			case i == h.toolsSel:
				paint.FillShape(gtx.Ops, th.SelectedBg, rr.Op(gtx.Ops))
			case b.Hovered():
				paint.FillShape(gtx.Ops, th.Pressed, rr.Op(gtx.Ops))
			}
			if gtx.Focused(b) {
				kit.FocusRing(gtx, th, image.Rectangle{Max: rowSize}, gtx.Dp(theme.RadiusControl))
			}
			to := op.Offset(image.Pt(padX, padY)).Push(gtx.Ops)
			tc := clip.Rect{Max: image.Pt(min(rm.titleSz.X, textMaxW), rm.titleSz.Y)}.Push(gtx.Ops)
			rm.titleCall.Add(gtx.Ops)
			tc.Pop()
			to.Pop()
			do := op.Offset(image.Pt(padX, padY+rm.titleSz.Y+lineGap)).Push(gtx.Ops)
			dc := clip.Rect{Max: image.Pt(min(rm.descSz.X, textMaxW), rm.descSz.Y)}.Push(gtx.Ops)
			rm.descCall.Add(gtx.Ops)
			dc.Pop()
			do.Pop()
			if i == cur {
				co := op.Offset(image.Pt(rowW-padX-checkSize, (rm.h-checkSize)/2)).Push(gtx.Ops)
				sidebar.Icon(gtx, "check", checkSize, checkCol)
				co.Pop()
			}
			defer clip.Rect{Max: rowSize}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
			return gl.Dimensions{Size: rowSize}
		})
		ro.Pop()
		y += rm.h
	}
	o.Pop()
	grow.Pop()
	fade.Pop()
	op.Defer(gtx.Ops, m.Stop())

	if h.toolsOpen && hovered != h.toolsHover {
		h.toolsHover = hovered
		if hovered >= 0 && hovered != h.toolsSel {
			h.toolsSel = hovered
			gtx.Execute(op.InvalidateCmd{})
		}
	}
}
