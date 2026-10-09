package sidebar

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func (s *Sidebar) projectHeader(gtx layout.Context, v *view, p model.Project) layout.Dimensions {
	th := v.th
	ps := s.project(p.ID)
	w, h := gtx.Constraints.Max.X, gtx.Dp(40) // --pane-header-h
	hovered := ps.toggle.Hovered() || ps.add.Hovered() || ps.more.Hovered()
	cursor := s.cursorOn('g', p.ID)
	switch {
	case cursor:
		paint.FillShape(gtx.Ops, cursorFill(th, th.Sidebar, false), clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
		cursorRing(gtx, th, image.Rect(0, 0, w, h), gtx.Dp(8))
	case hovered:
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
	}
	attention := 0 // tabs with something the user has not seen
	for _, ws := range v.byProject[p.ID] {
		if v.unseen[ws.ID] != nil {
			attention++
		}
	}
	bg := th.Sidebar
	switch {
	case cursor:
		bg = cursorFill(th, th.Sidebar, false)
	case hovered:
		bg = th.SurfaceSecondary
	}
	nameCol := theme.Mix(bg, th.Fg, 0.8)
	if p.ID == v.activeProject {
		nameCol = th.Fg
	}
	btn := gtx.Dp(24)
	gap := gtx.Dp(4)
	px := gtx.Dp(8)
	// The name takes the whole row; on hover "+" and "…" sit over its end
	// (px-2, [+ 24] gap-1 [… 24]) behind a fade.
	triggerW := w - 2*px
	bx := w - px - 2*btn - gap // where the buttons start
	showBtns := hovered || s.appearance == p.ID || s.groupMenu == p.ID

	ctx := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &ps.ctx) // its handlers nest inside, so right-clicks reach it
	defer ctx.Pop()

	tg := gtx
	tg.Constraints = layout.Exact(image.Pt(triggerW, h))
	off := op.Offset(image.Pt(px, 0)).Push(gtx.Ops)
	clickable(tg, &ps.toggle, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(triggerW-gtx.Dp(12), h))
		off := op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops)
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions {
				return chevron(gtx, &ps.chev, s.isExpanded(p.ID), gtx.Dp(12), th.Muted)
			}},
			{w: func(gtx layout.Context) layout.Dimensions {
				return drawIcon(gtx, projectIcon(p.Icon), gtx.Dp(14), th.ProjectColor(p.Color), 0)
			}},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if s.renamingGroup == p.ID {
					gtx.Constraints.Max.Y = gtx.Dp(24)
					return s.renameField(gtx, th)
				}
				return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), nameCol, p.Name)
			}},
		}
		if attention > 0 {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(16)
				paint.FillShape(gtx.Ops, theme.Mix(bg, th.Yellow, 0.14), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
				centered(gtx, image.Pt(d, d), func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Readable(th.Yellow, theme.Mix(bg, th.Yellow, 0.14)), fmt.Sprint(attention))
				})
				return layout.Dimensions{Size: image.Pt(d, d)}
			}})
		}
		hrow(gtx, h, gtx.Dp(8), items...)
		off.Pop()
		return layout.Dimensions{Size: image.Pt(triggerW, h)}
	})
	off.Pop()

	if showBtns {
		fadeOut(gtx, bg, bx-gtx.Dp(40), bx-gap, w, h, gtx.Dp(8))
	}
	// "+" and the "…" overflow trigger show on header hover, like aide's;
	// hidden, they still take the pointer, so hovering their spot shows them.
	off = op.Offset(image.Pt(bx, (h-btn)/2)).Push(gtx.Ops)
	headerButton(gtx, th, &ps.add, icPlus, btn, gtx.Dp(14), showBtns)
	off.Pop()
	mx := bx + btn + gap
	off = op.Offset(image.Pt(mx, (h-btn)/2)).Push(gtx.Ops)
	headerButton(gtx, th, &ps.more, icEllipsis, btn, gtx.Dp(16), showBtns)
	anchor := image.Rectangle{Min: s.rowAt.Add(image.Pt(mx, (h-btn)/2))}
	anchor.Max = anchor.Min.Add(image.Pt(btn, btn))
	if s.appearance == p.ID {
		m := op.Record(gtx.Ops)
		s.appearanceMenu(gtx, th, p, anchor)
		op.Defer(gtx.Ops, m.Stop())
	}
	if s.groupMenu == p.ID {
		m := op.Record(gtx.Ops)
		s.catcher(gtx)
		entries := []menuEntry{
			{c: &s.groupItem[0], icon: icPencil, text: "Rename group"},
			{c: &s.groupItem[1], icon: projectIcon("palette"), text: "Icon and color"},
		}
		if p.Kind == model.ProjectGit {
			entries = append(entries, menuEntry{c: &s.groupItem[3], icon: projectIcon("git-branch"), text: "New tab in a worktree"})
		}
		entries = append(entries, menuEntry{c: &s.groupItem[4], icon: icPlay, text: "New task…"})
		entries = append(entries, menuEntry{c: &s.groupItem[2], icon: projectIcon("layers"), text: "Ungroup", sep: true})
		s.menuList(gtx, th, anchor, entries)

		op.Defer(gtx.Ops, m.Stop())
	}
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// projectHeaderGhost draws group p's header for the lifted copy under the
// pointer and returns its height.
func (s *Sidebar) projectHeaderGhost(gtx layout.Context, v *view, p model.Project) int {
	th := v.th
	h := gtx.Dp(40)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X-gtx.Dp(28), h))
	o := op.Offset(image.Pt(gtx.Dp(14), 0)).Push(gtx.Ops)
	hrow(gtx, h, gtx.Dp(8),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return chevron(gtx, &s.project(p.ID).chev, s.isExpanded(p.ID), gtx.Dp(12), th.Muted)
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, projectIcon(p.Icon), gtx.Dp(14), th.ProjectColor(p.Color), 0)
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, p.Name)
		}},
	)
	o.Pop()
	return h
}

// headerButton is an icon button on a hovered group header, whose
// background is iconButton's hover color, so its own hover is a step
// lighter. Hidden, it is an empty hit area.
func headerButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, icon string, size, glyph int, shown bool) layout.Dimensions {
	gtx.Constraints = layout.Exact(image.Pt(size, size))
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if !shown {
			return layout.Dimensions{Size: image.Pt(size, size)}
		}
		col := th.Muted
		if c.Hovered() {
			paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.08), clip.UniformRRect(image.Rect(0, 0, size, size), gtx.Dp(6)).Op(gtx.Ops))
			col = th.Fg
		}
		return drawCentered(gtx, size, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icon, glyph, col, 0)
		})
	})
}

// fadeOut hides what is drawn under x0..w: a fade from clear to bg across
// x0..x1, solid bg after, clipped to the row's rounded rect of height h.
func fadeOut(gtx layout.Context, bg color.NRGBA, x0, x1, w, h, radius int) {
	defer clip.UniformRRect(image.Rect(0, 0, w, h), radius).Push(gtx.Ops).Pop()
	clear := bg
	clear.A = 0
	area := clip.Rect{Min: image.Pt(x0, 0), Max: image.Pt(x1, h)}.Push(gtx.Ops)
	paint.LinearGradientOp{Stop1: f32.Pt(float32(x0), 0), Color1: clear, Stop2: f32.Pt(float32(x1), 0), Color2: bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	area.Pop()
	paint.FillShape(gtx.Ops, bg, clip.Rect{Min: image.Pt(x1, 0), Max: image.Pt(w, h)}.Op())
}
