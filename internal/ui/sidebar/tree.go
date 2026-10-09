package sidebar

import (
	"fmt"
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rowLines are a tab row's line heights, 1.5 times its body and caption
// text, so the row grows with [font] ui_size.
func rowLines(gtx layout.Context, th *theme.Theme) (l1, l2 int) {
	return gtx.Sp(th.Sp(theme.Body) * 1.5), gtx.Sp(th.Sp(theme.Caption) * 1.5)
}

// rowHeight is a tab row's height: py-2, line 1, gap-1, line 2, py-2.
func rowHeight(gtx layout.Context, th *theme.Theme) int {
	l1, l2 := rowLines(gtx, th)
	return gtx.Dp(8) + l1 + gtx.Dp(4) + l2 + gtx.Dp(8)
}

// place lays the tree out in content pixels, top-level items in order: a
// run of ungrouped tabs between pt-1 and pb-1, gap-0.5 apart; a group as
// its separator (unless first), 6dp, its header and, when expanded, its tabs
// laid out the same way. It returns the elements and the height.
func (s *Sidebar) place(gtx layout.Context, v *view) ([]elem, int) {
	var out []elem
	rowH := rowHeight(gtx, v.th)
	y := 0
	s.queue = s.queue[:0]
	run := false // inside a run of tab rows
	row := func(id, g string) {
		if run {
			y += gtx.Dp(2)
		} else {
			y += gtx.Dp(4)
		}
		x := 0
		if g != "" {
			x = gtx.Dp(groupIndent)
		}
		out = append(out, elem{kind: 's', id: id, group: g, x: x, top: y, bot: y + rowH})
		y, run = y+rowH, true
		// Its agents' sub-rows hang under it, 2dp of air after the last.
		subs := s.shownSubs(v, id)
		for _, ap := range subs {
			h := subHeight(gtx, v.th, s.subAnswering(ap))
			out = append(out, elem{kind: 'a', id: ap.Pane.ID, parent: id, group: g, x: x, top: y, bot: y + h})
			y += h
		}
		if len(subs) > 0 {
			y += gtx.Dp(2)
		}
	}
	endRun := func() {
		if run {
			y, run = y+gtx.Dp(4), false
		}
	}
	for i, id := range v.top {
		if !v.groups[id] {
			row(id, "")
			continue
		}
		endRun()
		top := y
		if i > 0 {
			y += gtx.Dp(6) + 1 // mt-1.5 border-t
		}
		y += gtx.Dp(6) // pt-1.5, the first group's too
		head := y
		y += gtx.Dp(40)
		out = append(out, elem{kind: 'g', id: id, group: id, top: top, bot: y, head: head})
		if s.isExpanded(id) {
			for _, ws := range v.byProject[id] {
				row(ws.ID, id)
			}
			if len(v.byProject[id]) == 0 {
				y += gtx.Dp(8) // an empty group keeps its pt-1 pb-1
			}
			endRun()
		}
		y = s.placeQueue(gtx, v, id, len(out)-1, y)
	}
	endRun()
	y = s.placeQueue(gtx, v, "", len(out)-1, y)
	return out, y + gtx.Dp(6) // pb-1.5
}

// noteExpands starts the entrance of a group's rows when it opens, after
// the first frame.
func (s *Sidebar) noteExpands(now time.Time, v *view) {
	first := s.wasExpanded == nil
	if first {
		s.wasExpanded, s.expandAt = map[string]bool{}, map[string]time.Time{}
	}
	for _, p := range v.st.Projects {
		on := s.isExpanded(p.ID)
		if on && !s.wasExpanded[p.ID] && !first {
			s.expandAt[p.ID] = now
		}
		s.wasExpanded[p.ID] = on
	}
}

// tree draws the elements, each at its slide offset, the carried ones
// under the pointer instead. It reports whether an agent animation runs
// and whether rows or a drag are moving.
func (s *Sidebar) tree(gtx layout.Context, v *view) (layout.Dimensions, bool, bool) {
	elems, total := s.place(gtx, v)
	s.elems = elems
	s.noteExpands(gtx.Now, v)
	s.cardAt = ""
	s.cursorFrame(gtx, v, elems, total)
	offs, moving := s.animate(gtx, elems, total)
	animating := false
	byID := map[string]model.Workspace{}
	for _, ws := range v.st.Workspaces {
		byID[ws.ID] = ws
	}
	d := s.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		w := gtx.Constraints.Max.X
		n := 0 // rows so far, for their digits
		for i, e := range elems {
			digit := ""
			if e.kind == 's' {
				if n < len(s.Numbers) && s.Numbers[n] {
					digit = fmt.Sprint(n + 1)
				}
				n++
			}
			if s.drag.active && s.carried(e) {
				continue
			}
			y := e.top + int(offs[i]+0.5)
			if e.kind == 'g' {
				if e.head > e.top {
					sy := y + gtx.Dp(6)
					paint.FillShape(gtx.Ops, theme.Mix(v.th.Sidebar, v.th.Border, 0.6), clip.Rect{Min: image.Pt(0, sy), Max: image.Pt(w, sy+1)}.Op())
				}
				s.rowAt = image.Pt(gtx.Dp(listPad), gtx.Dp(56)+y+e.head-e.top-s.list.Position.Offset)
				o := op.Offset(image.Pt(0, y+e.head-e.top)).Push(gtx.Ops)
				for _, p := range v.st.Projects {
					if p.ID == e.id {
						s.projectHeader(gtx, v, p)
					}
				}
				o.Pop()
				continue
			}
			s.rowAt = image.Pt(gtx.Dp(listPad)+e.x, gtx.Dp(56)+y-s.list.Position.Offset)
			// A group's rows fade in as it opens, from 4dp up.
			t := float32(1)
			if at, ok := s.expandAt[e.group]; ok {
				t = anim.At(gtx, at, anim.Expand)
			}
			o := op.Offset(image.Pt(e.x, y-int(float32(gtx.Dp(4))*(1-t)))).Push(gtx.Ops)
			fade := paint.PushOpacity(gtx.Ops, t)
			rg := gtx
			rg.Constraints.Max.X = w - e.x
			var a bool
			if e.kind == 'a' {
				last := i+1 == len(elems) || elems[i+1].parent != e.parent
				subGuide(rg, v.th, e.bot-e.top, last)
				if ap, ok := v.agentPane(e.parent, e.id); ok {
					_, a = s.subRowAt(rg, v, e.parent, ap, false)
				}
			} else {
				_, a = s.workspaceRow(rg, v, byID[e.id], false, digit)
			}
			fade.Pop()
			o.Pop()
			if e.kind == 's' && e.id == s.hover.shown {
				s.cardAt, s.cardY = e.id, y
			}
			animating = animating || a
		}
		for _, b := range s.queue {
			if s.drag.active && s.drag.kind == 'g' && s.drag.id == b.group {
				continue // the lifted group is drawn without it
			}
			y := b.top
			if b.after >= 0 {
				y += int(offs[b.after] + 0.5)
			}
			o := op.Offset(image.Pt(b.x, y)).Push(gtx.Ops)
			s.drawQueue(gtx, v.th, b)
			o.Pop()
		}
		return layout.Dimensions{Size: image.Pt(w, total)}
	})
	if s.list.Position.Offset != s.scroll {
		s.hover.dismiss() // the rows moved under the pointer
	}
	s.cardY += gtx.Dp(56) - s.list.Position.Offset // under the header
	s.scroll = s.list.Position.Offset
	if s.dragOverlay(gtx, v, d.Size) {
		moving = true
	}
	return d, animating, moving
}
