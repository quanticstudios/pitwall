package sidebar

import (
	"cmp"
	"image"
	"image/color"
	"slices"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The keyboard cursor: while the window gives the sidebar the keyboard
// (focus_sidebar), a focus ring marks one row, a group header, a tab or an
// agent's sub-row, and the arrow keys move it through the rows as drawn,
// the way a file tree works.

// MenuKey is the key on a PC keyboard that opens a context menu. Gio has
// no name for it; the vendored Gio reports it as this.
const MenuKey key.Name = "Menu"

// cursorRingW is the cursor's ring, heavier than the shown tab's 1dp
// border so the two never look alike.
const cursorRingW unit.Dp = 2

// cursorFill is the fill of the row the cursor is on: SelectedBg, or on
// the shown tab, whose fill is already a primary tint, a step deeper.
func cursorFill(th *theme.Theme, base color.NRGBA, active bool) color.NRGBA {
	if active {
		return theme.Mix(base, th.Primary, 0.14)
	}
	return th.SelectedBg
}

// cursorRing draws the cursor's ring in Primary just inside rect.
func cursorRing(gtx layout.Context, th *theme.Theme, rect image.Rectangle, r int) {
	w := gtx.Dp(cursorRingW)
	path := clip.UniformRRect(rect.Inset(w/2), max(r-w/2, 0)).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, th.Primary, clip.Stroke{Path: path, Width: float32(w)}.Op())
}

// cursor is the keyboard's place in the sidebar and in an open menu.
type cursor struct {
	on         bool
	kind       byte   // 'g' group header, 's' tab, 'a' agent sub-row
	id, parent string // the row's group, tab or pane id; a sub-row's tab
	at         int    // its index in the rows, to land near it when it goes
	moved      bool   // scroll it into view on the next frame
	page       int    // how many rows a page of the list holds, as last drawn

	// The open menu: the entry the keys are on, the enabled entries as last
	// drawn, the Move to group entry and its flyout's entries.
	item  *widget.Clickable
	items []*widget.Clickable
	move  *widget.Clickable
	fly   []*widget.Clickable
	inFly bool
}

// Focus gives the sidebar the keyboard: the cursor starts on the agent
// sub-row of active that asks for an answer, else on active's row, else on
// the header of its collapsed group.
func (s *Sidebar) Focus(st *model.State, session, active string) {
	v, rows := s.cursorView(st, session, active)
	s.cur = cursor{on: true, kind: 's', id: active, moved: true, page: s.cur.page}
	for _, ap := range s.shownSubs(v, active) {
		if a := ap.Activity; a != nil && (a.State == model.StatePendingApproval || a.State == model.StateAwaitingInput) {
			s.cur.kind, s.cur.id, s.cur.parent = 'a', ap.Pane.ID, active
			break
		}
	}
	if s.find(rows) < 0 {
		s.cur.kind, s.cur.id = 'g', v.groupOf(active)
	}
	s.settle(v, rows)
}

// Blur takes the keyboard back, closing any menu.
func (s *Sidebar) Blur() {
	if s.cur.on && s.menuOpen() {
		s.closeMenus()
	}
	s.cur = cursor{}
}

// Focused reports whether the sidebar has the keyboard.
func (s *Sidebar) Focused() bool { return s.cur.on }

// Asking is the permission prompt of the cursor's row, nil when it shows
// none: allow_prompt and deny_prompt answer it while the sidebar has the
// keyboard.
func (s *Sidebar) Asking(st *model.State, session, active string) *model.Activity {
	if !s.cur.on {
		return nil
	}
	v, rows := s.cursorView(st, session, active)
	if s.find(rows) < 0 {
		return nil
	}
	a := v.activity[s.cur.id]
	if s.cur.kind == 'a' {
		ap, _ := v.agentPane(s.cur.parent, s.cur.id)
		a = ap.Activity
	}
	if s.cur.kind == 'g' || a == nil || !remote.Answerable(*a) {
		return nil
	}
	return a
}

