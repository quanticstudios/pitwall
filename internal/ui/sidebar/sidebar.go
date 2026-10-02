// Package sidebar draws the project and workspace tree with agent status,
// the way aide's SidebarTree does.
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
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Event is one of SelectWorkspace, NewSession, AddProject, RenameWorkspace,
// ArchiveWorkspace, DeleteWorkspace, RestoreWorkspace, OpenSettings,
// SetProjectAppearance, MoveToGroup, NewGroup, RenameGroup, Ungroup.
type Event any

type SelectWorkspace struct{ WorkspaceID, PaneID string }

// NewSession comes from the header's "+" (GroupID "") or a group's "+".
type NewSession struct{ GroupID string }
type AddProject struct{}
type RenameWorkspace struct{ WorkspaceID, Name string }
type ArchiveWorkspace struct{ WorkspaceID string }
type DeleteWorkspace struct{ WorkspaceID string }
type RestoreWorkspace struct{ WorkspaceID string }
type OpenSettings struct{}

// SetProjectAppearance carries the project's whole appearance: a lucide
// icon name and an aide color id.
type SetProjectAppearance struct{ ProjectID, Icon, Color string }

// MoveToGroup puts sessions in GroupID; "" takes them out of their group.
type MoveToGroup struct {
	WorkspaceIDs []string
	GroupID      string
}

// NewGroup makes a group of the picked sessions. The sidebar starts an
// inline rename on the group once it shows up in the state.
type NewGroup struct{ WorkspaceIDs []string }
type RenameGroup struct{ GroupID, Name string }

// Ungroup deletes the group; its sessions become ungrouped.
type Ungroup struct{ GroupID string }

// NewWorktreeSession asks for a session in a fresh git worktree of the
// group's repository.
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

	addProject, archived, comments, settings, newSession widget.Clickable

	// Ctrl+click toggles a session in the selection, Shift+click selects
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

	archivedOpen bool
	archivedDel  map[string]*widget.Clickable
	archivedRes  map[string]*widget.Clickable

	appearance string               // project whose icon and color popover is open
	iconBtn    [35]widget.Clickable // one per projectIcons entry
	colorBtn   [len(projectColorIDs)]widget.Clickable
	popover    int // tag that keeps clicks on a popover's body from closing it

	renaming      string // workspace whose name is being edited
	renamingGroup string // or the group's
	focusEditor   bool
	selectAll     bool   // select the name once the field has focus
	renameFrom    string // the name the field started with
	editor        widget.Editor

	events []Event
}

// Session menu actions, in menu order.
const (
	actRename = iota
	actMove
	actNewGroup
	actUngroup
	actArchive
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
	click, more widget.Clickable
	ctx         int // tag for right-click
}

// Layout draws st and returns events from this frame's input.
func (s *Sidebar) Layout(gtx layout.Context, th *theme.Theme, st *model.State, activeWorkspace string) (layout.Dimensions, []Event) {
	if s.expanded == nil {
		s.epoch = gtx.Now
		s.expanded = map[string]bool{}
		s.projects = map[string]*projectState{}
		s.rows = map[string]*rowState{}
		s.archivedDel = map[string]*widget.Clickable{}
		s.archivedRes = map[string]*widget.Clickable{}
		s.moveBtn = map[string]*widget.Clickable{}
		s.selected = map[string]bool{}
		s.list.Axis = layout.Vertical
		s.editor.SingleLine = true
		s.editor.Submit = true
	}
	s.events = s.events[:0]
	v := newView(gtx, th, st, activeWorkspace)
	if v.activeProject != s.activeProject {
		s.activeProject = v.activeProject
		if v.activeProject != "" {
			s.expanded[v.activeProject] = true
		}
	}
	for id := range s.selected {
		if _, ok := v.activity[id]; !ok {
			delete(s.selected, id) // gone or archived
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
	animating := false
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.header(gtx, th) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			d, a := s.tree(gtx, v)
			animating = a
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.footer(gtx, v) }),
	)
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(w-1, 0), Max: size}.Op())

	if animating {
		// The pulse, spin and shimmer are 1-3s loops; 30 fps is smooth
		// enough and halves the cost of redrawing while agents work.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	} else {
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
}

