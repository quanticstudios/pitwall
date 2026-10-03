// Package sidebar draws the tab list with agent status, the way aide's
// SidebarTree does: ungrouped tabs, then collapsible groups of tabs. A tab
// is a model.Workspace.
package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Event is one of SelectWorkspace, NewTab, CloseTab, RenameTab, AddProject,
// DetachSession, AttachSession, KillSession, GroupByFolder, DeleteWorkspace,
// OpenSettings, SetProjectAppearance, MoveToGroup, NewGroup, RenameGroup,
// Ungroup, NewWorktreeSession, MoveSession, MoveGroup.
type Event any

// SelectWorkspace shows a tab.
type SelectWorkspace struct{ WorkspaceID, PaneID string }

// NewTab opens a tab below After (a row's "+"), or with After "" below the
// open tab: the header's "+" (GroupID "") or a group's "+", which puts it
// last in GroupID when the open tab is elsewhere.
type NewTab struct{ After, GroupID string }

// CloseTab closes a tab and all its panes.
type CloseTab struct{ WorkspaceID string }

// RenameTab names a tab; "" goes back to the automatic title.
type RenameTab struct{ WorkspaceID, Name string }
type AddProject struct{}
type DetachSession struct{ WorkspaceID string }
type AttachSession struct{ WorkspaceID string }

// KillSession comes from the detached list after its inline confirm.
type KillSession struct{ WorkspaceID string }

// GroupByFolder groups the ungrouped tabs sharing WorkspaceID's RepoRoot.
type GroupByFolder struct{ WorkspaceID string }
type DeleteWorkspace struct{ WorkspaceID string }
type OpenSettings struct{}

// SetProjectAppearance carries the project's whole appearance: a lucide
// icon name and an aide color id.
type SetProjectAppearance struct{ ProjectID, Icon, Color string }

// MoveToGroup puts tabs in GroupID; "" takes them out of their group.
type MoveToGroup struct {
	WorkspaceIDs []string
	GroupID      string
}

// NewGroup makes a group of the picked tabs. The sidebar starts an
// inline rename on the group once it shows up in the state.
type NewGroup struct{ WorkspaceIDs []string }
type RenameGroup struct{ GroupID, Name string }

// Ungroup deletes the group; its tabs become ungrouped.
type Ungroup struct{ GroupID string }

// NewWorktreeSession asks for a tab in a fresh git worktree of the group's
// repository.
type NewWorktreeSession struct{ GroupID string }

// Width is aide's w-72.
const Width unit.Dp = 288

type Sidebar struct {
	epoch         time.Time
	expanded      map[string]bool // explicit toggles; absent means "active project only"
	activeProject string

	projects map[string]*projectState
	rows     map[string]*rowState
	list     layout.List

	addProject, detached, comments, settings, newTab widget.Clickable

	// Ctrl+click toggles a tab in the selection, Shift+click selects
	// the range from anchor. Group actions on a selected row apply to all.
	selected map[string]bool
	anchor   string

	menuWS   string // workspace whose overflow menu is open
	menuItem [actCount]widget.Clickable
	moveOpen bool                         // the "Move to group" flyout shows
	moveBtn  map[string]*widget.Clickable // flyout entries by group id
	dismiss  int                          // tag for the click-outside catcher

	groupMenu string // group whose overflow menu is open
	groupItem [4]widget.Clickable

	// pending holds the group ids from before a NewGroup; the id that is
	// not in it once the state changes is the new group, renamed inline.
	pending map[string]bool

	detachedOpen bool
	killBtn      map[string]*widget.Clickable
	attachBtn    map[string]*widget.Clickable
	killArmed    string // detached tab whose Kill waits for a second click

	appearance string               // project whose icon and color popover is open
	iconBtn    [35]widget.Clickable // one per projectIcons entry
	colorBtn   [len(projectColorIDs)]widget.Clickable
	popover    int // tag that keeps clicks on a popover's body from closing it

	renaming      string // tab whose name is being edited
	renamingGroup string // or the group's
	focusEditor   bool
	editorLaidOut bool
	selectAll     bool   // select the name once the field has focus
	renameFrom    string // the name the field started with
	editor        widget.Editor

	// Drag and drop: the gesture and its pointer tag, the elements laid
	// out this frame, the list's scroll offset, and the slides that move
	// rows apart and back. landing holds where dragged rows were drawn when
	// the drag ended, for their slide into place.
	drag    dragState
	dragTag int
	elems   []elem
	scroll  int
	slides  map[string]*slide
	prevTop map[string]int
	landing map[string]float32
	now     time.Time

	events []Event
}

// Session menu actions, in menu order.
const (
	actRename = iota
	actClose
	actMove
	actNewGroup
	actUngroup
	actGroupFolder
	actDetach
	actDelete
	actCount
)

type projectState struct {
	toggle, add, more widget.Clickable
	ctx               int // tag for right-click
}

// projectColorIDs is aide's PROJECT_COLOR_OPTIONS order.
var projectColorIDs = [...]string{
	"neutral", "red", "orange", "amber", "yellow", "lime", "green", "emerald", "teal",
	"cyan", "sky", "blue", "indigo", "violet", "purple", "fuchsia", "pink", "rose",
}

