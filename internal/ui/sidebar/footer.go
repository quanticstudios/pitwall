package sidebar

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// footer: the plan limits' meter when it shows, then Open folder as
// group, the update button when there is one, detached tabs, comments
// (disabled), settings.
func (s *Sidebar) footer(gtx layout.Context, v *view) layout.Dimensions {
	th := v.th
	w := gtx.Constraints.Max.X
	px, btn := gtx.Dp(8), gtx.Dp(28)
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: image.Pt(w, 1)}.Op())
	top := 0
	if len(s.Limits) > 0 {
		top = gtx.Dp(20)
		off := op.Offset(image.Pt(px+gtx.Dp(8), 1+px)).Push(gtx.Ops)
		mg := gtx
		mg.Constraints.Max.X = w - 2*px - gtx.Dp(16)
		s.limitMeter(mg, th, top)
		off.Pop()
	}
	if s.Hints != nil {
		off := op.Offset(image.Pt(px+gtx.Dp(8), 1+px+top)).Push(gtx.Ops)
		hg := gtx
		hg.Constraints = layout.Constraints{Max: image.Pt(w-2*px-gtx.Dp(16), gtx.Constraints.Max.Y)}
		top += s.Hints(hg).Size.Y + gtx.Dp(4)
		off.Pop()
	}
	defer op.Offset(image.Pt(0, top)).Push(gtx.Ops).Pop()
	h := 1 + 2*px + btn + top
	gap := gtx.Dp(4)
	addW := w - 2*px - 3*(btn+gap)
	x := px + addW + gap
	off := op.Offset(image.Pt(px, 1+px)).Push(gtx.Ops)
	if s.Update != "" {
		// The update button takes the label's room; the folder keeps its icon.
		s.updateButton(gtx, th, addW, btn)
		iconButton(gtx, th, &s.addProject, icFolderKanb, btn, gtx.Dp(14), true)
	} else {
		s.addButton(gtx, th, addW, btn)
	}
	off.Pop()

	for i, b := range []struct {
		c    *widget.Clickable
		icon string
	}{{&s.detached, icDetach}, {&s.comments, icMessage}, {&s.settings, icSettings}} {
		off := op.Offset(image.Pt(x+i*(btn+gap), 1+px)).Push(gtx.Ops)
		if b.c == &s.comments {
			// aide's comments button is disabled at 50% opacity.
			drawCentered(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				return drawIcon(gtx, icMessage, gtx.Dp(14), theme.Mix(th.Sidebar, th.Muted, 0.5), 0)
			})
		} else {
			iconButton(gtx, th, b.c, b.icon, btn, gtx.Dp(14), true)
		}
		if b.c == &s.detached && s.detachedOpen {
			m := op.Record(gtx.Ops)
			at := image.Pt(x+i*(btn+gap), s.height-h+top+1+px)
			s.detachedMenu(gtx, v, image.Rectangle{Min: at, Max: at.Add(image.Pt(btn, btn))})
			op.Defer(gtx.Ops, m.Stop())
		}
		off.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// addButton is "Open folder as group", addW wide.
func (s *Sidebar) addButton(gtx layout.Context, th *theme.Theme, addW, btn int) {
	ag := gtx
	ag.Constraints = layout.Exact(image.Pt(addW, btn))
	clickable(ag, &s.addProject, func(gtx layout.Context) layout.Dimensions {
		col := th.Muted
		if s.addProject.Hovered() {
			col = th.Fg
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, addW, btn), gtx.Dp(8)).Op(gtx.Ops))
		}
		gtx.Constraints = layout.Exact(image.Pt(addW-gtx.Dp(16), btn))
		defer op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops).Pop()
		hrow(gtx, btn, gtx.Dp(8),
			item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icFolderKanb, gtx.Dp(14), col, 0) }},
			item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, "Open folder as group")
			}},
		)
		return layout.Dimensions{Size: image.Pt(addW, btn)}
	})
}

