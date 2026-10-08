package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// workspaceRow draws tab ws's row: its agent's mark or its state icon,
// title and pill, then the branch with its diff stats (or the folder) and
// the time, which the "…" menu trigger and "×" cover on hover. A ghost row is the
// lifted copy under the pointer: no input, no hover buttons, its fill left
// to the caller. A digit other than "" takes the place of the mark or icon.
func (s *Sidebar) workspaceRow(gtx layout.Context, v *view, ws model.Workspace, ghost bool, digit string) (layout.Dimensions, bool) {
	th := v.th
	r := s.row(ws.ID)
	a := v.activity[ws.ID]
	isActive := ws.ID == v.active
	stats, hasStats := v.st.Stats[ws.ID]
	title := Title(ws)

	w := gtx.Constraints.Max.X
	pad, l1, l2 := gtx.Dp(8), gtx.Dp(19.5), gtx.Dp(16.5)
	h := rowHeight(gtx)
	rect := image.Rect(0, 0, w, h)
	rr := gtx.Dp(8)

	animating := model.Pulses(a) && !ghost
	hovered := !ghost && r.hovered()
	base := rowBase(th, a, isActive, hovered)
	selected := s.selected[ws.ID] && !ghost
	if selected && !isActive {
		base = theme.Mix(base, th.Primary, 0.07)
	}
	unseen := v.unseen[ws.ID]
	if ghost {
		unseen = nil
	}
	if unseen != nil {
		base = theme.Mix(base, StateColor(th, unseen.State), 0.1)
	}
	if ghost {
		base = th.SurfaceSecondary
	} else if base != th.Sidebar {
		paint.FillShape(gtx.Ops, base, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	}
	if animating {
		shimmer(gtx, rect, rr, base, v.t(s))
	}
	if selected {
		paint.FillShape(gtx.Ops, theme.Mix(base, th.Primary, 0.55), clip.Stroke{Path: clip.UniformRRect(rect.Inset(1), rr-1).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	}
	if unseen != nil {
		// Something here wants the user: an accent bar down the left edge.
		bar := image.Rect(gtx.Dp(2), gtx.Dp(10), gtx.Dp(5), h-gtx.Dp(10))
		paint.FillShape(gtx.Ops, StateColor(th, unseen.State), clip.UniformRRect(bar, bar.Dx()/2).Op(gtx.Ops))
	}

	// "…" then "×" show on hover in place of line 2's time. The ×'s strokes
	// end at the content's right inset; its 24dp hit area runs past them.
	showMore := !ghost && ((hovered && !s.drag.active) || s.menuWS == ws.ID)
	btn, glyph := gtx.Dp(24), gtx.Dp(16)
	x := (btn-glyph)/2 + glyph/4 // the button's edge to the ×'s edge
	content := func(gtx layout.Context) layout.Dimensions {
		// pl-3 pr-3
		left := gtx.Dp(12)
		inner := w - 2*left
		gtx.Constraints = layout.Exact(image.Pt(inner, l1))
		off := op.Offset(image.Pt(left, pad)).Push(gtx.Ops)
		nameCol := theme.Mix(base, th.Fg, 0.95)
		if isActive || ghost || unseen != nil || model.Tier(a) == model.TierAttention {
			nameCol = th.Fg
		}
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions {
				if digit != "" {
					return gotoDigit(gtx, th, digit, isActive, base)
				}
				return s.stateIcon(gtx, v, ws, a, isActive, base)
			}},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if s.renaming == ws.ID && !ghost {
					return s.renameField(gtx, th)
				}
				return label(gtx, th, semibold(th.UIFont), 13, nameCol, title)
			}},
		}
		if unseen != nil {
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(6)
				paint.FillShape(gtx.Ops, StateColor(th, unseen.State), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(d, d)}
			}})
		}
		if a != nil {
			items = append(items, item{right: unseen == nil, w: func(gtx layout.Context) layout.Dimensions { return pill(gtx, v, s, *a, base) }})
		}
		hrow(gtx, l1, gtx.Dp(8), items...)
		off.Pop()

		// pl-5 under the name, text-[11px] muted/70; the time quieter.
		muted := theme.Mix(base, th.Muted, 0.7)
		quiet := theme.Mix(base, th.Muted, 0.45)
		gtx.Constraints = layout.Exact(image.Pt(inner-gtx.Dp(20), l2))
		off = op.Offset(image.Pt(left+gtx.Dp(20), pad+l1+gtx.Dp(4))).Push(gtx.Ops)
		inRepo := ws.Branch != ""
		where := ws.Branch
		if !inRepo {
			where = ShortPath(v.st.LivePath(ws))
		}
		var line []item
		line = append(line, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.MonoFont, 11, muted, where)
		}})
		if inRepo && hasStats && (stats.Additions > 0 || stats.Deletions > 0) {
			line = append(line, item{w: func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, l2, gtx.Dp(4),
					item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 10, th.Green, fmt.Sprintf("+%d", stats.Additions))
					}},
					item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 10, th.Red, fmt.Sprintf("-%d", stats.Deletions))
					}},
				)
			}})
		}
		switch {
		case showMore:
			line = append(line, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(2*btn-x, 0)}
			}})
		case inRepo && hasStats && stats.MergeStatus == model.MergeConflicts:
			line = append(line, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, 11, th.Red, "Merge conflicts")
			}})
		default:
			if rt := relTime(v.now, ws.UpdatedAt); rt != "" {
				line = append(line, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 11, quiet, rt)
				}})
			}
		}
		hrow(gtx, l2, gtx.Dp(6), line...)
		off.Pop()
		return layout.Dimensions{Size: rect.Size()}
	}
	cg := gtx
	cg.Constraints = layout.Exact(rect.Size())
	if ghost {
		content(cg)
		return layout.Dimensions{Size: rect.Size()}, false
	}
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &r.ctx)
	clickable(cg, &r.click, content)
	area.Pop()

	// "×" at the right of line 2, centred on it, and "…" before it. The ×
	// has no hidden hit area: a stray click there must not close the tab.
	pos := image.Pt(w-gtx.Dp(12)+x-btn, pad+l1+gtx.Dp(4)+(l2-btn)/2)
	if showMore {
		off := op.Offset(pos).Push(gtx.Ops)
		iconButton(gtx, th, &r.close, icX, btn, glyph, true)
		off.Pop()
	}
	pos.X -= btn
	off := op.Offset(pos).Push(gtx.Ops)
	if showMore {
		iconButton(gtx, th, &r.more, icEllipsis, btn, glyph, false)
	} else {
		// Keep the hit area so the hidden button still opens the menu.
		clickable(gtx, &r.more, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(btn, btn)}
		})
	}
	if s.menuWS == ws.ID {
		m := op.Record(gtx.Ops)
		s.menu(gtx, v, ws, btn)
		op.Defer(gtx.Ops, m.Stop())
	}
	off.Pop()
	return layout.Dimensions{Size: rect.Size()}, animating
}