func newView(gtx layout.Context, th *theme.Theme, st *model.State, active string) *view {
	v := &view{th: th, st: st, now: gtx.Now, active: active,
		byProject: map[string][]model.Workspace{}, activity: map[string]*model.Activity{}}
	if v.now.IsZero() {
		v.now = time.Now()
	}
	acts := map[string][]model.Activity{}
	for _, a := range st.Activities {
		acts[a.WorkspaceID] = append(acts[a.WorkspaceID], a)
	}
	groups := map[string]bool{}
	for _, p := range st.Projects {
		groups[p.ID] = true
	}
	for _, ws := range st.Workspaces {
		g := ws.ProjectID
		if !groups[g] {
			g = "" // a session whose group is gone shows as ungrouped
		}
		if ws.ID == active {
			v.activeProject = g
		}
		if ws.Archived {
			continue
		}
		v.byProject[g] = append(v.byProject[g], ws)
		v.activity[ws.ID] = model.Aggregate(acts[ws.ID])
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

// order is the sessions top to bottom as drawn: ungrouped, then each
// expanded group's.
func (s *Sidebar) order(v *view) []string {
	var out []string
	add := func(g string) {
		for _, ws := range v.byProject[g] {
			out = append(out, ws.ID)
		}
	}
	add("")
	for _, p := range v.st.Projects {
		if s.isExpanded(p.ID) {
			add(p.ID)
		}
	}
	return out
}

// click applies a plain, Ctrl or Shift click on session id.
func (s *Sidebar) click(v *view, id string, mods key.Modifiers) {
	switch {
	case mods.Contain(key.ModShortcut):
		if len(s.selected) == 0 && v.active != "" && v.active != id {
			s.selected[v.active] = true // Ctrl+click extends from the open session
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

// targets are the sessions a menu action on id applies to: the selection
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
func (s *Sidebar) Editing() bool { return s.renaming != "" || s.renamingGroup != "" }

func (s *Sidebar) startRename(ws, group, name string) {
	s.renaming, s.renamingGroup, s.focusEditor = ws, group, true
	s.menuWS, s.groupMenu = "", ""
	s.editor.SetText(name)
	s.editor.SetCaret(utf8.RuneCountInString(name), 0)
	s.renameFrom = name
}

// update drains input from last frame's widgets before anything is drawn,
// so hover backgrounds and menus reflect this frame's state.
func (s *Sidebar) update(gtx layout.Context, v *view) {
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
	rightClick := func(tag event.Tag) bool {
		hit := false
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
			if !ok {
				return hit
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary {
				hit = true
			}
		}
	}
	for _, p := range v.st.Projects {
		pst := s.project(p.ID)
		for pst.toggle.Clicked(gtx) {
			if s.renamingGroup != p.ID { // a click in the name field is not a toggle
				s.expanded[p.ID] = !s.isExpanded(p.ID)
			}
		}
		for pst.add.Clicked(gtx) {
			s.events = append(s.events, NewSession{GroupID: p.ID})
		}
		for pst.more.Clicked(gtx) {
			s.toggleGroupMenu(p.ID)
		}
		if rightClick(&pst.ctx) {
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
			s.click(v, ws.ID, c.Modifiers)
		}
		for r.more.Clicked(gtx) {
			s.toggleMenu(ws.ID)
		}
		if rightClick(&r.ctx) {
			s.toggleMenu(ws.ID)
		}
		if del := s.archivedDel[ws.ID]; del != nil {
			for del.Clicked(gtx) {
				s.events = append(s.events, DeleteWorkspace{WorkspaceID: ws.ID})
				s.archivedOpen = false
			}
		}
		if res := s.archivedRes[ws.ID]; res != nil {
			for res.Clicked(gtx) {
				s.events = append(s.events, RestoreWorkspace{WorkspaceID: ws.ID})
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
					s.startRename(id, "", ws.Name)
				}
			}
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
		if s.menuItem[actArchive].Clicked(gtx) {
			s.events = append(s.events, ArchiveWorkspace{WorkspaceID: id})
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
				if name := strings.TrimSpace(s.editor.Text()); name != "" {
					if s.renamingGroup != "" {
						s.events = append(s.events, RenameGroup{GroupID: s.renamingGroup, Name: name})
					} else {
						s.events = append(s.events, RenameWorkspace{WorkspaceID: s.renaming, Name: name})
					}
				}
				s.renaming, s.renamingGroup = "", ""
			}
		}
		for {
			_, ok := gtx.Event(key.Filter{Focus: &s.editor, Name: key.NameEscape})
			if !ok {
				break
			}
			s.renaming, s.renamingGroup = "", ""
		}
	}
	for s.newSession.Clicked(gtx) {
		s.events = append(s.events, NewSession{})
	}
	for s.addProject.Clicked(gtx) {
		s.events = append(s.events, AddProject{})
	}
	for s.archived.Clicked(gtx) {
		open := !s.archivedOpen
		s.closeMenus()
		s.archivedOpen = open
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
	for _, c := range s.archivedRes {
		drain(c)
	}
	for _, c := range s.archivedDel {
		drain(c)
	}
	for _, c := range s.moveBtn {
		drain(c)
	}
	for _, r := range s.rows {
		drain(&r.click)
		drain(&r.more)
	}
	for i := range s.menuItem {
		drain(&s.menuItem[i])
	}
	for i := range s.groupItem {
		drain(&s.groupItem[i])
	}
	for _, c := range []*widget.Clickable{&s.addProject, &s.archived, &s.comments, &s.settings, &s.newSession} {
		drain(c)
	}
}

// snapshot is the sidebar state input can change, to spot that it did.
func (s *Sidebar) snapshot() [8]string {
	return [8]string{s.menuWS, s.groupMenu, s.appearance, s.renaming, s.renamingGroup, s.anchor,
		fmt.Sprint(s.archivedOpen, s.moveOpen), fmt.Sprint(len(s.selected), s.expanded)}
}

func (s *Sidebar) closeMenus() {
	s.menuWS, s.groupMenu, s.archivedOpen, s.appearance, s.moveOpen = "", "", false, "", false
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
			return drawIcon(gtx, icSquareTerm, gtx.Dp(22), th.Primary, 0)
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), 14, th.Fg, "pitwall")
		}},
		item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return iconButton(gtx, th, &s.newSession, icPlus, gtx.Dp(28), gtx.Dp(16), true)
		}},
	)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// tree lays out the ungrouped sessions, then the groups, and reports whether
