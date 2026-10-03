// Package app is the window: sidebar on the left, the active workspace's
// split panes on the right, Alt navigation and the Alt-hold switcher.
package app

import (
	"image"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Backend is what the window needs from the daemon. The real one wraps a
// proto.Conn; tests and demos use a fake.
type Backend interface {
	State() model.State
	Frame(pane string) (vt.Grid, vt.Modes, bool)
	Send(msg any) error // a proto message
	Changed() <-chan struct{}
}

// Focuser is optionally implemented by a Backend: proto.FocusSession
// requests from `pitwall attach`, including one queued at startup.
type Focuser interface {
	Focus() <-chan proto.FocusSession
}

const (
	sidebarWidth = unit.Dp(288)
	minRatio     = 0.05
)

// Run opens the window and blocks until it closes.
//
// No client-side decorations: Hyprland tiles the window and draws its own
// border, and Gio asks Wayland compositors for server-side decorations.
func Run(b Backend) error {
	w := new(app.Window)
	w.Option(app.Title("pitwall"), app.Size(1280, 800), app.MinSize(640, 360))
	u := &ui{b: b, panes: map[string]*paneUI{}}
	l := loadConfig()
	u.apply(l)
	var reported string
	reportProblems(l.probs, &reported)
	refreshSchemas()
	u.nav.sidebarHidden = loadGUIState().SidebarHidden
	u.sidebarShown = u.nav.sidebarHidden
	stop := make(chan struct{})
	defer close(stop)
	go watchConfig(stop, func() []string {
		u.cfgMu.Lock()
		defer u.cfgMu.Unlock()
		return []string{config.Path(), filepath.Join(config.Dir(), "themes", u.watchTheme+".toml")}
	}, func() {
		l := loadConfig()
		u.cfgMu.Lock()
		u.next = &l
		u.cfgMu.Unlock()
		w.Invalidate()
	})
	u.watchTheme = l.s.ThemeName
	u.report = func(probs []string) { reportProblems(probs, &reported) }
	u.notifications = newNotifier(b, w.Invalidate, desktopSender())
	defer u.notifications.close()
	if f, ok := b.(Focuser); ok {
		go func() {
			for fs := range f.Focus() {
				u.queueFocus(fs)
				w.Invalidate()
				w.Perform(system.ActionRaise)
			}
		}()
	}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.ConfigEvent:
			u.notifications.setView(&e.Config.Focused, "")
			if !e.Config.Focused {
				u.nav.altHeld, u.nav.pinned, u.nav.swallow = false, false, ""
			}
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
			if u.lastSessionGone() {
				// Like a tmux client when its session ends: the window goes,
				// detached sessions keep running in the daemon.
				w.Perform(system.ActionClose)
			}
		}
	}
}

// Scroller is optionally implemented by a Backend: the scroll position from
// the pane's last proto.Frame (ScrollOffset, ScrollMax).
type Scroller interface {
	Scroll(pane string) (offset, max int)
}

type paneUI struct {
	sentCols   int
	sentRows   int
	focusClick bool // its address is the click-to-focus pointer tag
	// Last, so a zero-size View never shares an address with focusClick.
	view term.View
}

type ui struct {
	hadSession bool // a session has been shown; closing the last one closes the window
	b          Backend
	th         *theme.Theme
	cfg        config.Settings
	nav        nav
	sidebar    sidebar.Sidebar
	panes      map[string]*paneUI
	open       widget.Clickable // empty-state button
	modal      modal
	modeTag    int // holds key focus in tab mode, so typed text skips the pane
	closeTag   int // a press anywhere closes a pinned switcher

	// A reloaded config waits in next for the UI goroutine; watchTheme is
	// the theme name whose file the watcher polls.
	cfgMu      sync.Mutex
	next       *loaded
	watchTheme string
	report     func([]string)

	// The sidebar slides over 200ms; sidebarShown is the visibility the
	// slide is heading to, slideAt when it started.
	sidebarShown bool // true when hidden, matching nav.sidebarHidden
	slideAt      time.Time

	// focusReq is the latest attach request, kept until its session is in
	// the state.
	focusMu  sync.Mutex
	focusReq *proto.FocusSession

	drags     []*gesture.Drag
	drag      *layout.Node // layout being dragged, drawn instead of the state's
	dragWS    string
	dragTab   string
	dragUntil uint64 // keep drawing drag until the state passes this version

	shownAt time.Time // switcher fade-in start

	notifications *notifier
}