// Title is what a tab's row shows: the name the user gave it, else its
// Label (what it is doing).
func Title(ws model.Workspace) string {
	if ws.NameSet || ws.Label == "" {
		return ws.Name
	}
	return ws.Label
}

func (v *view) t(s *Sidebar) float64 { return v.now.Sub(s.epoch).Seconds() }

// stateIcon is renderWorkspaceStateIcon: a 12px glyph per agent state.
func (s *Sidebar) stateIcon(gtx layout.Context, v *view, ws model.Workspace, a *model.Activity, isActive bool, base color.NRGBA) layout.Dimensions {
	th := v.th
	sz := gtx.Dp(12)
	if ag := v.agent[ws.ID]; ag != "" {
		// The mark draws at 14dp, centred on the 12dp slot the titles align to.
		off := op.Offset(image.Pt(-gtx.Dp(1), -gtx.Dp(1))).Push(gtx.Ops)
		AgentMark(gtx, ag, gtx.Dp(14), th.Fg)
		off.Pop()
		return layout.Dimensions{Size: image.Pt(sz, sz)}
	}
	if a == nil {
		col := th.Muted
		if isActive {
			col = th.Primary
		}
		if ws.Branch == "" {
			return drawIcon(gtx, icTerminal, sz, col, 0) // an idle shell
		}
		return drawIcon(gtx, icGitBranch, sz, col, 0)
	}
	switch a.State {
	case model.StateError:
		return drawIcon(gtx, icCircleAlert, sz, th.Red, 0)
	case model.StatePendingApproval:
		return drawIcon(gtx, icCircleAlert, sz, th.Yellow, 0)
	case model.StateAwaitingInput:
		return drawIcon(gtx, icCircleHelp, sz, th.Yellow, 0)
	case model.StateWorking:
		d := gtx.Dp(6)
		off := op.Offset(image.Pt((sz-d)/2, (sz-d)/2)).Push(gtx.Ops)
		paint.FillShape(gtx.Ops, theme.Mix(base, th.Blue, pulse(v.t(s))), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
		off.Pop()
		return layout.Dimensions{Size: image.Pt(sz, sz)}
	case model.StateConnecting:
		_, frac := math.Modf(v.t(s)) // animate-spin: 1s linear
		return drawIcon(gtx, icLoader, sz, th.Blue, float32(frac*2*math.Pi))
	case model.StateCompleted:
		return drawIcon(gtx, icCircleCheck, sz, th.Green, 0)
	case model.StatePlanReady:
		return drawIcon(gtx, icCircleCheck, sz, th.Purple, 0)
	case model.StateTerminalRunning:
		return drawIcon(gtx, icTerminal, sz, th.Green, 0)
	}
	return drawIcon(gtx, icGitBranch, sz, StateColor(th, a.State), 0)
}

// gotoDigit is a row's goto_tab digit while its modifier is held: a small
// keycap centred on the 12dp icon slot, so the title does not move.
func gotoDigit(gtx layout.Context, th *theme.Theme, digit string, isActive bool, base color.NRGBA) layout.Dimensions {
	sz, d := gtx.Dp(12), gtx.Dp(16)
	fill, col := theme.Mix(base, th.Fg, 0.1), th.Fg
	if isActive {
		fill, col = th.Primary, th.OnPrimary
	}
	off := op.Offset(image.Pt((sz-d)/2, (sz-d)/2)).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, d, d), gtx.Dp(4)).Op(gtx.Ops))
	centered(gtx, image.Pt(d, d), func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), 10, col, digit)
	})
	off.Pop()
	return layout.Dimensions{Size: image.Pt(sz, sz)}
}