// anything animates.
func (s *Sidebar) tree(gtx layout.Context, v *view) (layout.Dimensions, bool) {
	animating := false
	projects := v.st.Projects
	loose := len(v.byProject[""]) > 0
	d := s.list.Layout(gtx, len(projects)+2, func(gtx layout.Context, i int) layout.Dimensions {
		switch i {
		case 0:
			if !loose {
				return layout.Dimensions{}
			}
			d, a := s.rowList(gtx, v, v.byProject[""])
			animating = animating || a
			return d
		case len(projects) + 1:
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(6))} // pb-1.5
		}
		d, a := s.group(gtx, v, projects[i-1], loose || i > 1)
		animating = animating || a
		return d
	})
	return d, animating
}

// rowList stacks session rows between pt-1 and pb-1, gap-0.5 apart.
func (s *Sidebar) rowList(gtx layout.Context, v *view, wss []model.Workspace) (layout.Dimensions, bool) {
	y := gtx.Dp(4)
	animating := false
	for i, ws := range wss {
		if i > 0 {
			y += gtx.Dp(2)
		}
		off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d, a := s.workspaceRow(gtx, v, ws)
		off.Pop()
		y += d.Size.Y
		animating = animating || a
	}
	y += gtx.Dp(4)
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}, animating
}

func (s *Sidebar) group(gtx layout.Context, v *view, p model.Project, sep bool) (layout.Dimensions, bool) {
	th := v.th
	w := gtx.Constraints.Max.X
	y := 0
	if sep {
		// mt-1.5 border-t border-border/60 pt-1.5
		y = gtx.Dp(6)
		paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Border, 0.6), clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
		y += 1 + gtx.Dp(6)
	}
	off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	y += s.projectHeader(gtx, v, p).Size.Y
	off.Pop()

	animating := false
	if s.isExpanded(p.ID) {
		off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d, a := s.rowList(gtx, v, v.byProject[p.ID])
		off.Pop()
		y += d.Size.Y
		animating = a
	}
	return layout.Dimensions{Size: image.Pt(w, y)}, animating
}