type rowState struct {
	click, more, add widget.Clickable
	ctx              int // tag for right- and middle-click
}

// Layout draws st and returns events from this frame's input.
func (s *Sidebar) Layout(gtx layout.Context, th *theme.Theme, st *model.State, activeWorkspace string) (layout.Dimensions, []Event) {
	if s.expanded == nil {
		s.epoch = gtx.Now
		s.expanded = map[string]bool{}
		s.projects = map[string]*projectState{}
		s.rows = map[string]*rowState{}
		s.killBtn = map[string]*widget.Clickable{}
		s.attachBtn = map[string]*widget.Clickable{}
		s.moveBtn = map[string]*widget.Clickable{}
		s.selected = map[string]bool{}
		s.list.Axis = layout.Vertical
		s.editor.SingleLine = true
		s.editor.Submit = true
	}
	s.events = s.events[:0]
	s.now = gtx.Now
	v := newView(gtx, th, st, activeWorkspace)
	if s.renaming != "" {
		if _, ok := v.activity[s.renaming]; !ok {
			s.cancelRename()
		}
	}
	if s.renamingGroup != "" && !slices.ContainsFunc(st.Projects, func(p model.Project) bool { return p.ID == s.renamingGroup }) {
		s.cancelRename()
	}
	if v.activeProject != s.activeProject {
		s.activeProject = v.activeProject
		if v.activeProject != "" {
			s.expanded[v.activeProject] = true
		}
	}
	for id := range s.selected {
		if _, ok := v.activity[id]; !ok {
			delete(s.selected, id) // gone or detached
		}
	}
	if s.pending != nil {
		for _, p := range st.Projects {
			if !s.pending[p.ID] {
				s.pending = nil
				s.expanded[p.ID] = true
				s.startRename("", p.ID, p.Name)
				break
			}
		}
	}
	before := s.snapshot()
	s.update(gtx, v)
	if s.snapshot() != before || len(s.events) > 0 {
		// The input landed this frame; draw its result now, and give the
		// window a frame to act on the events.
		gtx.Execute(op.InvalidateCmd{})
	}

	w := min(gtx.Dp(Width), gtx.Constraints.Max.X)
	h := gtx.Constraints.Max.Y
	size := image.Pt(w, h)
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Sidebar, clip.Rect{Max: size}.Op())

	gtx.Constraints = layout.Exact(image.Pt(w-1, h))
	animating, moving := false, false
	s.editorLaidOut = false
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.header(gtx, th) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			d, a, m := s.tree(gtx, v)
			animating, moving = a, m
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.footer(gtx, v) }),
	)
	if !s.editorLaidOut {
		s.cancelRename()
	}
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(w-1, 0), Max: size}.Op())

	switch {
	case moving:
		// Rows sliding, a drag following the pointer: every frame, and
		// only until they stop.
		gtx.Execute(op.InvalidateCmd{})
	case animating:
		// The pulse, spin and shimmer are 1-3s loops; 30 fps is smooth
		// enough and halves the cost of redrawing while agents work.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	default:
		// Relative timestamps tick every 10s in aide too.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(10 * time.Second)})
	}
	return layout.Dimensions{Size: size}, s.events
}

// view is st indexed for one frame.
type view struct {
	th            *theme.Theme
	st            *model.State
	now           time.Time
	active        string
	activeProject string
	byProject     map[string][]model.Workspace // "" holds the ungrouped sessions
	activity      map[string]*model.Activity   // a key for every live session
	unseen        map[string]*model.Activity   // a live session's first unseen activity by priority
	top           []string                     // groups and live ungrouped sessions, in order
	groups        map[string]bool
	agent         map[string]model.Provider // a live session's agent, idle or busy
}

func newView(gtx layout.Context, th *theme.Theme, st *model.State, active string) *view {
	v := &view{th: th, st: st, now: gtx.Now, active: active,
		byProject: map[string][]model.Workspace{}, activity: map[string]*model.Activity{}, unseen: map[string]*model.Activity{}, agent: map[string]model.Provider{}}
	if v.now.IsZero() {
		v.now = time.Now()
	}
	acts, unseen := map[string][]model.Activity{}, map[string][]model.Activity{}
	for _, a := range st.Activities {
		acts[a.WorkspaceID] = append(acts[a.WorkspaceID], a)
		if a.Unseen {
			unseen[a.WorkspaceID] = append(unseen[a.WorkspaceID], a)
		}
	}
	groups := map[string]bool{}
	for _, p := range st.Projects {
		groups[p.ID] = true
	}
	v.groups = groups
	for _, ws := range st.Workspaces {
		g := ws.ProjectID
		if !groups[g] {
			g = "" // a session whose group is gone shows as ungrouped
		}
		if ws.ID == active {
			v.activeProject = g
		}
		if ws.Detached {
			continue
		}
		v.byProject[g] = append(v.byProject[g], ws)
		v.activity[ws.ID] = model.Aggregate(acts[ws.ID])
		if u := model.Aggregate(unseen[ws.ID]); u != nil {
			v.unseen[ws.ID] = u
		}
		if p := AgentOf(st, ws); p != "" {
			v.agent[ws.ID] = p
		}
	}
	for _, id := range st.TopOrder() {
		if _, live := v.activity[id]; live || groups[id] {
			v.top = append(v.top, id)
		}
	}
	return v
}

