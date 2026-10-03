package sidebar

import (
	"fmt"
	"image"
	"reflect"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	pl "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// SelectTab shows a session's tab; NewTab opens one in the session;
// CloseTab closes one; RenameTab names one ("" goes back to its title).
type (
	SelectTab struct{ WorkspaceID, TabID string }
	NewTab    struct{ WorkspaceID string }
	CloseTab  struct{ WorkspaceID, TabID string }
	RenameTab struct{ WorkspaceID, TabID, Name string }
)

const icX = "M18 6 6 18M6 6l12 12"

type tabRowState struct {
	click, close widget.Clickable
	mid          int // tag for the middle-click close
}

// TabLabel is what a tab shows: its name, else its title, else "tab N".
func TabLabel(t model.Tab, i int) string {
	switch {
	case t.Name != "":
		return t.Name
	case t.Title != "":
		return t.Title
	}
	return fmt.Sprintf("tab %d", i+1)
}

// StartTabRename opens the inline editor on a tab's row.
func (s *Sidebar) StartTabRename(ws, tab, name string) {
	s.startRename("", "", name)
	s.renamingTab, s.renamingTabWS = tab, ws
}

// shownTab is the tab the window draws for ws: its ActiveTab, else its first.
func shownTab(ws model.Workspace) string {
	for _, t := range ws.Tabs {
		if t.ID == ws.ActiveTab {
			return t.ID
		}
	}
	if len(ws.Tabs) > 0 {
		return ws.Tabs[0].ID
	}
	return ""
}

// showsTabs reports whether ws lists its tabs: with two or more, or while
// one of them is being renamed.
func (s *Sidebar) showsTabs(ws model.Workspace) bool {
	return len(ws.Tabs) > 1 || len(ws.Tabs) > 0 && s.renamingTabWS == ws.ID && s.renamingTab != ""
}

// tabActivity is the activity of t's panes that matters most, or nil.
func tabActivity(st *model.State, t model.Tab) *model.Activity {
	var acts []model.Activity
	if t.Layout != nil {
		for _, p := range pl.Panes(t.Layout) {
			for _, a := range st.Activities {
				if a.PaneID == p {
					acts = append(acts, a)
				}
			}
		}
	}
	return model.Aggregate(acts)
}

func (s *Sidebar) tabRow(id string) *tabRowState {
	if s.tabState == nil {
		s.tabState = map[string]*tabRowState{}
	}
	r := s.tabState[id]
	if r == nil {
		r = &tabRowState{}
		s.tabState[id] = r
	}
	return r
}

// updateTabs handles last frame's clicks on ws's tab rows and "+".
func (s *Sidebar) updateTabs(gtx layout.Context, ws model.Workspace) {
	for s.row(ws.ID).add.Clicked(gtx) {
		s.events = append(s.events, NewTab{WorkspaceID: ws.ID})
	}
	for i, t := range ws.Tabs {
		r := s.tabRow(t.ID)
		for {
			c, ok := r.click.Update(gtx)
			if !ok {
				break
			}
			if c.NumClicks >= 2 {
				s.StartTabRename(ws.ID, t.ID, TabLabel(t, i))
			} else {
				s.events = append(s.events, SelectTab{WorkspaceID: ws.ID, TabID: t.ID})
			}
		}
		for r.close.Clicked(gtx) {
			s.events = append(s.events, CloseTab{WorkspaceID: ws.ID, TabID: t.ID})
		}
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &r.mid, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonTertiary {
				s.events = append(s.events, CloseTab{WorkspaceID: ws.ID, TabID: t.ID})
			}
		}
	}
}

// drawTabs draws ws's tabs under its row, indented, and returns their
// height.
func (s *Sidebar) drawTabs(gtx layout.Context, v *view, ws model.Workspace) (int, bool) {
	th := v.th
	w := gtx.Constraints.Max.X
	h, rr, indent := gtx.Dp(28), gtx.Dp(6), gtx.Dp(24)
	shown := shownTab(ws)
	y, animating := 0, false
	for i, t := range ws.Tabs {
		y += gtx.Dp(2)
		r := s.tabRow(t.ID)
		a := tabActivity(v.st, t)
		animating = animating || model.Pulses(a)
		active := t.ID == shown
		hovered := r.click.Hovered() || r.close.Hovered()
		renaming := s.renamingTab == t.ID
		base := th.Sidebar
		switch {
		case active && ws.ID == v.active:
			base = theme.Mix(th.Sidebar, th.Primary, 0.12)
		case active:
			base = theme.Mix(th.Sidebar, th.SurfaceSecondary, 0.6)
		case hovered:
			base = th.SurfaceSecondary
		}
		off := op.Offset(image.Pt(indent, y)).Push(gtx.Ops)
		rect := image.Rect(0, 0, w-indent, h)
		if base != th.Sidebar {
			paint.FillShape(gtx.Ops, base, clip.UniformRRect(rect, rr).Op(gtx.Ops))
		}
		area := clip.Rect(rect).Push(gtx.Ops)
		event.Op(gtx.Ops, &r.mid)
		cg := gtx
		cg.Constraints = layout.Exact(rect.Size())
		clickable(cg, &r.click, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(rect.Dx()-gtx.Dp(8)-gtx.Dp(28), h))
			o := op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops)
			col := theme.Mix(base, th.Fg, 0.75)
			if active {
				col = th.Fg
			}
			hrow(gtx, h, gtx.Dp(8),
				item{w: func(gtx layout.Context) layout.Dimensions {
					if a == nil {
						ic := th.Muted
						if active {
							ic = th.Primary
						}
						return drawIcon(gtx, icTerminal, gtx.Dp(12), ic, 0)
					}
					return s.stateIcon(gtx, v, ws, a, active, base)
				}},
				item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
					if renaming {
						gtx.Constraints.Max.Y = gtx.Dp(22)
						return s.renameField(gtx, th)
					}
					return label(gtx, th, medium(th.UIFont), 12, col, TabLabel(t, i))
				}},
			)
			o.Pop()
			return layout.Dimensions{Size: rect.Size()}
		})
		area.Pop()
		btn := gtx.Dp(20)
		bo := op.Offset(image.Pt(rect.Dx()-gtx.Dp(4)-btn, (h-btn)/2)).Push(gtx.Ops)
		if hovered && !renaming {
			iconButton(gtx, th, &r.close, icX, btn, gtx.Dp(12), true)
		} else {
			clickable(gtx, &r.close, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(btn, btn)} })
		}
		bo.Pop()
		off.Pop()
		y += h
	}
	return y, animating
}

// displayName is a session row's title and the quiet secondary name under
// it: the session's Label (its most relevant tab's title) while the user
// has not named it, with the generated name second.
//
// workaround(until the engine track's Workspace.NameSet and Label merge):
// read by name through reflection so this builds before they exist.
func displayName(ws model.Workspace) (title, secondary string) {
	v := reflect.ValueOf(ws)
	l, set := v.FieldByName("Label"), v.FieldByName("NameSet")
	if l.IsValid() && set.IsValid() && !set.Bool() && l.String() != "" {
		return l.String(), ws.Name
	}
	return ws.Name, ""
}