// Key runs one key with the sidebar focused. It returns the events the
// window acts on, whether focus goes back to the pane, and whether the key
// was the sidebar's; one that was not is the window's to run or drop.
func (s *Sidebar) Key(st *model.State, session, active string, e key.Event) (evs []Event, back, used bool) {
	shift := e.Modifiers == key.ModShift && e.Name == key.NameF10
	if !s.cur.on || e.Modifiers != 0 && !shift {
		return nil, false, false
	}
	if e.State != key.Press {
		return nil, false, true
	}
	saved := s.events
	s.events = nil
	defer func() { s.events = saved }()
	v, rows := s.cursorView(st, session, active)
	if s.menuOpen() {
		s.menuKeys(e)
		return nil, false, true
	}
	s.cur.item, s.cur.inFly = nil, false
	i := s.settle(v, rows)
	if i < 0 {
		return nil, false, true
	}
	r := rows[i]
	tab := r.id // the tab F2, Delete and the menu act on
	if r.kind == 'a' {
		tab = r.parent
	}
	page := cmp.Or(s.cur.page, 10) // before the first frame
	switch e.Name {
	case key.NameUpArrow, "K":
		s.moveTo(rows, max(i-1, 0))
	case key.NameDownArrow, "J":
		s.moveTo(rows, i+1)
	case key.NameHome:
		s.moveTo(rows, 0)
	case key.NameEnd:
		s.moveTo(rows, len(rows)-1)
	case key.NamePageUp:
		s.moveTo(rows, max(i-page, 0))
	case key.NamePageDown:
		s.moveTo(rows, i+page)
	case key.NameRightArrow, "L":
		switch {
		case r.kind == 'g' && !s.isExpanded(r.id):
			s.setExpanded(r.id, true)
		case r.kind == 's' && v.agents[r.id] != nil && s.folded[r.id]:
			delete(s.folded, r.id)
		case i+1 < len(rows) && s.childOf(rows[i+1], r):
			s.moveTo(rows, i+1)
		}
	case key.NameLeftArrow, "H":
		switch {
		case r.kind == 'g' && s.isExpanded(r.id):
			s.setExpanded(r.id, false)
		case r.kind == 's' && len(s.shownSubs(v, r.id)) > 0:
			if s.folded == nil {
				s.folded = map[string]bool{}
			}
			s.folded[r.id] = true
		case r.kind == 'a':
			s.moveTo(rows, slices.IndexFunc(rows, func(e elem) bool { return e.kind == 's' && e.id == r.parent }))
		case r.kind == 's' && r.group != "":
			s.moveTo(rows, slices.IndexFunc(rows, func(e elem) bool { return e.kind == 'g' && e.id == r.group }))
		}
	case key.NameReturn, key.NameEnter, key.NameSpace:
		switch r.kind {
		case 'g':
			s.setExpanded(r.id, !s.isExpanded(r.id))
		case 's':
			s.click(v, r.id, 0)
			back = e.Name != key.NameSpace
		case 'a':
			s.clickPane(v, r.parent, r.id, 0)
			back = e.Name != key.NameSpace
		}
	case key.NameF10, MenuKey:
		if e.Name == key.NameF10 && !shift {
			break
		}
		if r.kind == 'g' {
			s.toggleGroupMenu(r.id)
		} else {
			s.toggleMenu(tab)
		}
	case key.NameF2:
		if r.kind == 'g' {
			for _, p := range v.st.Projects {
				if p.ID == r.id {
					s.startRename("", p.ID, p.Name)
				}
			}
			break
		}
		for _, ws := range v.st.Workspaces {
			if ws.ID == tab {
				s.startRename(tab, "", Title(ws))
			}
		}
	case key.NameDeleteForward:
		if r.kind != 'g' {
			s.events = append(s.events, DeleteWorkspace{WorkspaceID: tab})
		}
	case key.NameEscape:
		back = true
	}
	return s.events, back, true
}

// menuKeys moves through the open menu: Up and Down, Enter or Space to
// choose, Right into the Move to group flyout, Left or Esc out of it, Esc
// to close the menu.
func (s *Sidebar) menuKeys(e key.Event) {
	c := &s.cur
	list := c.items
	if c.inFly && s.moveOpen {
		list = c.fly
	}
	i := slices.Index(list, c.item)
	step := func(j int) {
		if len(list) > 0 {
			c.item = list[min(max(j, 0), len(list)-1)]
		}
	}
	out := func() { // out of the flyout, onto Move to group
		s.moveOpen, c.inFly, c.item = false, false, c.move
	}
	switch e.Name {
	case key.NameUpArrow, "K":
		step(i - 1)
	case key.NameDownArrow, "J":
		step(i + 1)
	case key.NameHome:
		step(0)
	case key.NameEnd:
		step(len(list) - 1)
	case key.NameRightArrow, "L", key.NameReturn, key.NameEnter, key.NameSpace:
		switch {
		case c.item == nil:
		case c.item == c.move && !c.inFly:
			s.moveOpen, s.subAt, c.inFly, c.item = true, s.now, true, nil
		case e.Name != key.NameRightArrow && e.Name != "L":
			c.item.Click()
		}
	case key.NameLeftArrow, "H":
		if c.inFly {
			out()
		}
	case key.NameEscape:
		if c.inFly {
			out()
			break
		}
		s.closeMenus()
		c.item, c.items = nil, nil
	}
}

// noteMenu keeps the enabled entries of the menu being drawn for the
// keys, and puts the cursor on the first one when it is on none.
func (s *Sidebar) noteMenu(entries []menuEntry) {
	if !s.cur.on {
		return
	}
	s.cur.items, s.cur.move = s.cur.items[:0], nil
	for _, e := range entries {
		if !e.off {
			s.cur.items = append(s.cur.items, e.c)
		}
		if e.sub {
			s.cur.move = e.c
		}
	}
	if !s.cur.inFly && !slices.Contains(s.cur.items, s.cur.item) && len(s.cur.items) > 0 {
		s.cur.item = s.cur.items[0]
	}
}