// groupOf is the group a live session shows under, "" when ungrouped.
func (v *view) groupOf(id string) string {
	for g, wss := range v.byProject {
		for _, ws := range wss {
			if ws.ID == id {
				return g
			}
		}
	}
	return ""
}

// order is the sessions top to bottom as drawn: the top-level items in
// order, an expanded group's sessions at its place.
func (s *Sidebar) order(v *view) []string {
	var out []string
	for _, id := range v.top {
		switch {
		case !v.groups[id]:
			out = append(out, id)
		case s.isExpanded(id):
			for _, ws := range v.byProject[id] {
				out = append(out, ws.ID)
			}
		}
	}
	return out
}

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
		for r.add.Clicked(gtx) {
			s.events = append(s.events, NewTab{After: ws.ID})
		}
		for r.more.Clicked(gtx) {
			s.toggleMenu(ws.ID)
		}
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
	for s.newTab.Clicked(gtx) {
		s.events = append(s.events, NewTab{})
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
		drain(&r.add)
	}
	for i := range s.menuItem {
		drain(&s.menuItem[i])
	}
	for i := range s.groupItem {
		drain(&s.groupItem[i])
	}
	for _, c := range []*widget.Clickable{&s.addProject, &s.detached, &s.comments, &s.settings, &s.newTab} {
		drain(c)
	}
}

// snapshot is the sidebar state input can change, to spot that it did.
func (s *Sidebar) snapshot() [8]string {
	return [8]string{s.menuWS, s.groupMenu, s.appearance, s.renaming, s.renamingGroup, s.anchor,
		fmt.Sprint(s.detachedOpen, s.moveOpen, s.killArmed), fmt.Sprint(len(s.selected), s.expanded)}
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

func (s *Sidebar) isExpanded(projectID string) bool { return s.expanded[projectID] }

func (s *Sidebar) project(id string) *projectState {
	p := s.projects[id]
	if p == nil {
		p = &projectState{}
		s.projects[id] = p
	}
	return p
}

func (s *Sidebar) row(id string) *rowState {
	r := s.rows[id]
	if r == nil {
		r = &rowState{}
		s.rows[id] = r
	}
	return r
}

// header is aide's brand bar: h-14, border-b, px-3, a 24px mark and the name.
func (s *Sidebar) header(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	h := gtx.Dp(56)
	w := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(w, h)}.Op())
	gtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(18*2), h-1))
	off := op.Offset(image.Pt(gtx.Dp(18), 0)).Push(gtx.Ops)
	hrow(gtx, h-1, gtx.Dp(8),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawLogo(gtx, th, gtx.Dp(22))
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), 14, th.Fg, "pitwall")
		}},
		item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return iconButton(gtx, th, &s.newTab, icPlus, gtx.Dp(28), gtx.Dp(16), true)
		}},
	)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// rowHeight is a tab row's height: py-2, line 1 (13px * 1.5), gap-1,
// line 2 (11px * 1.5), py-2.
func rowHeight(gtx layout.Context) int {
	return gtx.Dp(8) + gtx.Dp(19.5) + gtx.Dp(4) + gtx.Dp(16.5) + gtx.Dp(8)
}

// place lays the tree out in content pixels, top-level items in order: a
// run of ungrouped tabs between pt-1 and pb-1, gap-0.5 apart; a group as
// its separator (unless first) and header and, when expanded, its tabs
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
		out = append(out, elem{kind: 's', id: id, group: g, top: y, bot: y + rowH})
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
			y += gtx.Dp(6) + 1 + gtx.Dp(6) // mt-1.5 border-t pt-1.5
		}
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
	offs, moving := s.animate(gtx, elems, total)
	animating := false
	byID := map[string]model.Workspace{}
	for _, ws := range v.st.Workspaces {
		byID[ws.ID] = ws
	}
	d := s.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		w := gtx.Constraints.Max.X
		for i, e := range elems {
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
			o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
			_, a := s.workspaceRow(gtx, v, byID[e.id], false)
			o.Pop()
			animating = animating || a
		}
		return layout.Dimensions{Size: image.Pt(w, total)}
	})
	s.scroll = s.list.Position.Offset
	if s.dragOverlay(gtx, v, d.Size) {
		moving = true
	}
	return d, animating, moving
}