func (s *Sidebar) projectHeader(gtx layout.Context, v *view, p model.Project) layout.Dimensions {
	th := v.th
	ps := s.project(p.ID)
	w, h := gtx.Constraints.Max.X, gtx.Dp(40) // --pane-header-h
	hovered := ps.toggle.Hovered() || ps.add.Hovered() || ps.more.Hovered()
	if hovered {
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(8)).Op(gtx.Ops))
	}
	attention := 0
	for _, ws := range v.byProject[p.ID] {
		if model.Tier(v.activity[ws.ID]) == model.TierAttention {
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
			{&s.groupItem[0], icPencil, "Rename group", false, false},
			{&s.groupItem[1], projectIcon("palette"), "Icon and color", false, false},
		}
		if p.Kind == model.ProjectGit {
			entries = append(entries, menuEntry{&s.groupItem[3], projectIcon("git-branch"), "New worktree session", false, false})
		}
		entries = append(entries, menuEntry{&s.groupItem[2], projectIcon("layers"), "Ungroup", false, true})
		s.menuList(gtx, th, btn, entries)
		op.Defer(gtx.Ops, m.Stop())
	}
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func (s *Sidebar) workspaceRow(gtx layout.Context, v *view, ws model.Workspace) (layout.Dimensions, bool) {
	th := v.th
	r := s.row(ws.ID)
	a := v.activity[ws.ID]
	isActive := ws.ID == v.active
	stats, hasStats := v.st.Stats[ws.ID]

	// py-2, line 1 (13px * 1.5), gap-1, line 2 (11px * 1.5), py-2
	w := gtx.Constraints.Max.X
	pad, l1, l2 := gtx.Dp(8), gtx.Dp(19.5), gtx.Dp(16.5)
	h := pad + l1 + gtx.Dp(4) + l2 + pad
	rect := image.Rect(0, 0, w, h)
	rr := gtx.Dp(8)

	animating := model.Pulses(a)
	hovered := r.click.Hovered() || r.more.Hovered()
	base := rowBase(th, a, isActive, hovered)
	selected := s.selected[ws.ID]
	if selected && !isActive {
		base = theme.Mix(base, th.Primary, 0.07)
	}
	if base != th.Sidebar {
		paint.FillShape(gtx.Ops, base, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	}
	if animating {
		shimmer(gtx, rect, rr, base, v.t(s))
	}
	if selected {
		paint.FillShape(gtx.Ops, theme.Mix(base, th.Primary, 0.55), clip.Stroke{Path: clip.UniformRRect(rect.Inset(1), rr-1).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	}

	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &r.ctx)
	cg := gtx
	cg.Constraints = layout.Exact(rect.Size())
	clickable(cg, &r.click, func(gtx layout.Context) layout.Dimensions {
		// pl-3 pr-10
		left := gtx.Dp(12)
		inner := w - left - gtx.Dp(40)
		gtx.Constraints = layout.Exact(image.Pt(inner, l1))
		off := op.Offset(image.Pt(left, pad)).Push(gtx.Ops)
		nameCol := theme.Mix(base, th.Fg, 0.95)
		if isActive || model.Tier(a) == model.TierAttention {
			nameCol = th.Fg
		}
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return s.stateIcon(gtx, v, ws, a, isActive, base) }},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if s.renaming == ws.ID {
					return s.renameField(gtx, th)
				}
				return label(gtx, th, semibold(th.UIFont), 13, nameCol, ws.Name)
			}},
		}
		if a != nil {
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions { return pill(gtx, v, s, *a, base) }})
		}
		hrow(gtx, l1, gtx.Dp(8), items...)
		off.Pop()

		// pl-5 under the name, text-[11px] muted/70
		muted := theme.Mix(base, th.Muted, 0.7)
		gtx.Constraints = layout.Exact(image.Pt(inner-gtx.Dp(20), l2))
		off = op.Offset(image.Pt(left+gtx.Dp(20), pad+l1+gtx.Dp(4))).Push(gtx.Ops)
		var line []item
		inRepo := ws.Branch != ""
		if inRepo && hasStats && (stats.Additions > 0 || stats.Deletions > 0) {
			line = append(line, item{w: func(gtx layout.Context) layout.Dimensions {
				return hrowFit(gtx, l2, gtx.Dp(6),
					item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 10, th.Green, fmt.Sprintf("+%d", stats.Additions))
					}},
					item{w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, semibold(th.UIFont), 10, th.Red, fmt.Sprintf("-%d", stats.Deletions))
					}},
				)
			}})
		}
		where := ws.Branch
		if !inRepo {
			where = ShortPath(ws.Path)
		}
		line = append(line, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.MonoFont, 11, muted, where)
		}})
		if inRepo && hasStats && stats.MergeStatus == model.MergeConflicts {
			line = append(line,
				item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 11, theme.Mix(base, th.Muted, 0.5), "·")
				}},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, 11, th.Red, "Merge conflicts")
				}},
			)
		} else if rt := relTime(v.now, ws.UpdatedAt); rt != "" {
			line = append(line, item{ml: gtx.Dp(4), w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, 11, muted, rt)
			}})
		}
		hrow(gtx, l2, gtx.Dp(6), line...)
		off.Pop()
		return layout.Dimensions{Size: rect.Size()}
	})
	area.Pop()

	// The "…" trigger: absolute right-1 top-1.5, visible on row hover.
	btn := gtx.Dp(24)
	pos := image.Pt(w-gtx.Dp(4)-btn, gtx.Dp(6))
	off := op.Offset(pos).Push(gtx.Ops)
	if hovered || s.menuWS == ws.ID {
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

func (v *view) t(s *Sidebar) float64 { return v.now.Sub(s.epoch).Seconds() }

// stateIcon is renderWorkspaceStateIcon: a 12px glyph per agent state.
func (s *Sidebar) stateIcon(gtx layout.Context, v *view, ws model.Workspace, a *model.Activity, isActive bool, base color.NRGBA) layout.Dimensions {
	th := v.th
	sz := gtx.Dp(12)
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
	return drawIcon(gtx, icGitBranch, sz, stateColor(th, a.State), 0)
}

// pill is ActivityPill: rounded-full, px-1.5 py-px, 10px semibold, a pulsing
// dot while the agent works.
func pill(gtx layout.Context, v *view, s *Sidebar, a model.Activity, base color.NRGBA) layout.Dimensions {
	th := v.th
	col := stateColor(th, a.State)
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
	icChevronRight = "M9 18l6-6-6-6"
	icFolderPlus   = "M12 10v6M9 13h6M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"
	icFolderMinus  = "M9 13h6M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"
	icFolderInput  = "M2 9V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H20a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-1M2 13h10M9 16l3-3-3-3"
)

type menuEntry struct {
	c          *widget.Clickable
	icon, text string
	danger     bool // red, like Delete
	sep        bool // a divider above it
}

// menu is WorkspaceOverflowMenu plus the grouping actions. Move, new group
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
		newText = fmt.Sprintf("New group from %d sessions", len(targets))
	}
	entries := []menuEntry{{c: &s.menuItem[actRename], icon: icPencil, text: "Rename session"}}
	move := -1
	if len(groups) > 0 {
		move = len(entries)
		entries = append(entries, menuEntry{c: &s.menuItem[actMove], icon: icFolderInput, text: "Move to group"})
	}
	entries = append(entries, menuEntry{c: &s.menuItem[actNewGroup], icon: icFolderPlus, text: newText})
	if grouped {
		entries = append(entries, menuEntry{c: &s.menuItem[actUngroup], icon: icFolderMinus, text: "Remove from group"})
	}
	entries = append(entries,
		menuEntry{c: &s.menuItem[actArchive], icon: icArchive, text: "Archive", sep: true},
		menuEntry{c: &s.menuItem[actDelete], icon: icTrash, text: "Delete…", danger: true},
	)
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
		s.menuRow(gtx, th, c, image.Pt(p, p+i*itemH), image.Pt(w-2*p, itemH), projectIcon(g.Icon), th.ProjectColor(g.Color), g.Name, th.Fg)
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
		s.menuRow(gtx, th, e.c, image.Pt(p, tops[i]), image.Pt(w-2*p, itemH), e.icon, iconCol, e.text, col)
	}
	return tops
}

