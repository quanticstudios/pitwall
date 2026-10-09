package sidebar

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"
)

// click applies a plain, Ctrl or Shift click on tab id.
func (s *Sidebar) click(v *view, id string, mods key.Modifiers) {
	switch {
	case mods.Contain(key.ModShortcut):
		if len(s.selected) == 0 && v.active != "" && v.active != id {
			s.selected[v.active] = true // Ctrl+click extends from the open tab
		}
		if s.selected[id] {
			delete(s.selected, id)
		} else {
			s.selected[id] = true
		}
		s.anchor = id
	case mods.Contain(key.ModShift):
		from := s.anchor
		if from == "" {
			from = v.active
		}
		order := s.order(v)
		i, j := slices.Index(order, from), slices.Index(order, id)
		if i < 0 {
			i = j
		}
		clear(s.selected)
		for _, w := range order[min(i, j) : max(i, j)+1] {
			s.selected[w] = true
		}
	default:
		clear(s.selected)
		s.anchor = id
		e := SelectWorkspace{WorkspaceID: id}
		if a := v.activity[id]; a != nil {
			e.PaneID = a.PaneID
		}
		s.events = append(s.events, e)
	}
}

// targets are the tabs a menu action on id applies to: the selection
// when id is part of it, else id alone.
func (s *Sidebar) targets(v *view, id string) []string {
	if !s.selected[id] || len(s.selected) < 2 {
		return []string{id}
	}
	var out []string
	for _, ws := range v.st.Workspaces {
		if s.selected[ws.ID] {
			out = append(out, ws.ID)
		}
	}
	return out
}

// Editing reports whether an inline rename holds key focus, so the window
// keeps its panes from taking it back.
func (s *Sidebar) Editing() bool {
	return s.renaming != "" || s.renamingGroup != ""
}

// StartRename opens the inline editor on tab ws's row, starting from name.
func (s *Sidebar) StartRename(ws, name string) { s.startRename(ws, "", name) }

func (s *Sidebar) startRename(ws, group, name string) {
	s.renaming, s.renamingGroup, s.focusEditor = ws, group, true
	s.menuWS, s.groupMenu = "", ""
	s.editor.SetText(name)
	s.editor.SetCaret(utf8.RuneCountInString(name), 0)
	s.renameFrom = name
}

func (s *Sidebar) cancelRename() {
	s.renaming, s.renamingGroup = "", ""
	s.focusEditor, s.selectAll = false, false
}