func (s *Sidebar) projectHeader(gtx layout.Context, v *view, p model.Project) layout.Dimensions {
	th := v.th
	ps := s.project(p.ID)
	w, h := gtx.Constraints.Max.X, gtx.Dp(40) // --pane-header-h
	hovered := ps.toggle.Hovered() || ps.add.Hovered() || ps.more.Hovered()
	if hovered {
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
	}
	attention := 0 // tabs with something the user has not seen
	for _, ws := range v.byProject[p.ID] {
		if v.unseen[ws.ID] != nil {
			attention++
		}
	}
	bg := th.Sidebar
	if hovered {
		bg = th.SurfaceSecondary
	}
	nameCol := theme.Mix(bg, th.Fg, 0.8)
	if p.ID == v.activeProject {
		nameCol = th.Fg
	}
	btn := gtx.Dp(24)
	gap := gtx.Dp(4)
	px := gtx.Dp(8)
	// px-2, then [trigger flex-1] gap-1 [+ 24] gap-1 [overflow slot 24].
	triggerW := w - 2*px - 2*gap - 2*btn

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
				return drawIcon(gtx, projectIcon(p.Icon), gtx.Dp(14), th.ProjectColor(p.Color), 0)
			}},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if s.renamingGroup == p.ID {
					gtx.Constraints.Max.Y = gtx.Dp(24)
					return s.renameField(gtx, th)
				}
				return label(gtx, th, semibold(th.UIFont), 14, nameCol, p.Name)
			}},
		}
		if attention > 0 {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(16)
				paint.FillShape(gtx.Ops, theme.Mix(bg, th.Yellow, 0.14), clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
				centered(gtx, image.Pt(d, d), func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, semibold(th.UIFont), 9, th.Yellow, fmt.Sprint(attention))
				})
				return layout.Dimensions{Size: image.Pt(d, d)}
			}})
		}
		hrow(gtx, h, gtx.Dp(8), items...)
		off.Pop()
		return layout.Dimensions{Size: image.Pt(triggerW, h)}
	})
	off.Pop()

	off = op.Offset(image.Pt(px+triggerW+gap, (h-btn)/2)).Push(gtx.Ops)
	iconButton(gtx, th, &ps.add, icPlus, btn, gtx.Dp(14), true)
	off.Pop()

	// The "…" overflow trigger, shown on header hover like aide's.
	mx := px + triggerW + 2*gap + btn
	off = op.Offset(image.Pt(mx, (h-btn)/2)).Push(gtx.Ops)
	if hovered || s.appearance == p.ID || s.groupMenu == p.ID {
		iconButton(gtx, th, &ps.more, icEllipsis, btn, gtx.Dp(16), true)
	} else {
		clickable(gtx, &ps.more, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(btn, btn)}
		})
	}
	if s.appearance == p.ID {
		m := op.Record(gtx.Ops)
		s.appearanceMenu(gtx, th, p, mx, btn)
		op.Defer(gtx.Ops, m.Stop())
	}
	if s.groupMenu == p.ID {
		m := op.Record(gtx.Ops)
		s.catcher(gtx)
		entries := []menuEntry{
			{&s.groupItem[0], icPencil, "Rename group", false, false, ""},
			{&s.groupItem[1], projectIcon("palette"), "Icon and color", false, false, ""},
		}
		if p.Kind == model.ProjectGit {
			entries = append(entries, menuEntry{&s.groupItem[3], projectIcon("git-branch"), "New tab in a worktree", false, false, ""})
		}
		entries = append(entries, menuEntry{&s.groupItem[2], projectIcon("layers"), "Ungroup", false, true, ""})
		s.menuList(gtx, th, btn, entries)
		op.Defer(gtx.Ops, m.Stop())
	}
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// workspaceRow draws tab ws's row: its agent's mark or its state icon,
// title and pill, then the agent's name and the branch with its diff
// stats (or the folder). A ghost row is the lifted copy under the
// pointer: no input, no hover buttons, its fill left to the caller.
func (s *Sidebar) workspaceRow(gtx layout.Context, v *view, ws model.Workspace, ghost bool) (layout.Dimensions, bool) {
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
	hovered := !ghost && (r.click.Hovered() || r.more.Hovered() || r.add.Hovered())
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

	content := func(gtx layout.Context) layout.Dimensions {
		// pl-3 pr-10
		left := gtx.Dp(12)
		inner := w - left - gtx.Dp(40)
		gtx.Constraints = layout.Exact(image.Pt(inner, l1))
		off := op.Offset(image.Pt(left, pad)).Push(gtx.Ops)
		nameCol := theme.Mix(base, th.Fg, 0.95)
		if isActive || ghost || unseen != nil || model.Tier(a) == model.TierAttention {
			nameCol = th.Fg
		}
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return s.stateIcon(gtx, v, ws, a, isActive, base) }},
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

	// The "…" trigger: absolute right-1 top-1.5, visible on row hover, and
	// the new-tab "+" left of it.
	btn := gtx.Dp(24)
	pos := image.Pt(w-gtx.Dp(4)-btn, gtx.Dp(6))
	ao := op.Offset(pos.Sub(image.Pt(btn+gtx.Dp(2), 0))).Push(gtx.Ops)
	if hovered && s.menuWS != ws.ID && !s.drag.active {
		iconButton(gtx, th, &r.add, icPlus, btn, gtx.Dp(14), false)
	}
	ao.Pop()
	off := op.Offset(pos).Push(gtx.Ops)
	if (hovered && !s.drag.active) || s.menuWS == ws.ID {
		iconButton(gtx, th, &r.more, icEllipsis, btn, gtx.Dp(16), false)
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

// projectHeaderGhost draws group p's header for the lifted copy under the
// pointer and returns its height.
func (s *Sidebar) projectHeaderGhost(gtx layout.Context, v *view, p model.Project) int {
	th := v.th
	h := gtx.Dp(40)
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X-gtx.Dp(28), h))
	o := op.Offset(image.Pt(gtx.Dp(14), 0)).Push(gtx.Ops)
	hrow(gtx, h, gtx.Dp(8),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, projectIcon(p.Icon), gtx.Dp(14), th.ProjectColor(p.Color), 0)
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), 14, th.Fg, p.Name)
		}},
	)
	o.Pop()
	return h
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
		return label(gtx, th, semibold(th.UIFont), 10, col, PillText(a))
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