func (u *ui) send(msg any) {
	if err := u.b.Send(msg); err != nil {
		log.Printf("pitwall: send %T: %v", msg, err)
	}
}

// flush sends what nav queued outside key handling.
func (u *ui) flush() {
	for _, m := range u.nav.out {
		u.send(m)
	}
	u.nav.out = nil
}

func (u *ui) queueFocus(fs proto.FocusSession) {
	u.focusMu.Lock()
	u.focusReq = &fs
	u.focusMu.Unlock()
}

// applyFocus shows the requested session once the state has it.
func (u *ui) applyFocus(st *model.State) {
	u.focusMu.Lock()
	fs := u.focusReq
	if fs != nil && findWorkspace(st, fs.WorkspaceID) != nil {
		u.focusReq = nil
	} else {
		fs = nil
	}
	u.focusMu.Unlock()
	if fs != nil {
		u.nav.tabMode = false
		u.nav.attachSession(st, fs.WorkspaceID, fs.TabID)
		u.flush()
	}
}

func (u *ui) layout(gtx gl.Context) {
	u.cfgMu.Lock()
	if l := u.next; l != nil {
		u.next = nil
		u.watchTheme = l.s.ThemeName
		u.cfgMu.Unlock()
		u.apply(*l)
		if u.report != nil {
			u.report(l.probs)
		}
	} else {
		u.cfgMu.Unlock()
	}
	if u.th == nil {
		u.th = newTheme()
	}
	st := u.b.State()
	u.nav.sync(&st)
	u.applyFocus(&st)

	wasVisible := u.nav.switcherVisible()
	wasMode := u.nav.tabMode
	defer func() {
		if u.nav.tabMode != wasMode {
			gtx.Execute(op.InvalidateCmd{}) // draw the mode pill's new state now
		}
	}()
	for {
		ev, ok := gtx.Event(asFilters(u.nav.keyFilters())...)
		if !ok {
			break
		}
		if msg := u.nav.key(&st, ev.(key.Event)); msg != nil {
			u.send(msg)
			// Keys queued behind this one act on the state it produced.
			st = u.b.State()
			u.nav.sync(&st)
		}
	}
	if !wasVisible && u.nav.switcherVisible() {
		u.shownAt = gtx.Now
	}
	if t := u.nav.renameTab; t != "" {
		u.nav.renameTab = ""
		if w := findWorkspace(&st, u.nav.workspace); w != nil {
			for i, x := range w.Tabs {
				if x.ID == t {
					u.nav.sidebarHidden = false // the rename field is in the sidebar
					u.sidebar.StartTabRename(w.ID, t, tabLabel(x, i))
				}
			}
		}
	}
	if u.nav.sidebarHidden != u.sidebarShown {
		u.sidebarShown = u.nav.sidebarHidden
		u.slideAt = gtx.Now
		if u.report != nil { // a real window, not a test
			saveGUIState(guiState{SidebarHidden: u.sidebarShown}) // in order, so the last toggle wins
		}
	}
	if u.sidebar.Dragging() {
		// Before the panes, which would take Escape as input.
		if _, ok := gtx.Event(key.Filter{Name: key.NameEscape}); ok {
			u.sidebar.CancelDrag()
		}
	}
	for {
		if _, ok := gtx.Event(key.FocusFilter{Target: &u.modeTag}); !ok {
			break
		}
	}
	event.Op(gtx.Ops, &u.modeTag)
	if u.nav.tabMode && !gtx.Focused(&u.modeTag) {
		gtx.Execute(key.FocusCmd{Tag: &u.modeTag})
	}
	u.flush()

	paint.Fill(gtx.Ops, u.th.Bg)
	sw := gtx.Dp(sidebarWidth)
	// The panes take their new width at once, so the PTYs resize once; the
	// sidebar slides over them (aide's 200ms ease-out).
	slide := min(1, float32(gtx.Now.Sub(u.slideAt))/float32(200*time.Millisecond))
	if u.slideAt.IsZero() {
		slide = 1
	}
	if slide < 1 {
		gtx.Execute(op.InvalidateCmd{})
	}
	ease := 1 - (1-slide)*(1-slide)*(1-slide)
	shown := ease // how much of the sidebar shows
	if u.nav.sidebarHidden {
		shown = 1 - ease
	}
	left := 0
	if !u.nav.sidebarHidden {
		left = sw + 1
	}
	area := image.Rectangle{Min: image.Pt(left, 0), Max: gtx.Constraints.Max}
	off := op.Offset(area.Min).Push(gtx.Ops)
	pgtx := gtx
	pgtx.Constraints = gl.Exact(area.Size())
	u.layoutPanes(pgtx, &st)
	off.Pop()
	if u.nav.tabMode {
		u.drawModePill(gtx, area)
	}

	if x := int(float32(sw+1)*shown + 0.5); x > 0 {
		so := op.Offset(image.Pt(x-sw-1, 0)).Push(gtx.Ops)
		sgtx := gtx
		sgtx.Constraints = gl.Exact(image.Pt(sw, gtx.Constraints.Max.Y))
		for _, ev := range drawSidebar(sgtx, &u.sidebar, u.th, &st, u.nav.workspace) {
			u.sidebarEvent(&st, ev)
		}
		paint.FillShape(gtx.Ops, u.th.Border, clip.Rect{Min: image.Pt(sw, 0), Max: image.Pt(sw+1, area.Max.Y)}.Op())
		so.Pop()
		u.flush()
	}

	u.layoutModal(gtx, &st)
	if u.nav.switcherVisible() {
		u.drawSwitcher(gtx, &st)
		if u.nav.pinned {
			for {
				ev, ok := gtx.Event(pointer.Filter{Target: &u.closeTag, Kinds: pointer.Press})
				if !ok {
					break
				}
				if _, ok := ev.(pointer.Event); ok {
					u.nav.altHeld, u.nav.pinned = false, false
				}
			}
			// The press still reaches what is under it.
			cl := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
			pass := pointer.PassOp{}.Push(gtx.Ops)
			event.Op(gtx.Ops, &u.closeTag)
			pass.Pop()
			cl.Pop()
		}
	}
	if u.notifications != nil {
		u.notifications.setView(nil, u.nav.workspace)
	}
}