// PillText is a session pill's label: the command a busy terminal runs,
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

func (s *Sidebar) menuRow(gtx layout.Context, th *theme.Theme, c *widget.Clickable, at, size image.Point, icon string, iconCol color.NRGBA, text string, col color.NRGBA) {
	defer op.Offset(at).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(size)
	clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		gtx.Constraints = layout.Exact(image.Pt(size.X-gtx.Dp(16), size.Y))
		defer op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops).Pop()
		hrow(gtx, size.Y, gtx.Dp(8),
			item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icon, gtx.Dp(14), iconCol, 0) }},
			item{shrink: true, w: func(gtx layout.Context) layout.Dimensions { return label(gtx, th, th.UIFont, 13, col, text) }},
		)
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
	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, theme.Hex("#ffffff"), 0.14), clip.UniformRRect(rect.Inset(-1), r+1).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect, r).Op(gtx.Ops))
	hl := clip.UniformRRect(rect, r).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, theme.Hex("#ffffff"), 0.06), clip.Rect{Min: image.Pt(r, 0), Max: image.Pt(size.X-r, 1)}.Op())
	hl.Pop()
}

// footer: Open folder as group, archived, comments (disabled), settings.
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
	}{{&s.archived, icArchive}, {&s.comments, icMessage}, {&s.settings, icSettings}} {
		off := op.Offset(image.Pt(x+i*(btn+gap), 1+px)).Push(gtx.Ops)
		if b.c == &s.comments {
			// aide's comments button is disabled at 50% opacity.
			drawCentered(gtx, btn, func(gtx layout.Context) layout.Dimensions {
				return drawIcon(gtx, icMessage, gtx.Dp(14), theme.Mix(th.Sidebar, th.Muted, 0.5), 0)
			})
		} else {
			iconButton(gtx, th, b.c, b.icon, btn, gtx.Dp(14), true)
		}
		if b.c == &s.archived && s.archivedOpen {
			m := op.Record(gtx.Ops)
			s.archivedMenu(gtx, v, btn)
			op.Defer(gtx.Ops, m.Stop())
		}
		off.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// archivedMenu is ArchivedWorkspacesMenu, placed above its trigger.
func (s *Sidebar) archivedMenu(gtx layout.Context, v *view, trigger int) {
	th := v.th
	s.catcher(gtx)
	var archived []model.Workspace
	for _, ws := range v.st.Workspaces {
		if ws.Archived {
			archived = append(archived, ws)
		}
	}
	w, p, rowH := gtx.Dp(240), gtx.Dp(4), gtx.Dp(40)
	n := max(len(archived), 1)
	size := image.Pt(w, 2*p+n*rowH)
	x := trigger/2 - w/2 // placement "top", centered on the trigger
	defer op.Offset(image.Pt(x, -size.Y-gtx.Dp(8))).Push(gtx.Ops).Pop()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)
	if len(archived) == 0 {
		off := op.Offset(image.Pt(p+gtx.Dp(8), p)).Push(gtx.Ops)
		gtx.Constraints = layout.Exact(image.Pt(w-2*p-gtx.Dp(16), rowH))
		hrow(gtx, rowH, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, 12, th.Muted, "No archived sessions")
		}})
		off.Pop()
		return
	}
	names := map[string]string{}
	for _, pr := range v.st.Projects {
		names[pr.ID] = pr.Name
	}
	for i, ws := range archived {
		del := s.archivedDel[ws.ID]
		if del == nil {
			del = &widget.Clickable{}
			s.archivedDel[ws.ID] = del
		}
		res := s.archivedRes[ws.ID]
		if res == nil {
			res = &widget.Clickable{}
			s.archivedRes[ws.ID] = res
		}
		off := op.Offset(image.Pt(p+gtx.Dp(8), p+i*rowH)).Push(gtx.Ops)
		gtx := gtx
		gtx.Constraints = layout.Exact(image.Pt(w-2*p-gtx.Dp(12), rowH))
		hrow(gtx, rowH, gtx.Dp(4),
			item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return label(gtx, th, th.UIFont, 13, th.Fg, ws.Name) }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						sub, ok := names[ws.ProjectID]
						if !ok {
							sub = ShortPath(ws.Path)
						}
						return label(gtx, th, th.UIFont, 11, th.Muted, sub)
					}),
				)
			}},
			item{right: true, w: func(gtx layout.Context) layout.Dimensions {
				return textButton(gtx, th, res, "Restore", gtx.Dp(28))
			}},
			item{w: func(gtx layout.Context) layout.Dimensions {
				return iconButton(gtx, th, del, icTrash, gtx.Dp(28), gtx.Dp(14), true)
			}},
		)
		off.Pop()
	}
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
	return widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
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

// stateColor is the accent aide uses for a state's pill, dot and icon.
func stateColor(th *theme.Theme, s model.AgentState) color.NRGBA {
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