const (
	icX            = "M18 6 6 18M6 6l12 12"
	icChevronRight = "M9 18l6-6-6-6"
	icFolderPlus   = "M12 10v6M9 13h6M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"
	icFolderMinus  = "M9 13h6M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"
	icFolderInput  = "M2 9V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H20a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-1M2 13h10M9 16l3-3-3-3"
)

type menuEntry struct {
	c          *widget.Clickable
	icon, text string
	danger     bool   // red, like Delete
	sep        bool   // a divider above it
	hint       string // muted text at the right edge
}

// menu is WorkspaceOverflowMenu with tab words plus the grouping actions. Move, new group
// and remove act on the whole selection when ws is part of it.
func (s *Sidebar) menu(gtx layout.Context, v *view, ws model.Workspace, trigger int) {
	th := v.th
	s.catcher(gtx)
	targets := s.targets(v, ws.ID)
	cur := v.groupOf(ws.ID)
	var groups []model.Project
	grouped := false
	for _, id := range targets {
		if g := v.groupOf(id); g != "" {
			grouped = true
		}
	}
	for _, p := range v.st.Projects {
		if p.ID != cur || len(targets) > 1 {
			groups = append(groups, p)
		}
	}
	newText := "New group"
	if len(targets) > 1 {
		newText = fmt.Sprintf("New group from %d tabs", len(targets))
	}
	entries := []menuEntry{{c: &s.menuItem[actRename], icon: icPencil, text: "Rename tab"}}
	move := -1
	if len(groups) > 0 {
		move = len(entries)
		entries = append(entries, menuEntry{c: &s.menuItem[actMove], icon: icFolderInput, text: "Move to group"})
	}
	entries = append(entries, menuEntry{c: &s.menuItem[actNewGroup], icon: icFolderPlus, text: newText})
	if grouped {
		entries = append(entries, menuEntry{c: &s.menuItem[actUngroup], icon: icFolderMinus, text: "Remove from group"})
	}
	if n := folderCount(v.st, ws); n > 0 {
		entries = append(entries, menuEntry{c: &s.menuItem[actGroupFolder], icon: projectIcon("folder"),
			text: "Group tabs in " + baseName(ws.RepoRoot), hint: fmt.Sprint(n)})
	}
	entries = append(entries,
		menuEntry{c: &s.menuItem[actDetach], icon: icDetach, text: "Detach tab", sep: true},
		menuEntry{c: &s.menuItem[actClose], icon: icX, text: "Close tab"})
	if ws.WorktreeRoot != "" { // only a worktree pitwall made has a folder to delete
		entries = append(entries, menuEntry{c: &s.menuItem[actDelete], icon: icTrash, text: "Delete…", danger: true})
	}
	// Hovering "Move to group" opens its flyout; hovering another entry
	// closes it.
	for i, e := range entries {
		if e.c.Hovered() {
			s.moveOpen = i == move
		}
	}
	tops := s.menuList(gtx, th, trigger, entries)
	if move < 0 {
		return
	}
	// The chevron marks the submenu.
	off := op.Offset(image.Pt(trigger-gtx.Dp(28), trigger+gtx.Dp(4)+tops[move]+(gtx.Dp(32)-gtx.Dp(14))/2)).Push(gtx.Ops)
	drawIcon(gtx, icChevronRight, gtx.Dp(14), th.Muted, 0)
	off.Pop()
	if !s.moveOpen {
		return
	}
	for id := range s.moveBtn {
		if !slices.ContainsFunc(groups, func(p model.Project) bool { return p.ID == id }) {
			delete(s.moveBtn, id)
		}
	}
	w, itemH, p := gtx.Dp(200), gtx.Dp(32), gtx.Dp(4)
	size := image.Pt(w, 2*p+len(groups)*itemH)
	defer op.Offset(image.Pt(trigger+gtx.Dp(2), trigger+gtx.Dp(4)+tops[move]-p)).Push(gtx.Ops).Pop()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)
	for i, g := range groups {
		c := s.moveBtn[g.ID]
		if c == nil {
			c = &widget.Clickable{}
			s.moveBtn[g.ID] = c
		}
		s.menuRow(gtx, th, c, image.Pt(p, p+i*itemH), image.Pt(w-2*p, itemH), projectIcon(g.Icon), th.ProjectColor(g.Color), g.Name, th.Fg, "")
	}
}