func asFilters(fs []key.Filter) []event.Filter {
	out := make([]event.Filter, len(fs))
	for i, f := range fs {
		out[i] = f
	}
	return out
}

func (u *ui) sidebarEvent(st *model.State, ev sidebar.Event) {
	switch e := ev.(type) {
	case sidebar.SelectWorkspace:
		u.nav.selectWorkspace(st, e.WorkspaceID, e.PaneID)
	case sidebar.NewSession:
		u.nav.expectSession(st)
		cwd := newSessionCwd(st, u.nav.workspace, e.GroupID)
		from := ""
		if w := findWorkspace(st, u.nav.workspace); w != nil && cwd == w.Path {
			from = u.nav.focused() // where the open session's shell is now
		}
		u.send(proto.NewSession{Cwd: cwd, GroupID: e.GroupID, FromPane: from})
	case sidebar.MoveToGroup:
		for _, id := range e.WorkspaceIDs {
			u.send(proto.SetSessionGroup{WorkspaceID: id, GroupID: e.GroupID})
		}
	case sidebar.NewGroup:
		u.send(proto.NewGroup{Name: "New group", WorkspaceIDs: e.WorkspaceIDs})
	case sidebar.RenameGroup:
		u.send(proto.RenameGroup{GroupID: e.GroupID, Name: e.Name})
	case sidebar.Ungroup:
		u.send(proto.DeleteGroup{GroupID: e.GroupID})
	case sidebar.RenameWorkspace:
		u.send(proto.RenameWorkspace{WorkspaceID: e.WorkspaceID, Name: e.Name})
	case sidebar.DetachSession:
		u.send(proto.DetachSession{WorkspaceID: e.WorkspaceID, Detached: true})
	case sidebar.AttachSession:
		u.send(proto.DetachSession{WorkspaceID: e.WorkspaceID, Detached: false})
		u.nav.attachSession(st, e.WorkspaceID, "")
	case sidebar.KillSession:
		u.send(proto.KillSession{WorkspaceID: e.WorkspaceID})
	case sidebar.GroupByFolder:
		u.send(proto.GroupByFolder{WorkspaceID: e.WorkspaceID})
	case sidebar.DeleteWorkspace:
		u.modal.open(modalDelete, e.WorkspaceID)
	case sidebar.AddProject:
		u.modal.open(modalAddProject, "")
	case sidebar.OpenSettings:
		u.modal.open(modalSettings, "")
	case sidebar.NewWorktreeSession:
		u.nav.expectSession(st)
		u.send(proto.NewWorkspace{ProjectID: e.GroupID})
	case sidebar.SetProjectAppearance:
		u.send(proto.SetProjectAppearance{ProjectID: e.ProjectID, Icon: e.Icon, Color: e.Color})
	case sidebar.SelectTab:
		if e.WorkspaceID != u.nav.workspace {
			u.nav.selectWorkspace(st, e.WorkspaceID, "")
		}
		if msg := u.nav.selectTab(st, e.TabID); msg != nil {
			u.send(msg)
		}
	case sidebar.NewTab:
		from := ""
		if w := findWorkspace(st, e.WorkspaceID); w != nil {
			if t := shownTab(w); t != nil {
				from = u.nav.focus[focusKey(w.ID, t.ID)]
			}
		}
		delete(u.nav.pick, e.WorkspaceID) // the daemon makes the new tab active
		u.send(proto.NewTab{WorkspaceID: e.WorkspaceID, FromPane: from})
	case sidebar.MoveSession:
		u.send(proto.MoveSession{WorkspaceID: e.WorkspaceID, GroupID: e.GroupID, Before: e.Before})
	case sidebar.MoveGroup:
		u.send(proto.MoveGroup{GroupID: e.GroupID, Before: e.Before})
	case sidebar.CloseTab:
		u.send(proto.CloseTab{WorkspaceID: e.WorkspaceID, TabID: e.TabID})
	case sidebar.RenameTab:
		u.send(proto.RenameTab{WorkspaceID: e.WorkspaceID, TabID: e.TabID, Name: e.Name})
	}
}