// updateButton draws the update button, primary text on a primary tint,
// ending at x, h tall.
func (s *Sidebar) updateButton(gtx layout.Context, th *theme.Theme, x, h int) {
	m := op.Record(gtx.Ops)
	lg := gtx
	lg.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(140), h)}
	d := label(lg, th, medium(th.UIFont), th.Sp(theme.Small), th.Primary, s.Update)
	text := m.Stop()
	pad, bh := gtx.Dp(10), gtx.Dp(22)
	size := image.Pt(d.Size.X+2*pad, h)
	defer op.Offset(image.Pt(x-size.X, 0)).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(size)
	clickable(gtx, &s.upgrade, func(gtx layout.Context) layout.Dimensions {
		tint := float32(0.14)
		if s.upgrade.Hovered() {
			tint = 0.24
		}
		top := (h - bh) / 2
		paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Primary, tint), clip.UniformRRect(image.Rect(0, top, size.X, top+bh), gtx.Dp(6)).Op(gtx.Ops))
		defer op.Offset(image.Pt(pad, (h-d.Size.Y)/2)).Push(gtx.Ops).Pop()
		text.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
}

// detachedMenu lists the detached tabs above anchor, its trigger's rect in
// the sidebar, each with its agent state, Attach, and Kill behind a second
// click.
func (s *Sidebar) detachedMenu(gtx layout.Context, v *view, anchor image.Rectangle) {
	th := v.th
	s.catcher(gtx)
	var detached []model.Workspace
	for _, ws := range v.st.Workspaces {
		if ws.Detached {
			detached = append(detached, ws)
		}
	}
	acts := map[string][]model.Activity{}
	for _, a := range v.st.Activities {
		acts[a.WorkspaceID] = append(acts[a.WorkspaceID], a)
	}
	w, p, rowH, headH := gtx.Dp(300), gtx.Dp(4), gtx.Dp(44), gtx.Dp(30)
	n := max(len(detached), 1)
	size := image.Pt(w, 2*p+headH+n*rowH)
	at := kit.Place(anchor, size, s.bounds(), kit.Above, gtx.Dp(8), gtx.Dp(8)).Sub(anchor.Min)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	defer s.popIn(gtx, s.menuAt, size)()
	floatingSurface(gtx, th, size)

	s.blockClicks(gtx, size)
	inner := image.Pt(w-2*p-gtx.Dp(16), headH)
	off := op.Offset(image.Pt(p+gtx.Dp(8), p)).Push(gtx.Ops)
	hg := gtx
	hg.Constraints = layout.Exact(inner)
	hrow(hg, headH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), th.Muted, "Detached tabs")
	}})
	off.Pop()
	top := p + headH
	if len(detached) == 0 {
		off := op.Offset(image.Pt(p+gtx.Dp(8), top)).Push(gtx.Ops)
		gtx.Constraints = layout.Exact(image.Pt(inner.X, rowH))
		hrow(gtx, rowH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Detach keeps a tab running out of the list")
		}})
		off.Pop()
		return
	}
	for i, ws := range detached {
		kill := s.killBtn[ws.ID]
		if kill == nil {
			kill = &widget.Clickable{}
			s.killBtn[ws.ID] = kill
		}
		at := s.attachBtn[ws.ID]
		if at == nil {
			at = &widget.Clickable{}
			s.attachBtn[ws.ID] = at
		}
		a := model.Aggregate(acts[ws.ID])
		state, stateCol := "idle", th.Muted
		if a != nil {
			state, stateCol = PillText(*a, v.st.Decide.Provider), StateColor(th, a.State)
		}
		off := op.Offset(image.Pt(p+gtx.Dp(8), top+i*rowH)).Push(gtx.Ops)
		gtx := gtx
		gtx.Constraints = layout.Exact(image.Pt(w-2*p-gtx.Dp(12), rowH))
		items := []item{
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, Title(ws))
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, th.UIFont, th.Sp(theme.Caption), stateCol, state)
					}),
				)
			}},
			{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return kit.Button(gtx, th, at, kit.Ghost, kit.Small, "Attach")
			}},
		}
		if s.killArmed == ws.ID {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				return kit.Button(gtx, th, kill, kit.Danger, kit.Small, "Kill")
			}})
		} else {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				return iconButton(gtx, th, kill, icPower, gtx.Dp(28), gtx.Dp(14), true)
			}})
		}
		hrow(gtx, rowH, gtx.Dp(4), items...)
		off.Pop()
	}
}