// menuList draws entries as a menu placed "bottom end" under a trigger of
// size trigger and returns each entry's top inside the menu.
func (s *Sidebar) menuList(gtx layout.Context, th *theme.Theme, trigger int, entries []menuEntry) []int {
	w, itemH, p, sepH := gtx.Dp(220), gtx.Dp(32), gtx.Dp(4), gtx.Dp(9)
	tops := make([]int, len(entries))
	y := p
	for i, e := range entries {
		if e.sep {
			y += sepH
		}
		tops[i] = y
		y += itemH
	}
	size := image.Pt(w, y+p)
	defer op.Offset(image.Pt(trigger-w, trigger+gtx.Dp(4))).Push(gtx.Ops).Pop()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)
	for i, e := range entries {
		if e.sep {
			sy := tops[i] - sepH/2 - 1
			paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Border, 0.9), clip.Rect{Min: image.Pt(p+gtx.Dp(4), sy), Max: image.Pt(w-p-gtx.Dp(4), sy+1)}.Op())
		}
		col, iconCol := th.Fg, th.Muted
		if e.danger {
			col, iconCol = th.Red, th.Red
		}
		s.menuRow(gtx, th, e.c, image.Pt(p, tops[i]), image.Pt(w-2*p, itemH), e.icon, iconCol, e.text, col, e.hint)
	}
	return tops
}

// PillText is a tab pill's label: the command a busy terminal runs,
// else aide's label for the state.
func PillText(a model.Activity) string {
	if a.State == model.StateTerminalRunning && a.Detail != "" {
		return a.Detail
	}
	return model.PillLabel(a)
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

func (s *Sidebar) menuRow(gtx layout.Context, th *theme.Theme, c *widget.Clickable, at, size image.Point, icon string, iconCol color.NRGBA, text string, col color.NRGBA, hint string) {
	defer op.Offset(at).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(size)
	clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		gtx.Constraints = layout.Exact(image.Pt(size.X-gtx.Dp(16), size.Y))
		defer op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops).Pop()
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icon, gtx.Dp(14), iconCol, 0) }},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions { return label(gtx, th, th.UIFont, 13, col, text) }},
		}
		if hint != "" {
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), 12, th.Muted, hint)
			}})
		}
		hrow(gtx, size.Y, gtx.Dp(8), items...)
		return layout.Dimensions{Size: size}
	})
}

// catcher covers the whole window under a popover so a click anywhere else
// closes it.
func (s *Sidebar) catcher(gtx layout.Context) {
	const far = 1 << 16
	defer clip.Rect{Min: image.Pt(-far, -far), Max: image.Pt(far, far)}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.dismiss)
}

// blockClicks keeps a click on a popover's body, between its buttons, from
// reaching the catcher underneath and closing it.
func (s *Sidebar) blockClicks(gtx layout.Context, size image.Point) {
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.popover)
}

// appearanceOf is p's icon and color with aide's defaults filled in.
func appearanceOf(p model.Project) (icon, color string) {
	icon, color = p.Icon, p.Color
	if icon == "" {
		icon = "folder"
	}
	if color == "" {
		color = "neutral"
	}
	return icon, color
}

// appearanceMenu is ProjectOverflowMenu without the icon search and the
// setup entry: a 7-column icon grid and aide's color row. x is the trigger's
// offset in the sidebar, so the 320px popover keeps its left edge inside the
// window.
func (s *Sidebar) appearanceMenu(gtx layout.Context, th *theme.Theme, p model.Project, x, trigger int) {
	s.catcher(gtx)
	icon, col := appearanceOf(p)
	w, pad, gap := gtx.Dp(320), gtx.Dp(10), gtx.Dp(4)
	inner := w - 2*pad
	cell, sw := gtx.Dp(32), gtx.Dp(24)
	const cols = 7
	rows := (len(projectIcons) + cols - 1) / cols
	perRow := max(1, (inner+gap)/(sw+gap))
	colorRows := (len(projectColorIDs) + perRow - 1) / perRow
	labelH := gtx.Dp(16)
	gridH := rows*(cell+gap) - gap
	colorH := colorRows*(sw+gap) - gap
	size := image.Pt(w, pad+labelH+gtx.Dp(8)+gridH+gtx.Dp(12)+labelH+gtx.Dp(8)+colorH+pad)

	left := max(trigger-w, gtx.Dp(8)-x) // "bottom end", clamped to the window
	defer op.Offset(image.Pt(left, trigger+gtx.Dp(4))).Push(gtx.Ops).Pop()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)

	section := func(y int, text string) {
		off := op.Offset(image.Pt(pad+gtx.Dp(4), y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(inner, labelH))
		hrow(g, labelH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), 11, th.Muted, text)
		}})
		off.Pop()
	}
	y := pad
	section(y, "Icon")
	y += labelH + gtx.Dp(8)
	colW := (inner - (cols-1)*gap) / cols
	for i, ic := range projectIcons {
		c := &s.iconBtn[i]
		at := image.Pt(pad+(i%cols)*(colW+gap), y+(i/cols)*(cell+gap))
		selected := ic.name == icon
		off := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(cell, cell))
		clickable(g, c, func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(0, 0, cell, cell)
			fg := theme.Mix(th.SurfaceSecondary, th.Fg, 0.85)
			switch {
			case selected:
				bg := theme.Mix(th.SurfaceSecondary, th.Primary, 0.15)
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
				paint.FillShape(gtx.Ops, theme.Mix(bg, th.Primary, 0.5), clip.Stroke{Path: clip.UniformRRect(r, gtx.Dp(8)).Path(gtx.Ops), Width: 1}.Op())
				fg = th.Primary
			case c.Hovered():
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
				fg = th.Fg
			}
			return drawCentered(gtx, cell, func(gtx layout.Context) layout.Dimensions {
				return drawIcon(gtx, ic.d, gtx.Dp(14), fg, 0)
			})
		})
		off.Pop()
	}
	y += gridH + gtx.Dp(12)
	section(y, "Color")
	y += labelH + gtx.Dp(8)
	for i, id := range projectColorIDs {
		c := &s.colorBtn[i]
		at := image.Pt(pad+(i%perRow)*(sw+gap), y+(i/perRow)*(sw+gap))
		off := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(sw, sw))
		clickable(g, c, func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(0, 0, sw, sw)
			if c.Hovered() {
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.Ellipse(r).Op(gtx.Ops))
			}
			if id == col {
				paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.7), clip.Stroke{Path: clip.Ellipse(r).Path(gtx.Ops), Width: 1}.Op())
			}
			d := gtx.Dp(16)
			o := (sw - d) / 2
			paint.FillShape(gtx.Ops, th.ProjectColor(id), clip.Ellipse(image.Rect(o, o, o+d, o+d)).Op(gtx.Ops))
			return layout.Dimensions{Size: r.Size()}
		})
		off.Pop()
	}
}