// newSessionCwd is where a new session starts: the open session's folder,
// or for a group's "+" the group's root when it has one. The GUI does not
// know a pane's current directory, so the session's start folder stands in.
func newSessionCwd(st *model.State, active, group string) string {
	if group != "" {
		for _, p := range st.Projects {
			if p.ID == group && p.Root != "" {
				return p.Root
			}
		}
	}
	if w := findWorkspace(st, active); w != nil && (group == "" || groupOf(st, *w) == group) {
		return w.Path
	}
	return ""
}

func (u *ui) layoutPanes(gtx gl.Context, st *model.State) {
	live := map[string]bool{}
	defer func() { // a pane no longer drawn has lost focus
		for id, p := range u.panes {
			if live[id] {
				continue
			}
			if b := p.view.Blur(); b != nil {
				u.send(proto.Input{Pane: id, Data: b})
			}
		}
	}()
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil {
		return
	}
	tab := shownTab(ws)
	var root *layout.Node
	tabID := ""
	if tab != nil {
		root, tabID = tab.Layout, tab.ID
	}
	if u.drag != nil && u.dragWS == ws.ID && u.dragTab == tabID && (u.dragUntil == 0 || st.Version <= u.dragUntil) {
		root = u.drag
	} else {
		u.drag = nil
	}
	if root == nil {
		u.emptyState(gtx, st, ws.ID, tabID)
		return
	}

	// Pane frames sit pane_margin in from the edges and pane_gap apart on
	// the canvas's surface fill; the dividers are the gaps.
	m, gap := gtx.Dp(unit.Dp(u.cfg.PaneMargin)), gtx.Dp(unit.Dp(u.cfg.PaneGap))
	area := layout.Rect{X: m, Y: m, W: max(0, gtx.Constraints.Max.X-2*m), H: max(0, gtx.Constraints.Max.Y-2*m)}
	paint.FillShape(gtx.Ops, u.th.Surface, clip.Rect{Max: gtx.Constraints.Max}.Op())
	focused := u.nav.focused()
	if u.modal.kind != modalNone || u.sidebar.Editing() || u.nav.tabMode {
		focused = "" // the dialog, a rename field or tab mode holds key focus
	}
	// A lone pane flush with the edges needs no frame.
	sole := root.Pane != "" && m == 0
	for id, r := range rectsOf(root, area, gap) {
		live[id] = true
		p := u.panes[id]
		if p == nil {
			p = &paneUI{}
			u.panes[id] = p
		}
		p.view.Keys = u.nav.bind()
		u.layoutPane(gtx, p, id, r, id == focused, sole)
	}
	for id := range u.panes {
		if !live[id] && findPane(st, id) == nil {
			delete(u.panes, id)
		}
	}
	u.layoutDividers(gtx, ws.ID, tabID, root, area, gap)
}