// pill is ActivityPill: rounded-full, px-1.5 py-px, 10px semibold, a pulsing
// dot while the agent works.
func pill(gtx layout.Context, v *view, s *Sidebar, a model.Activity, base color.NRGBA) layout.Dimensions {
	th := v.th
	col := StateColor(th, a.State)
	soft := float32(0.14)
	if a.State == model.StatePlanReady {
		soft = 0.15
	}
	bg := theme.Mix(base, col, soft)
	h := gtx.Dp(16.5) // 10px * 1.25 + py-px + 1px transparent border
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max.Y = h
	off := op.Offset(image.Pt(gtx.Dp(7), 0)).Push(gtx.Ops)
	var items []item
	if model.Pulses(&a) {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			d := gtx.Dp(6)
			paint.FillShape(gtx.Ops, theme.Mix(bg, col, pulse(v.t(s))), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(d, d)}
		}})
	}
	items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), 10, col, PillText(a, v.st.Decide.Provider))
	}})
	d := hrowFit(gtx, h, gtx.Dp(4), items...)
	off.Pop()
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(14), h)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: size}, h/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}

func (s *Sidebar) renameField(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	s.editorLaidOut = true
	switch {
	case s.focusEditor:
		gtx.Execute(key.FocusCmd{Tag: &s.editor})
		s.focusEditor, s.selectAll = false, true
	case s.selectAll && gtx.Focused(&s.editor):
		// Gaining focus drops the selection SetCaret made; select the
		// whole name again so typing replaces it, unless typing came first.
		if s.editor.Text() == s.renameFrom {
			s.editor.SetCaret(s.editor.Len(), 0)
		}
		s.selectAll = false
	}
	h := gtx.Constraints.Max.Y
	w := gtx.Constraints.Max.X
	rect := image.Rect(0, 0, w, h)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect, gtx.Dp(4)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Primary, 0.6), clip.Stroke{Path: clip.UniformRRect(rect, gtx.Dp(4)).Path(gtx.Ops), Width: 1}.Op())
	gtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(8), h))
	off := op.Offset(image.Pt(gtx.Dp(4), 0)).Push(gtx.Ops)
	centered(gtx, image.Pt(w-gtx.Dp(8), h), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return s.editor.Layout(gtx, th.Shaper, semibold(th.UIFont), 13, material(gtx, th.Fg), material(gtx, theme.Mix(th.SurfaceSecondary, th.Primary, 0.35)))
	})
	off.Pop()
	return layout.Dimensions{Size: rect.Size()}
}

// PillText is a tab pill's label: the command a busy terminal runs, a
// decision model's recommendation on an approval ("Jev: allow 96%"),
// else aide's label for the state, with " · now" when triage says the
// user is needed now. by is State.Decide.Provider.
func PillText(a model.Activity, by string) string {
	if a.State == model.StateTerminalRunning && a.Detail != "" {
		return a.Detail
	}
	if s := AdviceText(a, by); s != "" {
		return s
	}
	if a.Urgency == "now" && model.NeedsYou(a.State) {
		return model.PillLabel(a) + " · now"
	}
	return model.PillLabel(a)
}