// textButton is a small ghost button with a text label.
func textButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, text string, h int) layout.Dimensions {
	m := op.Record(gtx.Ops)
	d := label(gtx, th, medium(th.UIFont), 12, th.Fg, text)
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(20), h)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
		}
		off := op.Offset(size.Sub(d.Size).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		return layout.Dimensions{Size: size}
	})
}

// floatingSurface is aide's .floating-surface: popover fill, a 1px
// border-strong ring and an inset top highlight, rounded-lg.
func floatingSurface(gtx layout.Context, th *theme.Theme, size image.Point) {
	r := gtx.Dp(10)
	rect := image.Rectangle{Max: size}
	shadow := rect.Add(image.Pt(0, gtx.Dp(4))).Inset(-gtx.Dp(2))
	paint.FillShape(gtx.Ops, color.NRGBA{A: 90}, clip.UniformRRect(shadow, r+gtx.Dp(2)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Fg, 0.14), clip.UniformRRect(rect.Inset(-1), r+1).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect, r).Op(gtx.Ops))
	hl := clip.UniformRRect(rect, r).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.06), clip.Rect{Min: image.Pt(r, 0), Max: image.Pt(size.X-r, 1)}.Op())
	hl.Pop()
}

// footer: Open folder as group, detached tabs, comments (disabled),
// settings.
func (s *Sidebar) footer(gtx layout.Context, v *view) layout.Dimensions {
	th := v.th
	w := gtx.Constraints.Max.X
	px, btn := gtx.Dp(8), gtx.Dp(28)
	h := 1 + 2*px + btn
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: image.Pt(w, 1)}.Op())
	gap := gtx.Dp(4)
	addW := w - 2*px - 3*(btn+gap)

	off := op.Offset(image.Pt(px, 1+px)).Push(gtx.Ops)
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
				return label(gtx, th, medium(th.UIFont), 12.5, col, "Open folder as group")
			}},
		)
		return layout.Dimensions{Size: image.Pt(addW, btn)}
	})
	off.Pop()

	x := px + addW + gap
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
			s.detachedMenu(gtx, v, btn)
			op.Defer(gtx.Ops, m.Stop())
		}
		off.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// folderCount is how many ungrouped tabs, detached ones included, share
