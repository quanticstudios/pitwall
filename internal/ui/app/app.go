// Package app is the window: sidebar on the left, the active workspace's
// split panes on the right, Alt navigation and the Alt-hold switcher.
package app

import (
	"image"
	"log"
	"time"

	"gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

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
	go func() {
		for range b.Changed() {
			w.Invalidate()
		}
	}()
	u := &ui{b: b, th: newTheme(), panes: map[string]*paneUI{}}
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.ConfigEvent:
			if !e.Config.Focused {
				u.nav.altHeld, u.nav.pinned = false, false
			}
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
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
	b       Backend
	th      *theme.Theme
	nav     nav
	sidebar sidebar.Sidebar
	panes   map[string]*paneUI
	open    widget.Clickable // empty-state button
	modal   modal

	drags     []*gesture.Drag
	drag      *layout.Node // layout being dragged, drawn instead of the state's
	dragWS    string
	dragUntil uint64 // keep drawing drag until the state passes this version

	shownAt time.Time // switcher fade-in start
}

func (u *ui) send(msg any) {
	if err := u.b.Send(msg); err != nil {
		log.Printf("pitwall: send %T: %v", msg, err)
	}
}

func (u *ui) layout(gtx gl.Context) {
	st := u.b.State()
	u.nav.sync(&st)

	wasVisible := u.nav.switcherVisible()
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

	paint.Fill(gtx.Ops, u.th.Bg)
	sw := gtx.Dp(sidebarWidth)
	sgtx := gtx
	sgtx.Constraints = gl.Exact(image.Pt(sw, gtx.Constraints.Max.Y))
	for _, ev := range drawSidebar(sgtx, &u.sidebar, u.th, &st, u.nav.workspace) {
		u.sidebarEvent(&st, ev)
	}

	area := image.Rectangle{Min: image.Pt(sw+1, 0), Max: gtx.Constraints.Max}
	paint.FillShape(gtx.Ops, u.th.Border, clip.Rect{Min: image.Pt(sw, 0), Max: image.Pt(sw+1, area.Max.Y)}.Op())
	off := op.Offset(area.Min).Push(gtx.Ops)
	pgtx := gtx
	pgtx.Constraints = gl.Exact(area.Size())
	u.layoutPanes(pgtx, &st)
	off.Pop()

	u.layoutModal(gtx, &st)
	if u.nav.switcherVisible() {
		u.drawSwitcher(gtx, &st)
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
	case sidebar.NewWorkspace:
		u.send(proto.NewWorkspace{ProjectID: e.ProjectID})
	case sidebar.RenameWorkspace:
		u.send(proto.RenameWorkspace{WorkspaceID: e.WorkspaceID, Name: e.Name})
	case sidebar.ArchiveWorkspace:
		u.send(proto.ArchiveWorkspace{WorkspaceID: e.WorkspaceID, Archived: true})
	case sidebar.RestoreWorkspace:
		u.send(proto.ArchiveWorkspace{WorkspaceID: e.WorkspaceID, Archived: false})
	case sidebar.DeleteWorkspace:
		u.modal.open(modalDelete, e.WorkspaceID)
	case sidebar.AddProject:
		u.modal.open(modalAddProject, "")
	case sidebar.OpenSettings:
		u.modal.open(modalSettings, "")
	case sidebar.SetProjectAppearance:
		u.send(proto.SetProjectAppearance{ProjectID: e.ProjectID, Icon: e.Icon, Color: e.Color})
	}
}

func (u *ui) layoutPanes(gtx gl.Context, st *model.State) {
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil {
		return
	}
	root := ws.Layout
	if u.drag != nil && u.dragWS == ws.ID && (u.dragUntil == 0 || st.Version <= u.dragUntil) {
		root = u.drag
	} else {
		u.drag = nil
	}
	if root == nil {
		u.emptyState(gtx, st, ws.ID)
		return
	}

	area := layout.Rect{W: gtx.Constraints.Max.X, H: gtx.Constraints.Max.Y}
	// aide's split: gap-4 between pane frames, on the canvas's surface fill.
	// The dividers are the gaps.
	gap := gtx.Dp(16)
	paint.FillShape(gtx.Ops, u.th.Surface, clip.Rect{Max: gtx.Constraints.Max}.Op())
	focused := u.nav.focused()
	if u.modal.kind != modalNone {
		focused = "" // the dialog holds key focus
	}
	sole := root.Pane != ""
	live := map[string]bool{}
	for id, r := range rectsOf(root, area, gap) {
		live[id] = true
		p := u.panes[id]
		if p == nil {
			p = &paneUI{}
			u.panes[id] = p
		}
		u.layoutPane(gtx, p, id, r, id == focused, sole)
	}
	for id := range u.panes {
		if !live[id] && findPane(st, id) == nil {
			delete(u.panes, id)
		}
	}
	u.layoutDividers(gtx, ws.ID, root, area, gap)
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
	border := theme.Mix(th.TermBg, theme.Hex("#ffffff"), 0.08)
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
			u.nav.focus[u.nav.workspace] = id
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

func (u *ui) emptyState(gtx gl.Context, st *model.State, ws string) {
	if u.open.Clicked(gtx) {
		u.nav.expectPane(st)
		u.send(proto.OpenPane{WorkspaceID: ws})
	}
	gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return u.open.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			call, sz := textCall(gtx, u.th, u.th.UIFont, u.th.TextSize, u.th.Fg, "Open a terminal   Alt+N")
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

func (u *ui) layoutDividers(gtx gl.Context, ws string, root *layout.Node, area layout.Rect, gap int) {
	slop := gtx.Dp(3)
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
					if u.drag == nil || u.dragWS != ws || u.dragUntil != 0 {
						u.drag, u.dragWS, u.dragUntil = cloneNode(root), ws, 0
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
						u.send(proto.SetLayout{WorkspaceID: ws, Layout: cloneNode(u.drag)})
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
