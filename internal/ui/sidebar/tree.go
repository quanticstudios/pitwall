package sidebar

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rowHeight is a tab row's height: py-2, line 1 (13px * 1.5), gap-1,
// line 2 (11px * 1.5), py-2.
func rowHeight(gtx layout.Context) int {
	return gtx.Dp(8) + gtx.Dp(19.5) + gtx.Dp(4) + gtx.Dp(16.5) + gtx.Dp(8)
}

// place lays the tree out in content pixels, top-level items in order: a
// run of ungrouped tabs between pt-1 and pb-1, gap-0.5 apart; a group as
// its separator (unless first), 6dp, its header and, when expanded, its tabs
// laid out the same way. It returns the elements and the height.
func (s *Sidebar) place(gtx layout.Context, v *view) ([]elem, int) {
	var out []elem
	rowH := rowHeight(gtx)
	y := 0
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
	}
	endRun()
	return out, y + gtx.Dp(6) // pb-1.5
}

// tree draws the elements, each at its slide offset, the carried ones
// under the pointer instead. It reports whether an agent animation runs
// and whether rows or a drag are moving.
func (s *Sidebar) tree(gtx layout.Context, v *view) (layout.Dimensions, bool, bool) {
	elems, total := s.place(gtx, v)
	s.elems = elems
	s.cardAt = ""
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
				o := op.Offset(image.Pt(0, y+e.head-e.top)).Push(gtx.Ops)
				for _, p := range v.st.Projects {
					if p.ID == e.id {
						s.projectHeader(gtx, v, p)
					}
				}
				o.Pop()
				continue
			}
			o := op.Offset(image.Pt(e.x, y)).Push(gtx.Ops)
			rg := gtx
			rg.Constraints.Max.X = w - e.x
			_, a := s.workspaceRow(rg, v, byID[e.id], false, digit)
			o.Pop()
			if e.id == s.hover.shown {
				s.cardAt, s.cardY = e.id, y
			}
			animating = animating || a
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