func findPane(st *model.State, id string) *model.Pane {
	for i := range st.Panes {
		if st.Panes[i].ID == id {
			return &st.Panes[i]
		}
	}
	return nil
}

// paneChrome draws aide's pane frame (getPaneFrameClassName) and terminal
// pane box (TerminalPane.tsx) inside frame and returns the rect left for the
// grid. A split pane gets rounded-lg border p-4 bg-surface, the focused one
// with border-strong; a sole pane drops that frame. Every terminal sits in
// rounded-lg border border-border bg-background with p-3.
// paneChrome draws one rounded terminal surface per pane, as aide's canvas
// does, with a blue border on the focused pane when there is more than one.
// It returns the rect the terminal fills; the term view pads itself.
func paneChrome(gtx gl.Context, th *theme.Theme, frame image.Rectangle, focused, sole bool) image.Rectangle {
	if sole {
		paint.FillShape(gtx.Ops, th.TermBg, clip.Rect(frame).Op())
		return frame
	}
	r := gtx.Dp(10)
	border := theme.Mix(th.TermBg, th.TermFg, 0.08)
	if focused {
		border = theme.Mix(th.TermBg, th.Primary, 0.75)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(frame, r).Op(gtx.Ops))
	return frame.Inset(1)
}

// roundedFor is the terminal's corner radius inside paneChrome's border.
func roundedFor(gtx gl.Context, sole bool) int {
	if sole {
		return 0
	}
	return gtx.Dp(10) - 1
}