// update drains input from last frame's widgets before anything is drawn,
// so hover backgrounds and menus reflect this frame's state.
func (s *Sidebar) update(gtx layout.Context, v *view) {
	dropped := s.dragEvents(gtx, v) // its release is no click
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.dismiss, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			s.closeMenus()
		}
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &s.popover, Kinds: pointer.Press}); !ok {
			break
		}
	}
	// press reports a right click and a middle click on tag.
	press := func(tag event.Tag) (right, middle bool) {
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
			if !ok {
				return right, middle
			}
			if pe, ok := ev.(pointer.Event); ok {
				right = right || pe.Buttons == pointer.ButtonSecondary
				middle = middle || pe.Buttons == pointer.ButtonTertiary
			}
		}
	}
	for _, p := range v.st.Projects {
		pst := s.project(p.ID)
		for pst.toggle.Clicked(gtx) {
			if s.renamingGroup != p.ID && !dropped { // a click in the name field is not a toggle
				s.expanded[p.ID] = !s.isExpanded(p.ID)
			}
		}
		for pst.add.Clicked(gtx) {
			s.events = append(s.events, NewTab{GroupID: p.ID})
		}
		for pst.more.Clicked(gtx) {
			s.toggleGroupMenu(p.ID)
		}
		if right, _ := press(&pst.ctx); right {
			s.toggleGroupMenu(p.ID)
		}
		if s.appearance == p.ID {
			icon, col := appearanceOf(p)
			for i := range s.iconBtn {
				for s.iconBtn[i].Clicked(gtx) {
					s.events = append(s.events, SetProjectAppearance{ProjectID: p.ID, Icon: projectIcons[i].name, Color: col})
				}
			}
			for i := range s.colorBtn {
				for s.colorBtn[i].Clicked(gtx) {
					s.events = append(s.events, SetProjectAppearance{ProjectID: p.ID, Icon: icon, Color: projectColorIDs[i]})
				}
			}
		}
		if s.groupMenu == p.ID {
			if s.groupItem[0].Clicked(gtx) {
				s.startRename("", p.ID, p.Name)
			}
			if s.groupItem[1].Clicked(gtx) {
				s.groupMenu, s.appearance = "", p.ID
			}
			if s.groupItem[2].Clicked(gtx) {
				s.events = append(s.events, Ungroup{GroupID: p.ID})
				s.groupMenu = ""
			}
			if s.groupItem[3].Clicked(gtx) {
				s.events = append(s.events, NewWorktreeSession{GroupID: p.ID})
				s.groupMenu = ""
			}
			if s.groupItem[4].Clicked(gtx) {
				s.events = append(s.events, NewTaskIn{GroupID: p.ID})
				s.groupMenu = ""
			}
		}
	}
	for _, ws := range v.st.Workspaces {
		r := s.row(ws.ID)
		for {
			c, ok := r.click.Update(gtx)
			if !ok {
				break
			}
			switch {
			case dropped:
			case c.NumClicks >= 2 && c.Modifiers == 0:
				s.startRename(ws.ID, "", Title(ws))
			default:
				s.click(v, ws.ID, c.Modifiers)
			}
		}
		for r.fold.Clicked(gtx) {
			if !dropped {
				if s.folded == nil {
					s.folded = map[string]bool{}
				}
				s.folded[ws.ID] = !s.folded[ws.ID]
			}
		}
		for r.more.Clicked(gtx) {
			s.toggleMenu(ws.ID)
		}
		for r.close.Clicked(gtx) {
			s.events = append(s.events, CloseTab{WorkspaceID: ws.ID})
		}
		s.answerClicks(gtx, v, ws.ID, r)
		s.prClicks(gtx, v, ws)
		right, middle := press(&r.ctx)
		if right {
			s.toggleMenu(ws.ID)
		}
		if middle {
			s.events = append(s.events, CloseTab{WorkspaceID: ws.ID})
		}
		if kill := s.killBtn[ws.ID]; kill != nil {
			for kill.Clicked(gtx) {
				if s.killArmed != ws.ID {
					s.killArmed = ws.ID
					continue
				}
				s.events = append(s.events, KillSession{WorkspaceID: ws.ID})
				s.killArmed = ""
			}
		}
		if at := s.attachBtn[ws.ID]; at != nil {
			for at.Clicked(gtx) {
				s.events = append(s.events, AttachSession{WorkspaceID: ws.ID})
				s.closeMenus()
			}
		}
	}
	if s.menuWS != "" {
		id := s.menuWS
		group := func(e Event) {
			s.events = append(s.events, e)
			s.closeMenus()
			clear(s.selected)
		}
		if s.menuItem[actRename].Clicked(gtx) {
			for _, ws := range v.st.Workspaces {
				if ws.ID == id {
					s.startRename(id, "", Title(ws))
				}
			}
		}
		if s.menuItem[actNewBelow].Clicked(gtx) {
			s.events = append(s.events, NewTab{After: id})
			s.closeMenus()
		}
		if s.menuItem[actClose].Clicked(gtx) {
			s.events = append(s.events, CloseTab{WorkspaceID: id})
			s.closeMenus()
		}
		if s.menuItem[actMove].Clicked(gtx) {
			s.moveOpen = true
		}
		for g, c := range s.moveBtn {
			if c.Clicked(gtx) {
				s.expanded[g] = true
				group(MoveToGroup{WorkspaceIDs: s.targets(v, id), GroupID: g})
			}
		}
		if s.menuItem[actNewGroup].Clicked(gtx) {
			s.pending = map[string]bool{}
			for _, p := range v.st.Projects {
				s.pending[p.ID] = true
			}
			group(NewGroup{WorkspaceIDs: s.targets(v, id)})
		}
		if s.menuItem[actUngroup].Clicked(gtx) {
			group(MoveToGroup{WorkspaceIDs: s.targets(v, id)})
		}
		if s.menuItem[actGroupFolder].Clicked(gtx) {
			s.events = append(s.events, GroupByFolder{WorkspaceID: id})
			s.closeMenus()
		}
		if s.menuItem[actDetach].Clicked(gtx) {
			s.events = append(s.events, DetachSession{WorkspaceID: id})
			s.closeMenus()
		}
		if s.menuItem[actDelete].Clicked(gtx) {
			s.events = append(s.events, DeleteWorkspace{WorkspaceID: id})
			s.closeMenus()
		}
		diffClicked, prClicked := s.menuItem[actDiff].Clicked(gtx), s.menuItem[actPR].Clicked(gtx)
		for _, ws := range v.st.Workspaces {
			if ws.ID != id || !diffClicked && !prClicked {
				continue
			}
			// An off entry stays open and does nothing.
			diff, pr := ReviewBlocked(v.st, ws, s.GH)
			if diffClicked && diff == "" {
				s.events = append(s.events, ViewDiff{WorkspaceID: id})
				s.closeMenus()
			}
			if prClicked && pr == "" {
				s.events = append(s.events, CreatePR{WorkspaceID: id})
				s.closeMenus()
			}
		}
	}
	if s.Editing() {
		for {
			ev, ok := s.editor.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				name := strings.TrimSpace(s.editor.Text())
				switch {
				case name == strings.TrimSpace(s.renameFrom):
				case s.renamingGroup != "":
					if name != "" {
						s.events = append(s.events, RenameGroup{GroupID: s.renamingGroup, Name: name})
					}
				default:
					// An empty name goes back to the automatic title.
					s.events = append(s.events, RenameTab{WorkspaceID: s.renaming, Name: name})
				}
				s.cancelRename()
			}
		}
		for {
			_, ok := gtx.Event(key.Filter{Focus: &s.editor, Name: key.NameEscape})
			if !ok {
				break
			}
			s.cancelRename()
		}
		if !s.focusEditor && !gtx.Focused(&s.editor) {
			s.cancelRename()
		}
	}
	s.queueEvents(gtx, v.st)
	s.subEvents(gtx, v, dropped)
	for s.newTab.Clicked(gtx) {
		s.events = append(s.events, NewTab{Loose: true})
	}
	for s.sessions.Clicked(gtx) {
		s.events = append(s.events, OpenSessions{})
	}
	for s.addProject.Clicked(gtx) {
		s.events = append(s.events, AddProject{})
	}
	for s.detached.Clicked(gtx) {
		open := !s.detachedOpen
		s.closeMenus()
		s.detachedOpen = open
	}
	for s.settings.Clicked(gtx) {
		s.events = append(s.events, OpenSettings{})
	}
	for s.upgrade.Clicked(gtx) {
		s.events = append(s.events, RunUpdate{})
	}
	// Refresh hover state for every clickable drawn below.
	drain := func(c *widget.Clickable) {
		for {
			if _, ok := c.Update(gtx); !ok {
				return
			}
		}
	}
	for _, pst := range s.projects {
		drain(&pst.toggle)
		drain(&pst.add)
		drain(&pst.more)
	}
	for i := range s.iconBtn {
		drain(&s.iconBtn[i])
	}
	for i := range s.colorBtn {
		drain(&s.colorBtn[i])
	}
	for _, c := range s.attachBtn {
		drain(c)
	}
	for _, c := range s.killBtn {
		drain(c)
	}
	for _, c := range s.moveBtn {
		drain(c)
	}
	for _, r := range s.rows {
		drain(&r.click)
		drain(&r.more)
		drain(&r.close)
		drain(&r.allow)
		drain(&r.deny)
		drain(&r.pr)
		drain(&r.archive)
		drain(&r.fold)
	}
	for i := range s.menuItem {
		drain(&s.menuItem[i])
	}
	for i := range s.groupItem {
		drain(&s.groupItem[i])
	}
	for _, c := range []*widget.Clickable{&s.addProject, &s.detached, &s.comments, &s.settings, &s.newTab, &s.sessions, &s.upgrade} {
		drain(c)
	}
}

// snapshot is the sidebar state input can change, to spot that it did.
func (s *Sidebar) snapshot() [8]string {
	return [8]string{s.menuWS, s.groupMenu, s.appearance, s.renaming, s.renamingGroup, s.anchor,
		fmt.Sprint(s.detachedOpen, s.moveOpen, s.killArmed), fmt.Sprint(len(s.selected), s.expanded, s.folded)}
}

func (s *Sidebar) closeMenus() {
	s.menuWS, s.groupMenu, s.detachedOpen, s.appearance, s.moveOpen, s.killArmed = "", "", false, "", false, ""
}

func (s *Sidebar) toggleMenu(id string) {
	open := s.menuWS != id
	s.closeMenus()
	if open {
		s.menuWS = id
	}
}

func (s *Sidebar) toggleGroupMenu(id string) {
	open := s.groupMenu != id && s.appearance != id
	s.closeMenus()
	if open {
		s.groupMenu = id
	}
}