// AdviceText is the recommendation on a pending approval, "Jev: allow
// 96%", with the first risk pitwall sees in the call after it, "Jev:
// allow 96% · sudo"; the risk alone, "Risk: sudo", when the
// recommendation is held out or missing; "" with neither. It is only a
// suggestion.
func AdviceText(a model.Activity, by string) string {
	if a.State != model.StatePendingApproval || a.Advice == "" && a.AdviceRule == "" {
		return ""
	}
	if a.Advice == "" {
		return "Risk: " + a.AdviceRule
	}
	s := fmt.Sprintf("%s: %s %.0f%%", DecideName(by), a.Advice, a.AdviceP*100)
	if a.AdviceRule != "" {
		s += " · " + a.AdviceRule
	}
	return s
}

// DecideName is how the UI names a decision provider.
func DecideName(provider string) string {
	if provider == "jev" {
		return "Jev"
	}
	return "Model"
}

var homeDir = sync.OnceValue(func() string {
	h, _ := os.UserHomeDir()
	return h
})

// ShortPath is p with the home directory written as ~.
func ShortPath(p string) string { return shortPath(p, homeDir()) }

func shortPath(p, home string) string {
	switch {
	case home == "" || home == "/":
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+"/"):
		return "~" + p[len(home):]
	}
	return p
}

// shimmer is .sidebar-working-shimmer: a 200%-wide white gradient (0, .02,
// .04, .02, 0 at 0/40/50/60/100%) sliding from background-position -200% to
// 200% over 3s ease-in-out, repeating.
func shimmer(gtx layout.Context, rect image.Rectangle, r int, base color.NRGBA, t float64) {
	defer clip.UniformRRect(rect, r).Push(gtx.Ops).Pop()
	w := float32(rect.Dx())
	_, p := math.Modf(t / 3)
	e := float32(p * p * (3 - 2*p))
	c := float32(math.Mod(float64(w*(3-4*e)), float64(2*w)))
	white := func(a float32) color.NRGBA { return theme.Mix(base, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, a) }
	stops := []struct{ x, a float32 }{{-1, 0}, {-0.2, 0.02}, {0, 0.04}, {0.2, 0.02}, {1, 0}}
	for _, cx := range []float32{c - 2*w, c, c + 2*w} {
		for i := 0; i+1 < len(stops); i++ {
			x0, x1 := cx+stops[i].x*w, cx+stops[i+1].x*w
			if x1 < 0 || x0 > w {
				continue
			}
			area := clip.Rect{Min: image.Pt(round(x0), rect.Min.Y), Max: image.Pt(round(x1), rect.Max.Y)}.Push(gtx.Ops)
			paint.LinearGradientOp{
				Stop1: f32.Pt(x0, 0), Color1: white(stops[i].a),
				Stop2: f32.Pt(x1, 0), Color2: white(stops[i+1].a),
			}.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			area.Pop()
		}
	}
}

func round(x float32) int { return int(math.Round(float64(x))) }

// pulse is Tailwind's animate-pulse opacity: 1 -> .5 -> 1 every 2s.
func pulse(t float64) float32 {
	return float32(0.75 + 0.25*math.Cos(t*math.Pi))
}

// StateColor is the accent aide uses for a state's pill, dot and icon, and
// the color of the attention ring on a pane.
func StateColor(th *theme.Theme, s model.AgentState) color.NRGBA {
	switch s {
	case model.StateError:
		return th.Red
	case model.StatePendingApproval, model.StateAwaitingInput:
		return th.Yellow
	case model.StateWorking, model.StateConnecting:
		return th.Blue
	case model.StatePlanReady:
		return th.Purple
	}
	return th.Green
}

// rowBase is a workspace row's opaque background: getWorkspaceBackground-
// ClassName's state tint, the active row's primary/12 on top, or the hover
// fill for rows with no activity.
func rowBase(th *theme.Theme, a *model.Activity, active, hovered bool) color.NRGBA {
	base := th.Sidebar
	if a != nil {
		switch a.State {
		case model.StateError:
			base = theme.Mix(base, th.Red, 0.03)
		case model.StatePendingApproval, model.StateAwaitingInput:
			base = theme.Mix(base, th.Yellow, 0.03)
		case model.StatePlanReady:
			base = theme.Mix(base, th.Purple, 0.15)
		}
	}
	switch {
	case active:
		base = theme.Mix(base, th.Primary, 0.12)
	case hovered:
		// A state's tint shows through the hover fill, faintly.
		base = theme.Mix(base, th.SurfaceSecondary, 0.85)
		if a == nil {
			base = th.SurfaceSecondary
		}
	}
	return base
}

// relTime ports aide's formatRelativeTime.
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	sec := int(now.Sub(t) / time.Second)
	switch {
	case sec < 5:
		return "just now"
	case sec < 60:
		return fmt.Sprintf("%ds ago", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm ago", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%dh ago", sec/3600)
	}
	return fmt.Sprintf("%dd ago", sec/86400)
}