func (u *ui) layoutPane(gtx gl.Context, p *paneUI, id string, r layout.Rect, focused, sole bool) {
	rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(rect.Size())

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.focusClick, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			u.nav.setFocus(id)
		}
	}

	g, m, ok := u.b.Frame(id)
	if !ok {
		g = vt.Grid{}
	}
	if s, ok := u.b.(Scroller); ok {
		off, mx := s.Scroll(id)
		setScroll(&p.view, off, mx)
	}
	cl := clip.Rect{Max: rect.Size()}.Push(gtx.Ops)
	grid := paneChrome(gtx, u.th, image.Rectangle{Max: rect.Size()}, focused, sole)
	var input []byte
	cols, rows := g.Cols, g.Rows
	if !grid.Empty() {
		tg := gtx
		tg.Constraints = gl.Exact(grid.Size())
		o := op.Offset(grid.Min).Push(gtx.Ops)
		rc := clip.UniformRRect(image.Rectangle{Max: grid.Size()}, roundedFor(gtx, sole)).Push(gtx.Ops)
		input, cols, rows = drawTerm(tg, &p.view, u.th, &g, m, focused)
		rc.Pop()
		o.Pop()
	}
	// Clicking anywhere in the frame focuses the pane, as aide's onMouseDown
	// on the pane article does; PassOp lets the grid see the press too.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.focusClick)
	pass.Pop()
	cl.Pop()

	if len(input) > 0 {
		u.send(proto.Input{Pane: id, Data: input})
	}
	if d := p.view.ScrollDelta(); d != 0 {
		u.send(proto.Scroll{Pane: id, Lines: d})
	}
	if (cols != g.Cols || rows != g.Rows) && (cols != p.sentCols || rows != p.sentRows) {
		p.sentCols, p.sentRows = cols, rows
		u.send(proto.Resize{Pane: id, Cols: cols, Rows: rows})
	}
}

func (u *ui) emptyState(gtx gl.Context, st *model.State, ws, tab string) {
	if u.open.Clicked(gtx) {
		u.nav.expectPane(st)
		u.send(proto.OpenPane{WorkspaceID: ws, TabID: tab})
	}
	gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return u.open.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			call, sz := textCall(gtx, u.th, u.th.UIFont, u.th.TextSize, u.th.Fg, strings.TrimSpace("Open a terminal   "+firstChord(u.nav.bind().Global["split_right"])))
			pad := image.Pt(gtx.Dp(16), gtx.Dp(10))
			box := sz.Add(pad.Mul(2))
			bg := u.th.SurfaceSecondary
			if u.open.Hovered() {
				bg = u.th.SurfaceElevated
			}
			rr := gtx.Dp(8)
			paint.FillShape(gtx.Ops, u.th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rr).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rr-1).Op(gtx.Ops))
			o := op.Offset(pad).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			return gl.Dimensions{Size: box}
		})
	})
}

// walkSplits visits every node with its rect. A split's children share its
// space by ratio, with gap pixels between them; path is the child indexes
// from the root.
func walkSplits(n *layout.Node, r layout.Rect, gap int, path []int, fn func(n *layout.Node, r layout.Rect, path []int)) {
	fn(n, r, path)
	if n.Pane != "" || len(n.Children) == 0 {
		return
	}
	total := r.W
	if n.Dir == layout.Vertical {
		total = r.H
	}
	avail := max(0, total-gap*(len(n.Children)-1))
	cum, start := 0.0, 0
	for i, c := range n.Children {
		ratio := 1 / float64(len(n.Children))
		if len(n.Ratios) == len(n.Children) {
			ratio = n.Ratios[i]
		}
		cum += ratio
		end := int(float64(avail)*cum + 0.5)
		if i == len(n.Children)-1 {
			end = avail
		}
		cr := layout.Rect{X: r.X + start + i*gap, Y: r.Y, W: end - start, H: r.H}
		if n.Dir == layout.Vertical {
			cr = layout.Rect{X: r.X, Y: r.Y + start + i*gap, W: r.W, H: end - start}
		}
		walkSplits(c, cr, gap, append(path[:len(path):len(path)], i), fn)
		start = end
	}
}