// ws's RepoRoot: the ones "Group tabs in" would move.
func folderCount(st *model.State, ws model.Workspace) int {
	if ws.RepoRoot == "" {
		return 0
	}
	groups := map[string]bool{}
	for _, p := range st.Projects {
		groups[p.ID] = true
	}
	n := 0
	for _, w := range st.Workspaces {
		if w.RepoRoot == ws.RepoRoot && !groups[w.ProjectID] {
			n++
		}
	}
	return n
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

// detachedMenu lists the detached tabs above its trigger, each with its
// agent state, Attach, and Kill behind a second click.
func (s *Sidebar) detachedMenu(gtx layout.Context, v *view, trigger int) {
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
	x := max(trigger/2-w/2, -gtx.Dp(120)) // placement "top", kept inside the sidebar
	defer op.Offset(image.Pt(x, -size.Y-gtx.Dp(8))).Push(gtx.Ops).Pop()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)
	inner := image.Pt(w-2*p-gtx.Dp(16), headH)
	off := op.Offset(image.Pt(p+gtx.Dp(8), p)).Push(gtx.Ops)
	hg := gtx
	hg.Constraints = layout.Exact(inner)
	hrow(hg, headH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), 12, th.Muted, "Detached tabs")
	}})
	off.Pop()
	top := p + headH
	if len(detached) == 0 {
		off := op.Offset(image.Pt(p+gtx.Dp(8), top)).Push(gtx.Ops)
		gtx.Constraints = layout.Exact(image.Pt(inner.X, rowH))
		hrow(gtx, rowH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, 12, th.Muted, "Detach keeps a tab running out of the list")
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
			state, stateCol = PillText(*a), StateColor(th, a.State)
		}
		off := op.Offset(image.Pt(p+gtx.Dp(8), top+i*rowH)).Push(gtx.Ops)
		gtx := gtx
		gtx.Constraints = layout.Exact(image.Pt(w-2*p-gtx.Dp(12), rowH))
		items := []item{
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 13, th.Fg, Title(ws))
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return label(gtx, th, th.UIFont, 11, stateCol, state) }),
				)
			}},
			{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return textButton(gtx, th, at, "Attach", gtx.Dp(28))
			}},
		}
		if s.killArmed == ws.ID {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				return dangerButton(gtx, th, kill, "Kill", gtx.Dp(28))
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

// dangerButton is the armed state of a two-click action: red text on a
// red-soft fill.
func dangerButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, text string, h int) layout.Dimensions {
	m := op.Record(gtx.Ops)
	d := label(gtx, th, semibold(th.UIFont), 12, th.Red, text)
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(20), h)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		a := float32(0.14)
		if c.Hovered() {
			a = 0.22
		}
		paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Red, a), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
		off := op.Offset(size.Sub(d.Size).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		return layout.Dimensions{Size: size}
	})
}

// --- drawing helpers ---

func semibold(f font.Font) font.Font { f.Weight = font.SemiBold; return f }
func medium(f font.Font) font.Font   { f.Weight = font.Medium; return f }

func material(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// label draws one truncated line of text.
func label(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, txt string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	// WrapGraphemes fills the line before the ellipsis; the default policy
	// cuts a hyphenated name like swift-otter-… at its last hyphen.
	return widget.Label{MaxLines: 1, WrapPolicy: text.WrapGraphemes}.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
}

// clickable wraps c with a pointer cursor.
func clickable(gtx layout.Context, c *widget.Clickable, w layout.Widget) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := w(gtx)
		defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return d
	})
}

// iconButton is aide's square ghost icon button: muted glyph, surface-
// secondary fill and foreground glyph on hover.
func iconButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, icon string, size, glyph int, hoverFg bool) layout.Dimensions {
	gtx.Constraints = layout.Exact(image.Pt(size, size))
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		col := th.Muted
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, size, size), gtx.Dp(6)).Op(gtx.Ops))
			if hoverFg {
				col = th.Fg
			}
		}
		return drawCentered(gtx, size, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icon, glyph, col, 0)
		})
	})
}

func drawCentered(gtx layout.Context, size int, w layout.Widget) layout.Dimensions {
	return centered(gtx, image.Pt(size, size), w)
}

// centered draws w centered in a box of size.
func centered(gtx layout.Context, size image.Point, w layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	gtx.Constraints = layout.Constraints{Max: size}
	d := w(gtx)
	call := m.Stop()
	off := op.Offset(size.Sub(d.Size).Div(2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return layout.Dimensions{Size: size}
}

// item is one child of hrow. shrink marks the single child that takes the
// leftover width and truncates (CSS min-w-0 truncate); right pushes it and
// everything after it to the right edge (ml-auto); ml adds left margin.
type item struct {
	w      layout.Widget
	shrink bool
	right  bool
	ml     int
}

// hrow is a CSS `flex items-center` row of fixed height filling the width.
func hrow(gtx layout.Context, h, gap int, items ...item) layout.Dimensions {
	d := hrowFit(gtx, h, gap, items...)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y)}
}

// hrowFit is hrow sized to its content.
func hrowFit(gtx layout.Context, h, gap int, items ...item) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	calls := make([]op.CallOp, len(items))
	dims := make([]layout.Dimensions, len(items))
	measure := func(i, avail int) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(avail, 0), h)}
		dims[i] = items[i].w(g)
		calls[i] = m.Stop()
	}
	used := 0
	for i, it := range items {
		used += it.ml
		if i > 0 {
			used += gap
		}
		if !it.shrink {
			measure(i, maxW-used)
			used += dims[i].Size.X
		}
	}
	for i, it := range items {
		if it.shrink {
			measure(i, maxW-used)
			used += dims[i].Size.X
		}
	}
	x := 0
	for i, it := range items {
		if i > 0 {
			x += gap
		}
		x += it.ml
		if it.right {
			x = max(x, maxW-(used-x))
		}
		off := op.Offset(image.Pt(x, (h-dims[i].Size.Y)/2)).Push(gtx.Ops)
		calls[i].Add(gtx.Ops)
		off.Pop()
		x += dims[i].Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
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
	case a == nil && hovered:
		base = th.SurfaceSecondary
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
