// Package sidebar draws the tab list with agent status, the way aide's
// SidebarTree does: ungrouped tabs, then collapsible groups of tabs. A tab
// is a model.Workspace.
package sidebar

import (
	"image"
	"slices"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Width is aide's w-72.
const Width unit.Dp = 288

type Sidebar struct {
	// ExpandAll starts every group expanded, for a drawing of a session
	// the window does not show (the session switcher's preview).
	ExpandAll bool
	// Update labels the footer's update button; "" hides it.
	Update string
	// Numbers marks the rows, first to ninth from the top, that show their
	// goto_tab digit in place of their icon.
	Numbers [9]bool
	// GH is whether the gh CLI is installed, for Create pull request.
	GH bool
	// Usage is the token use of a tab's agents for its hover card, nil
	// when unknown. ShowCost adds what the tokens cost.
	Usage    func(workspace string) *flow.Usage
	ShowCost bool

	epoch         time.Time
	expanded      map[string]bool // explicit toggles; absent means "active project only"
	activeProject string

	projects map[string]*projectState
	rows     map[string]*rowState
	list     layout.List

	addProject, detached, comments, settings, newTab, sessions, upgrade widget.Clickable

	// session is the session drawn; switchedAt is when it last changed, for
	// the header's flash.
	session    string
	switchedAt time.Time

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

	// The hover card: its state, and where the row it is for was drawn
	// this frame (cardAt's top in window px).
	hover  hoverState
	cardY  int
	cardAt string

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
	actNewBelow
	actDiff
	actPR
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
	click, more, close widget.Clickable
	ctx                int // tag for right- and middle-click
}

// Layout draws session's groups and tabs in st and returns events from this
// frame's input.
func (s *Sidebar) Layout(gtx layout.Context, th *theme.Theme, st *model.State, session, activeWorkspace string) (layout.Dimensions, []Event) {
	if s.projects == nil {
		s.epoch = gtx.Now
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
	if session != s.session {
		if s.session != "" {
			s.switchedAt = gtx.Now
		}
		s.session = session
	}
	name := ""
	if ss := st.Session(session); ss != nil {
		name = ss.Name
	}
	view := st.View(session)
	st = &view
	v := newView(gtx, th, st, session, activeWorkspace)
	if s.renaming != "" {
		if _, ok := v.activity[s.renaming]; !ok {
			s.cancelRename()
		}
	}
	if s.renamingGroup != "" && !slices.ContainsFunc(st.Projects, func(p model.Project) bool { return p.ID == s.renamingGroup }) {
		s.cancelRename()
	}
	if s.expanded == nil {
		s.expanded = map[string]bool{}
	}
	if s.ExpandAll {
		for _, p := range st.Projects {
			if _, set := s.expanded[p.ID]; !set {
				s.expanded[p.ID] = true
			}
		}
	}
	s.follow(v)
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
	s.hoverFrame(gtx)
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
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.header(gtx, th, name) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			// px-2: rows and group headers keep clear of both edges.
			return layout.Inset{Left: listPad, Right: listPad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				d, a, m := s.tree(gtx, v)
				animating, moving = a, m
				return d
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.footer(gtx, v) }),
	)
	if !s.editorLaidOut {
		s.cancelRename()
	}
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(w-1, 0), Max: size}.Op())
	s.drawHover(gtx, v, w, h)

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

func newView(gtx layout.Context, th *theme.Theme, st *model.State, session, active string) *view {
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
	for _, id := range st.TopOrder(session) {
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

// follow expands the active tab's group when the active tab moved to
// another group.
func (s *Sidebar) follow(v *view) {
	if v.activeProject == s.activeProject {
		return
	}
	s.activeProject = v.activeProject
	if v.activeProject != "" {
		if s.expanded == nil {
			s.expanded = map[string]bool{}
		}
		s.expanded[v.activeProject] = true
	}
}

// Rows is session's tabs top to bottom as the sidebar lists them, without
// detached tabs and the tabs of collapsed groups: the rows goto_tab_N
// counts. Like Layout, it expands the active tab's group when the active
// tab moved to another one, so the count holds while the sidebar is hidden.
func (s *Sidebar) Rows(st *model.State, session, active string) []string {
	view := st.View(session)
	v := newView(layout.Context{}, nil, &view, session, active)
	s.follow(v)
	return s.order(v)
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

// hovered reports whether the pointer is over the row or its buttons.
func (r *rowState) hovered() bool {
	return r.click.Hovered() || r.more.Hovered() || r.close.Hovered()
}

func (s *Sidebar) row(id string) *rowState {
	r := s.rows[id]
	if r == nil {
		r = &rowState{}
		s.rows[id] = r
	}
	return r
}