func cloneNode(n *layout.Node) *layout.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Ratios = append([]float64(nil), n.Ratios...)
	c.Children = make([]*layout.Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = cloneNode(ch)
	}
	return &c
}

// dragRatios moves the divider after child i of n to pos, measured along the
// split's axis from the split's origin, and keeps every child at minRatio or
// more.
func dragRatios(n *layout.Node, avail, gap, i, pos int) {
	if len(n.Ratios) != len(n.Children) {
		n.Ratios = make([]float64, len(n.Children))
		for j := range n.Ratios {
			n.Ratios[j] = 1 / float64(len(n.Children))
		}
	}
	before := 0.0
	for _, r := range n.Ratios[:i] {
		before += r
	}
	pair := n.Ratios[i] + n.Ratios[i+1]
	frac := float64(pos-i*gap) / float64(max(1, avail))
	left := min(max(frac-before, minRatio), pair-minRatio)
	n.Ratios[i], n.Ratios[i+1] = left, pair-left
}

func (u *ui) layoutDividers(gtx gl.Context, ws, tab string, root *layout.Node, area layout.Rect, gap int) {
	slop := gtx.Dp(4) // the hit area reaches past a narrow gap
	k := 0
	walkSplits(root, area, gap, nil, func(n *layout.Node, r layout.Rect, path []int) {
		if n.Pane != "" {
			return
		}
		vertical := n.Dir == layout.Vertical
		axis, total := gesture.Horizontal, r.W
		if vertical {
			axis, total = gesture.Vertical, r.H
		}
		avail := total - gap*(len(n.Children)-1)
		var bounds []int // divider offsets along the axis, from walking children
		walkSplits(n, r, gap, nil, func(c *layout.Node, cr layout.Rect, p []int) {
			if len(p) == 1 && p[0] < len(n.Children)-1 {
				if vertical {
					bounds = append(bounds, cr.Y+cr.H)
				} else {
					bounds = append(bounds, cr.X+cr.W)
				}
			}
		})
		for i, b := range bounds {
			if k == len(u.drags) {
				u.drags = append(u.drags, new(gesture.Drag))
			}
			d := u.drags[k]
			k++
			for {
				e, ok := d.Update(gtx.Metric, gtx.Source, axis)
				if !ok {
					break
				}
				switch e.Kind {
				case pointer.Drag:
					if u.drag == nil || u.dragWS != ws || u.dragTab != tab || u.dragUntil != 0 {
						u.drag, u.dragWS, u.dragTab, u.dragUntil = cloneNode(root), ws, tab, 0
					}
					node := u.drag
					for _, j := range path {
						node = node.Children[j]
					}
					pos := int(e.Position.X) - r.X
					if vertical {
						pos = int(e.Position.Y) - r.Y
					}
					dragRatios(node, avail, gap, i, pos)
				case pointer.Release, pointer.Cancel:
					if u.drag != nil && u.dragUntil == 0 {
						st := u.b.State()
						u.dragUntil = st.Version
						u.send(proto.SetLayout{WorkspaceID: ws, TabID: tab, Layout: cloneNode(u.drag)})
					}
				}
			}
			hit := image.Rect(b-slop, r.Y, b+gap+slop, r.Y+r.H)
			cursor := pointer.CursorColResize
			if vertical {
				hit = image.Rect(r.X, b-slop, r.X+r.W, b+gap+slop)
				cursor = pointer.CursorRowResize
			}
			s := clip.Rect(hit).Push(gtx.Ops)
			cursor.Add(gtx.Ops)
			d.Add(gtx.Ops)
			s.Pop()
		}
	})
}

// lastSessionGone reports when the window has shown a session and now has
// none left to show, because the last one ended or was detached.
func (u *ui) lastSessionGone() bool {
	st := u.b.State()
	visible := slices.ContainsFunc(st.Workspaces, func(w model.Workspace) bool { return !w.Detached })
	if visible {
		u.hadSession = true
	}
	return u.hadSession && !visible
}