// noteFlyout is noteMenu for the Move to group flyout.
func (s *Sidebar) noteFlyout(cs []*widget.Clickable) {
	if !s.cur.on {
		return
	}
	s.cur.fly = cs
	if s.cur.inFly && !slices.Contains(cs, s.cur.item) && len(cs) > 0 {
		s.cur.item = cs[0]
	}
}

// keyed reports whether the keyboard is on menu entry c.
func (s *Sidebar) keyed(c *widget.Clickable) bool { return s.cur.on && c == s.cur.item }

// cursorOn reports whether the cursor is on the row kind id.
func (s *Sidebar) cursorOn(kind byte, id string) bool {
	return s.cur.on && s.cur.kind == kind && s.cur.id == id
}

// cursorFrame keeps the cursor on a row that is still drawn, forgets a
// menu that closed, and scrolls the list to show the cursor once it moved.
// elems are this frame's rows laid out, total their height.
func (s *Sidebar) cursorFrame(gtx layout.Context, v *view, elems []elem, total int) {
	if !s.cur.on {
		return
	}
	if !s.menuOpen() {
		s.cur.item, s.cur.items, s.cur.inFly = nil, nil, false
	}
	s.settle(v, s.rowsOf(v))
	h := gtx.Constraints.Max.Y
	if len(elems) > 0 && total > 0 {
		s.cur.page = max(1, h*len(elems)/total-1)
	}
	if !s.cur.moved {
		return
	}
	s.cur.moved = false
	i := slices.IndexFunc(elems, func(e elem) bool { return e.kind == s.cur.kind && e.id == s.cur.id })
	if i < 0 {
		return
	}
	top, bot, pad := elems[i].top, elems[i].bot, gtx.Dp(4)
	if elems[i].kind == 'g' {
		top = elems[i].head
	}
	off := s.list.Position.Offset
	if top-pad < off {
		off = top - pad
	} else if bot+pad > off+h {
		off = bot + pad - h
	}
	s.list.Position.First, s.list.Position.Offset = 0, max(0, min(off, total-h))
}

// cursorView is st's view for the keys, with the rows the cursor moves
// through.
func (s *Sidebar) cursorView(st *model.State, session, active string) (*view, []elem) {
	view := st.View(session)
	v := newView(layout.Context{}, nil, &view, session, active)
	s.follow(v)
	return v, s.rowsOf(v)
}

// rowsOf is the rows the cursor moves through, in the order place draws
// them: group headers, tabs and the agent sub-rows under each tab.
func (s *Sidebar) rowsOf(v *view) []elem {
	var out []elem
	tab := func(id, g string) {
		out = append(out, elem{kind: 's', id: id, group: g})
		for _, ap := range s.shownSubs(v, id) {
			out = append(out, elem{kind: 'a', id: ap.Pane.ID, parent: id, group: g})
		}
	}
	for _, id := range v.top {
		if !v.groups[id] {
			tab(id, "")
			continue
		}
		out = append(out, elem{kind: 'g', id: id, group: id})
		if s.isExpanded(id) {
			for _, ws := range v.byProject[id] {
				tab(ws.ID, id)
			}
		}
	}
	return out
}

// find is the cursor's index in rows, or -1.
func (s *Sidebar) find(rows []elem) int {
	return slices.IndexFunc(rows, func(e elem) bool { return e.kind == s.cur.kind && e.id == s.cur.id })
}

// settle keeps the cursor on a row: one that went, folded or collapsed
// away hands over to its tab, else its group's header, else the row now
// where it was. It returns the cursor's index, -1 with no rows.
func (s *Sidebar) settle(v *view, rows []elem) int {
	if i := s.find(rows); i >= 0 {
		s.cur.at = i
		return i
	}
	if len(rows) == 0 {
		return -1
	}
	tab, i := s.cur.id, -1
	if s.cur.kind == 'a' {
		tab = s.cur.parent
		i = slices.IndexFunc(rows, func(e elem) bool { return e.kind == 's' && e.id == tab })
	}
	if g := v.groupOf(tab); i < 0 && g != "" && s.cur.kind != 'g' {
		i = slices.IndexFunc(rows, func(e elem) bool { return e.kind == 'g' && e.id == g })
	}
	if i < 0 {
		i = min(s.cur.at, len(rows)-1)
	}
	s.moveTo(rows, i)
	return s.cur.at
}

// moveTo puts the cursor on rows[i], or on the last row past the end; a
// negative i leaves it where it is.
func (s *Sidebar) moveTo(rows []elem, i int) {
	if i < 0 || len(rows) == 0 {
		return
	}
	i = min(i, len(rows)-1)
	r := rows[i]
	s.cur.kind, s.cur.id, s.cur.parent, s.cur.at, s.cur.moved = r.kind, r.id, r.parent, i, true
}

// childOf reports whether row c hangs under row p: a tab of group p, or a
// sub-row of tab p.
func (s *Sidebar) childOf(c, p elem) bool {
	return p.kind == 'g' && c.kind != 'g' && c.group == p.id || p.kind == 's' && c.kind == 'a' && c.parent == p.id
}

func (s *Sidebar) setExpanded(id string, on bool) {
	if s.expanded == nil {
		s.expanded = map[string]bool{}
	}
	s.expanded[id] = on
}
