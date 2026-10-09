package app

import (
	"image"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// sidebarKeys is focus_sidebar's mode, while the sidebar has the
// keyboard: its cursor takes the keys (sidebar.Sidebar.Key) and none
// reaches a pane.
type sidebarKeys struct {
	lent  bool // the mode showed the hidden sidebar; leaving hides it again
	under bool // the settings page or the review view showed when it began
}

// toggleSidebarKeys runs focus_sidebar: the keyboard moves into the
// sidebar, which shows while it is there, or back to the pane.
func (u *ui) toggleSidebarKeys(st *model.State) {
	if u.sidebar.Focused() {
		u.leaveSidebar()
		return
	}
	if id := u.find.pane; id != "" {
		u.closeFind(findPane(st, id) != nil)
	}
	u.sideKeys = sidebarKeys{lent: u.nav.sidebarHidden, under: u.settings.Shown() || u.review.on}
	u.nav.sidebarHidden = false
	u.sidebar.Focus(st, u.nav.session, u.nav.workspace)
}

// leaveSidebar gives the keyboard back to the shown tab's pane.
func (u *ui) leaveSidebar() {
	if !u.sidebar.Focused() {
		return
	}
	u.sidebar.Blur()
	if u.sideKeys.lent {
		u.nav.sidebarHidden = true
	}
	u.sideKeys = sidebarKeys{}
}

// keepSidebarKeys ends the mode once something else takes the keyboard: a
// dialog, the palette, a switcher, the find bar, tab or pane mode, or the
// settings page or review view opening. Hiding the sidebar ends it too.
func (u *ui) keepSidebarKeys() {
	if !u.sidebar.Focused() {
		return
	}
	page := u.settings.Shown() || u.review.on
	u.sideKeys.under = u.sideKeys.under && page
	if u.nav.sidebarHidden {
		u.sideKeys.lent = false
	}
	if u.nav.sidebarHidden || u.modal.kind != modalNone || u.sw.open || u.pal.open || u.find.pane != "" ||
		u.nav.tabMode || u.nav.paneMode || u.nav.switcherVisible() || page && !u.sideKeys.under {
		u.leaveSidebar()
	}
}

// sidebarKey runs e while the sidebar has the keyboard and reports whether
// that was all. allow_prompt and deny_prompt answer the cursor's row when
// it asks; other bound chords run as usual; any other key is the sidebar's
// and never reaches a pane.
func (u *ui) sidebarKey(st *model.State, e key.Event) bool {
	if !u.sidebar.Focused() || u.sidebar.Editing() {
		return false
	}
	act := u.nav.bind().Action(e)
	if act == "allow_prompt" || act == "deny_prompt" {
		a := u.sidebar.Asking(st, u.nav.session, u.nav.workspace)
		if a != nil && e.State == key.Press {
			u.send(proto.Answer{Pane: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: act == "allow_prompt"})
		}
		return a != nil
	}
	evs, back, used := u.sidebar.Key(st, u.nav.session, u.nav.workspace, e)
	for _, ev := range evs {
		u.sidebarEvent(st, ev)
	}
	if back {
		u.leaveSidebar()
		u.nav.swallow = e.Name // its release must not reach the pane
	}
	return used || act == ""
}

// sidebarPointer handles a sidebar event from the pointer: a click that
// opens a tab or an agent's pane gives that pane the keyboard, as it does
// outside the mode.
func (u *ui) sidebarPointer(st *model.State, ev sidebar.Event) {
	if _, ok := ev.(sidebar.SelectWorkspace); ok {
		u.leaveSidebar()
	}
	u.sidebarEvent(st, ev)
}

// sidebarHints are the keys the sidebar's footer lists while it has the
// keyboard, nil otherwise.
func (u *ui) sidebarHints() gl.Widget {
	if !u.sidebar.Focused() {
		return nil
	}
	return func(gtx gl.Context) gl.Dimensions {
		h := gtx.Dp(24)
		u.drawHints(gtx, h, [][2]string{{"↑↓", "move"}, {"←→", "fold"}, {"Enter", "open"}})
		o := op.Offset(image.Pt(0, h)).Push(gtx.Ops)
		u.drawHints(gtx, h, [][2]string{{"Shift+F10", "menu"}, {"Esc", "back"}})
		o.Pop()
		return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 2*h)}
	}
}
