package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"

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
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/board"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ToggleBoard is a click on the header's board button.
type ToggleBoard struct{}

// CloseBoard is Esc on the board with no scope left to leave.
type CloseBoard struct{}

// BoardOptions are what the board needs from the window each frame.
type BoardOptions struct {
	// Hosts is each session's machine by session id, "" for this one.
	Hosts map[string]string
	// GuardDeny is [agents] guard_deny: an approval whose command it or
	// the guard's own list names gets no Allow.
	GuardDeny []string
	// AllowKey and DenyKey name allow_prompt's and deny_prompt's keys for
	// the footer, "" when unbound.
	AllowKey, DenyKey string
}

const (
	boardColMin unit.Dp = 288 // a column's least width; more scroll sideways
	boardGap    unit.Dp = 10  // between columns
	boardPad    unit.Dp = 14  // the board's sides
	cardGap     unit.Dp = 6   // between cards
	cardIndent  unit.Dp = 23  // a card's lines under its title, past its mark
	boardMove           = 200 * time.Millisecond
)

// boardState is the board's own state: its scope, cursor, scrolls,
// widgets and motion.
type boardState struct {
	scope  board.Scope
	shells bool
	cur    string // the card with keyboard focus, "" for none
	reveal bool   // scroll the cur card into view once: the keys moved it
	// shown is the cards drawn whole inside the board and still last frame,
	// the ones keys may answer.
	shown   map[string]bool
	tr      board.Tracker
	focus   bool // take key focus this frame
	tag     int  // key focus
	scrollT int  // the scroll handler
	cards   map[string]*boardCard
	b       board.Board
	open    bool // the scope menu shows
	opened  time.Time

	x       int // sideways scroll
	colY    [board.Columns]int
	maxY    [board.Columns]int
	colX    [board.Columns]int // each column's left in content, as drawn
	colW    int
	area    image.Rectangle // the columns' area, in the board
	scopeBt widget.Clickable
	shellBt widget.Clickable
	newTask widget.Clickable
	chips   [board.Columns]widget.Clickable
	scopes  []boardScope
	foot    [3]widget.Clickable
	menuTag int

	// Motion: where each card was drawn last frame, in content, and its
	// column; the moves and fades in flight.
	last  map[string]cardPlace
	moves map[string]cardMove
	born  map[string]time.Time
}

type cardPlace struct {
	at  image.Point
	col board.Column
}

type cardMove struct {
	from, to image.Point
	start    time.Time
	dur      time.Duration
}

type boardScope struct {
	scope board.Scope
	label string
	n     int
	btn   widget.Clickable
}

// boardCard is one card's widgets.
type boardCard struct {
	card          widget.Clickable
	allow, deny   widget.Clickable
	choices       choices
	open, act, ex widget.Clickable
}

// BoardFocus gives the board the keyboard on its next frame.
func (s *Sidebar) BoardFocus() { s.bd.focus = true }

// BoardAnswer is allow_prompt's or deny_prompt's event on the board: an
// Answer for the focused card's approval, when the card offers Allow and
// its ask has been on screen for board.Arming at now. ok is false
// otherwise.
func (s *Sidebar) BoardAnswer(allow bool, now time.Time) (Event, bool) {
	c, ok := s.bd.b.All[s.bd.cur]
	if !ok || !s.Answers || c.Held || !s.bd.shown[c.ID] || c.Answers() != board.AnswerAllow || !s.bd.tr.Armed(c.ID, now) {
		return nil, false
	}
	return Answer{PaneID: c.Act.PaneID, At: c.Act.UpdatedAt.UnixNano(), Allow: allow}, true
}

// Board draws the board in gtx's size and returns what the user did.
func (s *Sidebar) Board(gtx layout.Context, th *theme.Theme, st *model.State, o BoardOptions) []Event {
	bd := &s.bd
	if bd.cards == nil {
		bd.cards, bd.last, bd.moves, bd.born = map[string]*boardCard{}, map[string]cardPlace{}, map[string]cardMove{}, map[string]time.Time{}
		if s.epoch.IsZero() {
			s.epoch = gtx.Now
		}
	}
	s.events = s.events[:0]
	s.now = gtx.Now
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	// why: the key focus tag's area goes under everything, or it would take the cards' clicks.
	for {
		if _, ok := gtx.Event(key.FocusFilter{Target: &bd.tag}); !ok {
			break
		}
	}
	event.Op(gtx.Ops, &bd.tag)

	bd.b = board.Derive(st, board.Options{Scope: bd.scope, Shells: bd.shells, Hosts: o.Hosts, GuardDeny: o.GuardDeny, Now: gtx.Now})
	held := map[string]bool{}
	for id, c := range bd.cards {
		if c.card.Hovered() || c.allow.Hovered() || c.deny.Hovered() || c.choices.hovered() {
			held[id] = true
		}
	}
	if bd.cur != "" {
		held[bd.cur] = true
	}
	bd.tr.Hold(&bd.b, held, gtx.Now)
	if _, ok := bd.b.All[bd.cur]; !ok && !slices.ContainsFunc(bd.b.Cols[board.Queued], func(c board.Card) bool { return c.ID == bd.cur }) {
		bd.cur = ""
	}
	if at := bd.armsAt(gtx.Now); !at.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: at})
	}

	s.boardKeys(gtx, st)
	pad, top := gtx.Dp(boardPad), gtx.Dp(12)
	headH := gtx.Dp(28)
	s.boardHeader(gtx, th, st, image.Rect(gtx.Dp(16), top, size.X-pad, top+headH))
	chipsY := top + headH + gtx.Dp(theme.SpaceS)
	chipH := gtx.Dp(24)
	footH := gtx.Dp(34)
	bd.area = image.Rect(0, chipsY+chipH+gtx.Dp(10), size.X, size.Y-footH)
	s.boardColumns(gtx, th, st)
	s.boardChips(gtx, th, image.Rect(gtx.Dp(12), chipsY, size.X-pad, chipsY+chipH))
	s.boardFooter(gtx, th, o, image.Rect(gtx.Dp(16), size.Y-footH, size.X-pad, size.Y))
	if bd.open {
		s.scopeMenu(gtx, th, st, o)
	}

	if bd.focus {
		bd.focus = false
		gtx.Execute(key.FocusCmd{Tag: &bd.tag})
	}
	return slices.Clone(s.events)
}

// armsAt is the soonest a shown card's answers start to take clicks.
func (bd *boardState) armsAt(now time.Time) time.Time {
	var out time.Time
	for _, c := range bd.b.Cols[board.NeedsYou] {
		if at := bd.tr.ArmsAt(c.ID, now); !at.IsZero() && (out.IsZero() || at.Before(out)) {
			out = at
		}
	}
	return out
}

// shownCols are the columns the board draws: Shells only while shown.
func (bd *boardState) shownCols() []board.Column {
	var out []board.Column
	for c := range board.Columns {
		if c != board.Shells || bd.shells {
			out = append(out, c)
		}
	}
	return out
}

// boardKeys moves the cursor with the arrow keys, opens its tab with
// Enter, and leaves the scope menu, then the scope, then the board with
// Esc.
func (s *Sidebar) boardKeys(gtx layout.Context, st *model.State) {
	bd := &s.bd
	filters := []event.Filter{key.Filter{Name: key.NameEscape}}
	for _, k := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow, key.NameRightArrow, key.NameReturn, key.NameEnter} {
		filters = append(filters, key.Filter{Focus: &bd.tag, Name: k})
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameEscape:
			switch {
			case bd.open:
				bd.open = false
			case bd.scope != board.Scope{}:
				bd.scope, bd.cur = board.Scope{}, ""
			default:
				s.events = append(s.events, CloseBoard{})
			}
		case key.NameReturn, key.NameEnter:
			if c, ok := bd.b.All[bd.cur]; ok {
				s.events = append(s.events, SelectWorkspace{WorkspaceID: c.ID, PaneID: c.Act.PaneID})
			}
		default:
			bd.step(e.Name)
			bd.reveal = true
		}
	}
}

// step moves the cursor one card up or down its column, or to the
// nearest card of the next column that has any.
func (bd *boardState) step(k key.Name) {
	cols := bd.shownCols()
	ci, ri := -1, 0
	for i, c := range cols {
		if j := slices.IndexFunc(bd.b.Cols[c], func(card board.Card) bool { return card.ID == bd.cur }); j >= 0 {
			ci, ri = i, j
		}
	}
	if ci < 0 {
		for _, c := range cols {
			if len(bd.b.Cols[c]) > 0 {
				bd.cur = bd.b.Cols[c][0].ID
				return
			}
		}
		return
	}
	switch k {
	case key.NameUpArrow, key.NameDownArrow:
		d := 1
		if k == key.NameUpArrow {
			d = -1
		}
		list := bd.b.Cols[cols[ci]]
		bd.cur = list[min(max(ri+d, 0), len(list)-1)].ID
	default:
		d := 1
		if k == key.NameLeftArrow {
			d = -1
		}
		for i := ci + d; i >= 0 && i < len(cols); i += d {
			if list := bd.b.Cols[cols[i]]; len(list) > 0 {
				bd.cur = list[min(ri, len(list)-1)].ID
				return
			}
		}
	}
}

// boardHeader is the title, the scope control, the shells switch and New
// task.
func (s *Sidebar) boardHeader(gtx layout.Context, th *theme.Theme, st *model.State, r image.Rectangle) {
	bd := &s.bd
	for bd.scopeBt.Clicked(gtx) {
		bd.open = !bd.open
		bd.opened = gtx.Now
	}
	for bd.shellBt.Clicked(gtx) {
		bd.shells = !bd.shells
	}
	for bd.newTask.Clicked(gtx) {
		g := ""
		if bd.scope.Kind == board.ScopeProject {
			g = bd.scope.ID
		}
		s.events = append(s.events, NewTaskIn{GroupID: g})
	}
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	g := gtx
	g.Constraints = layout.Exact(r.Size())
	h := r.Dy()
	hrow(g, h, gtx.Dp(theme.SpaceS),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Board")
		}},
		item{ml: gtx.Dp(6), w: func(gtx layout.Context) layout.Dimensions {
			return s.ctlButton(gtx, th, &bd.scopeBt, "Scope", scopeLabel(st, bd.scope), bd.open)
		}},
		item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return s.switchButton(gtx, th, &bd.shellBt, "Show shells", bd.shells)
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return kit.Button(gtx, th, &bd.newTask, kit.Secondary, kit.Medium, "New task")
		}},
	)
}

// scopeLabel names a scope.
func scopeLabel(st *model.State, sc board.Scope) string {
	switch sc.Kind {
	case board.ScopeProject:
		for _, p := range st.Projects {
			if p.ID == sc.ID {
				return p.Name
			}
		}
		return "Group"
	case board.ScopeUngrouped:
		return "Ungrouped"
	case board.ScopeHost:
		return hostName(sc.ID)
	}
	return "All tabs"
}

// ctlButton is a header control: a quiet key, its value and a chevron
// on a ringed surface.
func (s *Sidebar) ctlButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, k, v string, open bool) layout.Dimensions {
	h, px := gtx.Dp(28), gtx.Dp(10)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(260), h)}
	d := hrowFit(g, h, gtx.Dp(6),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, k)
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, v)
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icChevronDown, gtx.Dp(14), th.Muted, 0)
		}},
	)
	call := m.Stop()
	size := image.Pt(d.Size.X+2*px, h)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		fill := th.Surface
		switch {
		case open:
			fill = th.SelectedBg
		case c.Hovered():
			fill = th.SurfaceElevated
		}
		rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl))
		paint.FillShape(gtx.Ops, th.BorderSubtle, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}.Inset(1), gtx.Dp(theme.RadiusControl)-1).Op(gtx.Ops))
		defer op.Offset(image.Pt(px, 0)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
}

// switchButton is a labeled on and off switch.
func (s *Sidebar) switchButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, text string, on bool) layout.Dimensions {
	h := gtx.Dp(28)
	m := op.Record(gtx.Ops)
	col := th.Muted
	if c.Hovered() {
		col = th.Fg
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(200), h)}
	d := hrowFit(g, h, gtx.Dp(7),
		item{w: func(gtx layout.Context) layout.Dimensions {
			w, sh := gtx.Dp(26), gtx.Dp(16)
			track := th.SurfaceElevated
			if on {
				track = th.Primary
			}
			paint.FillShape(gtx.Ops, track, clip.UniformRRect(image.Rect(0, 0, w, sh), sh/2).Op(gtx.Ops))
			k, x := gtx.Dp(12), gtx.Dp(2)
			knob := th.Fg
			if on {
				x, knob = w-k-gtx.Dp(2), th.OnPrimary
			}
			paint.FillShape(gtx.Ops, knob, clip.Ellipse{Min: image.Pt(x, gtx.Dp(2)), Max: image.Pt(x+k, gtx.Dp(2)+k)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(w, sh)}
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), col, text)
		}},
	)
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(12), h)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		defer op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
}

// scopeMenu is the Scope control's menu: every tab, each group, the
// ungrouped tabs and each machine, with how many cards each shows.
func (s *Sidebar) scopeMenu(gtx layout.Context, th *theme.Theme, st *model.State, o BoardOptions) {
	bd := &s.bd
	var want []board.Scope
	want = append(want, board.Scope{})
	for _, p := range st.Projects {
		want = append(want, board.Scope{Kind: board.ScopeProject, ID: p.ID})
	}
	want = append(want, board.Scope{Kind: board.ScopeUngrouped})
	hosts := map[string]bool{}
	for _, h := range o.Hosts {
		hosts[h] = true
	}
	if len(hosts) > 0 {
		hosts[""] = true
		for _, h := range slices.Sorted(func(yield func(string) bool) {
			for h := range hosts {
				if !yield(h) {
					return
				}
			}
		}) {
			want = append(want, board.Scope{Kind: board.ScopeHost, ID: h})
		}
	}
	for len(bd.scopes) < len(want) {
		bd.scopes = append(bd.scopes, boardScope{})
	}
	for i, sc := range want {
		b := board.Derive(st, board.Options{Scope: sc, Hosts: o.Hosts, Now: gtx.Now})
		n := 0
		for c := range b.Cols {
			if board.Column(c) != board.Shells {
				n += len(b.Cols[c])
			}
		}
		bd.scopes[i].scope, bd.scopes[i].label, bd.scopes[i].n = sc, scopeLabel(st, sc), n
	}
	items := bd.scopes[:len(want)]
	for i := range items {
		for items[i].btn.Clicked(gtx) {
			bd.scope, bd.open, bd.cur = items[i].scope, false, ""
			bd.x, bd.colY = 0, [board.Columns]int{}
		}
	}
	rowH, w, pad := gtx.Dp(30), gtx.Dp(240), gtx.Dp(4)
	at := image.Pt(gtx.Dp(16)+gtx.Dp(54), gtx.Dp(12)+gtx.Dp(28)+gtx.Dp(4))
	rect := image.Rectangle{Min: at, Max: at.Add(image.Pt(w, len(items)*rowH+2*pad))}
	// A press outside closes the menu.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &bd.menuTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && !e.Position.Round().In(rect) {
			bd.open = false
		}
	}
	cl := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &bd.menuTag)
	pass.Pop()
	cl.Pop()
	t := anim.At(gtx, bd.opened, anim.Menu)
	defer kit.PopIn(gtx, t, rect, 4)()
	r := gtx.Dp(theme.RadiusPopover)
	kit.Surface(gtx, rect, r, 2, th.BorderStrong, th.SurfaceSecondary)
	for i := range items {
		it := &items[i]
		ir := image.Rect(rect.Min.X+pad, rect.Min.Y+pad+i*rowH, rect.Max.X-pad, rect.Min.Y+pad+(i+1)*rowH)
		off := op.Offset(ir.Min).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(ir.Size())
		clickable(g, &it.btn, func(gtx layout.Context) layout.Dimensions {
			sel := it.scope == bd.scope
			if sel || it.btn.Hovered() {
				fill := th.Hover
				if sel {
					fill = th.SelectedBg
				}
				paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: ir.Size()}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			}
			o := op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops)
			g := gtx
			g.Constraints = layout.Exact(image.Pt(ir.Dx()-gtx.Dp(16), ir.Dy()))
			hrow(g, ir.Dy(), gtx.Dp(8),
				item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, it.label)
				}},
				item{right: true, w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, strconv.Itoa(it.n))
				}},
			)
			o.Pop()
			return layout.Dimensions{Size: ir.Size()}
		})
		off.Pop()
	}
}

// columnStyle is each column's accent and icon.
func columnStyle(th *theme.Theme, c board.Column) (color.NRGBA, string) {
	switch c {
	case board.NeedsYou:
		return th.Yellow, icCircleAlert
	case board.Failed:
		return th.Red, icCircleX
	case board.Working:
		return th.Blue, icLoader
	case board.PROpen:
		return th.Blue, icGitPullRequest
	case board.Done:
		return th.Green, icCircleCheck
	case board.Queued:
		return th.Muted, icClock
	}
	return th.Muted, icTerminal
}

const icCircleX = "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M15 9l-6 6M9 9l6 6"

// boardChips is the row of column counts. A chip whose column is scrolled
// out of view is outlined, and a click scrolls to its column.
func (s *Sidebar) boardChips(gtx layout.Context, th *theme.Theme, r image.Rectangle) {
	bd := &s.bd
	cols := bd.shownCols()
	for _, c := range cols {
		for bd.chips[c].Clicked(gtx) {
			bd.x = bd.colX[c] - gtx.Dp(boardPad)
		}
	}
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	h := r.Dy()
	var items []item
	for _, c := range cols {
		n := len(bd.b.Cols[c])
		x0 := bd.colX[c] - bd.x
		out := x0 < 0 || x0+bd.colW > bd.area.Dx()
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return s.chip(gtx, th, &bd.chips[c], c, n, out)
		}})
	}
	if bd.scope == (board.Scope{}) {
		items = append(items, item{right: true, opt: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, "Workers fold into their orchestrator unless they need you")
		}})
	}
	g := gtx
	g.Constraints = layout.Exact(r.Size())
	hrow(g, h, gtx.Dp(4), items...)
}

func (s *Sidebar) chip(gtx layout.Context, th *theme.Theme, c *widget.Clickable, col board.Column, n int, out bool) layout.Dimensions {
	h, px := gtx.Dp(24), gtx.Dp(9)
	acc, ic := columnStyle(th, col)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(200), h)}
	nCol, nFont := th.Fg, semibold(th.UIFont)
	if n == 0 {
		nCol, nFont = th.TextQuiet, medium(th.UIFont)
	}
	lab := th.Muted
	if c.Hovered() {
		lab = th.Fg
	}
	d := hrowFit(g, h, gtx.Dp(6),
		item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, gtx.Dp(12), acc, 0) }},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), lab, board.Names[col])
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, nFont, th.Sp(theme.Small), nCol, strconv.Itoa(n))
		}},
	)
	call := m.Stop()
	size := image.Pt(d.Size.X+2*px, h)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rectangle{Max: size}, h/2)
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.Surface, rr.Op(gtx.Ops))
		}
		if out {
			paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		}
		defer op.Offset(image.Pt(px, 0)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
}

// boardColumns lays out the columns side by side, at least boardColMin
// wide, scrolling sideways when they do not fit, each scrolling down on
// its own. Cards that change column travel there.
func (s *Sidebar) boardColumns(gtx layout.Context, th *theme.Theme, st *model.State) {
	bd := &s.bd
	area := bd.area
	cols := bd.shownCols()
	pad, gap := gtx.Dp(boardPad), gtx.Dp(boardGap)
	avail := area.Dx() - 2*pad
	bd.colW = max(gtx.Dp(boardColMin), (avail-gap*(len(cols)-1))/len(cols))
	content := len(cols)*bd.colW + gap*(len(cols)-1)
	maxX := max(content-avail, 0)
	s.boardScroll(gtx, area, maxX)
	bd.x = min(max(bd.x, 0), maxX)
	for i, c := range cols {
		bd.colX[c] = pad + i*(bd.colW+gap)
	}
	if c, ok := bd.b.All[bd.cur]; ok && bd.reveal {
		s.revealColumn(gtx, c.Column(), avail)
	}

	defer op.Offset(area.Min).Push(gtx.Ops).Pop()
	defer clip.Rect{Max: area.Size()}.Push(gtx.Ops).Pop()
	headH := gtx.Dp(40)
	h := area.Dy() - gtx.Dp(boardGap)
	type flying struct {
		call op.CallOp
		at   image.Point
		size image.Point
	}
	var over []flying
	next := map[string]cardPlace{}
	bd.shown = map[string]bool{}
	first := len(bd.last) == 0
	for _, c := range cols {
		x := bd.colX[c] - bd.x
		cr := image.Rect(x, 0, x+bd.colW, h)
		r := gtx.Dp(theme.RadiusCard)
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.UniformRRect(cr, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(cr.Inset(1), r-1).Op(gtx.Ops))
		s.columnHead(gtx, th, c, image.Rect(x, 0, x+bd.colW, headH))
		body := image.Rect(x+gtx.Dp(8), headH, x+bd.colW-gtx.Dp(8), h-gtx.Dp(8))
		list := bd.b.Cols[c]
		cg := gtx
		cg.Constraints = layout.Constraints{Min: image.Pt(body.Dx(), 0), Max: image.Pt(body.Dx(), 1<<20)}
		y := 0
		visible := x+bd.colW > 0 && x < area.Dx()
		cl := clip.Rect(body).Push(gtx.Ops)
		if len(list) == 0 {
			o := op.Offset(body.Min.Add(image.Pt(gtx.Dp(6), gtx.Dp(6)))).Push(gtx.Ops)
			eg := gtx
			eg.Constraints = layout.Constraints{Max: image.Pt(body.Dx()-gtx.Dp(12), body.Dy())}
			emptyText := columnEmpty[c]
			if bd.scope != (board.Scope{}) && c != board.Queued {
				emptyText = "None in this scope. " + emptyText
			}
			para(eg, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, emptyText, 4)
			o.Pop()
		}
		seen := false
		for i, card := range list {
			if c == board.Done && !seen && i > 0 && !card.Act.Unseen && list[0].Act.Unseen {
				seen = true
				y += s.seenDivider(gtx, th, body.Min.Add(image.Pt(0, y-bd.colY[c])), body.Dx())
			}
			m := op.Record(gtx.Ops)
			d := s.boardCard(cg, th, st, card)
			call := m.Stop()
			if card.ID == bd.cur && bd.reveal {
				if y < bd.colY[c] {
					bd.colY[c] = y
				} else if y+d.Size.Y > bd.colY[c]+body.Dy() {
					bd.colY[c] = y + d.Size.Y - body.Dy()
				}
				gtx.Execute(op.InvalidateCmd{})
			}
			at := image.Pt(bd.colX[c]+gtx.Dp(8), headH+y)
			next[card.ID] = cardPlace{at, c}
			screen := at.Sub(image.Pt(bd.x, bd.colY[c]))
			from, moving := bd.motion(gtx, card.ID, cardPlace{at, c}, first || card.Held || !visible)
			alpha := float32(1)
			if b, ok := bd.born[card.ID]; ok {
				t, run := anim.Progress(b, gtx.Now, anim.State)
				if run {
					gtx.Execute(op.InvalidateCmd{})
				} else {
					delete(bd.born, card.ID)
				}
				alpha = t
			}
			whole := image.Rectangle{Min: screen, Max: screen.Add(d.Size)}
			bd.shown[card.ID] = !moving && whole.In(body) && body.Min.X >= 0 && body.Max.X <= area.Dx()
			if moving && from.col != c {
				over = append(over, flying{call, from.at.Sub(image.Pt(bd.x, bd.colY[c])), d.Size})
			} else {
				p := screen
				if moving {
					p = from.at.Sub(image.Pt(bd.x, bd.colY[c]))
				}
				o := op.Offset(p).Push(gtx.Ops)
				fo := paint.PushOpacity(gtx.Ops, alpha)
				call.Add(gtx.Ops)
				fo.Pop()
				o.Pop()
			}
			y += d.Size.Y + gtx.Dp(cardGap)
		}
		cl.Pop()
		bd.maxY[c] = max(y-body.Dy(), 0)
		bd.colY[c] = min(max(bd.colY[c], 0), bd.maxY[c])
	}
	for _, f := range over {
		o := op.Offset(f.at).Push(gtx.Ops)
		f.call.Add(gtx.Ops)
		o.Pop()
	}
	for id := range bd.last {
		if _, ok := next[id]; !ok {
			delete(bd.moves, id)
		}
	}
	bd.last = next
	bd.reveal = false
	s.edgeFades(gtx, th, area.Size(), maxX)
}

// motion is where card id draws this frame on its way to p, and whether
// it is on its way: a card that changed column travels boardMove, one
// that moved in its column slides anim.Slide, and a new card fades in.
// still skips the motion. Under reduce_motion a moved card fades in at
// its new place.
func (bd *boardState) motion(gtx layout.Context, id string, p cardPlace, still bool) (cardPlace, bool) {
	prev, ok := bd.last[id]
	if !ok {
		if !still {
			bd.born[id] = gtx.Now
		}
		return p, false
	}
	if prev.at != p.at && !still {
		if anim.Reduced() {
			if prev.col != p.col {
				bd.born[id] = gtx.Now
			}
		} else {
			from := prev.at
			if m, ok := bd.moves[id]; ok {
				from = m.to // a new change finishes the one in flight
			}
			dur := anim.Slide
			if prev.col != p.col {
				dur = boardMove
			}
			bd.moves[id] = cardMove{from: from, to: p.at, start: gtx.Now, dur: dur}
		}
	}
	m, ok := bd.moves[id]
	if !ok || m.to != p.at {
		delete(bd.moves, id)
		return p, false
	}
	el := gtx.Now.Sub(m.start)
	if el >= m.dur {
		delete(bd.moves, id)
		return p, false
	}
	gtx.Execute(op.InvalidateCmd{})
	t := easeInOut(float32(el) / float32(m.dur))
	at := image.Pt(m.from.X+int(float32(m.to.X-m.from.X)*t), m.from.Y+int(float32(m.to.Y-m.from.Y)*t))
	return cardPlace{at, prev.col}, true
}

// easeInOut is a strong ease-in-out, cubic-bezier(0.77, 0, 0.175, 1)
// approximated by a quintic.
func easeInOut(t float32) float32 {
	t = min(max(t, 0), 1)
	if t < 0.5 {
		return 16 * t * t * t * t * t
	}
	u := 2*t - 2
	return 1 + u*u*u*u*u/2
}

var columnEmpty = [board.Columns]string{
	"Nothing waits on you. Approvals, questions and plans land here, oldest first.",
	"No failed turns and no usage limits.",
	"No agent is in a turn right now.",
	"No branch here has an open pull request.",
	"No finished turns. Settled tabs stay in the inbox.",
	"No queued tasks. New task, then Queue, adds one.",
	"No plain shells in this scope.",
}

// revealColumn scrolls sideways so column c shows whole.
func (s *Sidebar) revealColumn(gtx layout.Context, c board.Column, avail int) {
	bd := &s.bd
	pad := gtx.Dp(boardPad)
	if x := bd.colX[c] - pad; x < bd.x {
		bd.x = x
	} else if x+bd.colW > bd.x+avail {
		bd.x = x + bd.colW - avail
	}
}

// boardScroll scrolls the columns sideways with a sideways wheel or Shift
// held, and the column under the pointer down otherwise.
func (s *Sidebar) boardScroll(gtx layout.Context, area image.Rectangle, maxX int) {
	bd := &s.bd
	big := pointer.ScrollRange{Min: -1e6, Max: 1e6}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &bd.scrollT, Kinds: pointer.Scroll, ScrollX: big, ScrollY: big})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		dx, dy := e.Scroll.X, e.Scroll.Y
		if e.Modifiers.Contain(key.ModShift) && dx == 0 {
			dx, dy = dy, 0
		}
		bd.x += int(dx)
		if dy != 0 {
			px := int(e.Position.X) - area.Min.X + bd.x
			for c := range board.Columns {
				if px >= bd.colX[c] && px < bd.colX[c]+bd.colW {
					bd.colY[c] = min(max(bd.colY[c]+int(dy), 0), bd.maxY[c])
				}
			}
		}
	}
	defer clip.Rect(area).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &bd.scrollT)
}

// edgeFades shade the sides where more columns are scrolled out of view.
func (s *Sidebar) edgeFades(gtx layout.Context, th *theme.Theme, size image.Point, maxX int) {
	w := float32(gtx.Dp(40))
	clear := th.Bg
	clear.A = 0
	fade := func(x0, x1 float32, c0, c1 color.NRGBA) {
		r := image.Rect(int(min(x0, x1)), 0, int(max(x0, x1)), size.Y)
		defer clip.Rect(r).Push(gtx.Ops).Pop()
		paint.LinearGradientOp{Stop1: f32.Pt(x0, 0), Color1: c0, Stop2: f32.Pt(x1, 0), Color2: c1}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}
	if s.bd.x > 0 {
		fade(0, w, th.Bg, clear)
	}
	if s.bd.x < maxX {
		fade(float32(size.X), float32(size.X)-w, th.Bg, clear)
	}
}

// columnHead is a column's icon, name, count and summary.
func (s *Sidebar) columnHead(gtx layout.Context, th *theme.Theme, c board.Column, r image.Rectangle) {
	acc, ic := columnStyle(th, c)
	list := s.bd.b.Cols[c]
	sub := ""
	switch c {
	case board.NeedsYou:
		sub = needsBreakdown(list)
	case board.Failed:
		sub = "and usage limits"
	}
	defer op.Offset(r.Min.Add(image.Pt(gtx.Dp(12), gtx.Dp(2)))).Push(gtx.Ops).Pop()
	g := gtx
	g.Constraints = layout.Exact(image.Pt(r.Dx()-gtx.Dp(24), r.Dy()))
	hrow(g, r.Dy(), gtx.Dp(7),
		item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, ic, gtx.Dp(14), acc, 0) }},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, board.Names[c])
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, strconv.Itoa(len(list)))
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, sub)
		}},
	)
}

// needsBreakdown is "4 approvals, 3 questions, 1 plan".
func needsBreakdown(list []board.Card) string {
	var n [3]int
	for _, c := range list {
		switch c.Kind {
		case board.KindApproval:
			n[0]++
		case board.KindInput:
			n[1]++
		case board.KindPlan:
			n[2]++
		}
	}
	var out []string
	for i, w := range []string{"approval", "question", "plan"} {
		if n[i] == 1 {
			out = append(out, "1 "+w)
		} else if n[i] > 1 {
			out = append(out, fmt.Sprintf("%d %ss", n[i], w))
		}
	}
	return strings.Join(out, ", ")
}

// seenDivider is the line above the Done cards the user has seen, drawn
// at p; it returns its height.
func (s *Sidebar) seenDivider(gtx layout.Context, th *theme.Theme, p image.Point, w int) int {
	h := gtx.Dp(22)
	o := op.Offset(p.Add(image.Pt(gtx.Dp(4), 0))).Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w-gtx.Dp(8), h))
	hrow(g, h, gtx.Dp(8),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), th.TextQuiet, "Seen")
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 1)}
		}},
	)
	o.Pop()
	return h + gtx.Dp(cardGap)
}

// boardFooter is what the board leaves out, and the keys.
func (s *Sidebar) boardFooter(gtx layout.Context, th *theme.Theme, o BoardOptions, r image.Rectangle) {
	bd := &s.bd
	for i := range bd.foot {
		for bd.foot[i].Clicked(gtx) {
			if i == 2 {
				bd.shells = true
			} else {
				s.events = append(s.events, SetView{Inbox: true})
			}
		}
	}
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, r.Min.Y), Max: image.Pt(gtx.Constraints.Max.X, r.Min.Y+1)}.Op())
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	h := r.Dy()
	var items []item
	link := func(i, n int, what string) {
		if n == 0 {
			return
		}
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return kit.Button(gtx, th, &bd.foot[i], kit.Ghost, kit.Small, fmt.Sprintf("%d %s", n, what))
		}})
	}
	link(0, bd.b.Snoozed, "snoozed")
	link(1, bd.b.Settled, "settled")
	if !bd.shells {
		what := "shells hidden"
		if bd.b.HiddenShells == 1 {
			what = "shell hidden"
		}
		link(2, bd.b.HiddenShells, what)
	}
	hint := func(keys, what string, right bool) {
		if keys == "" {
			return
		}
		items = append(items, item{right: right, opt: true, w: func(gtx layout.Context) layout.Dimensions {
			return hrowFit(gtx, h, gtx.Dp(6),
				item{w: func(gtx layout.Context) layout.Dimensions { return kbd(gtx, th, keys) }},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, what)
				}})
		}})
	}
	answer := strings.TrimSpace(o.AllowKey + " " + o.DenyKey)
	hint("Enter", "open tab", true)
	hint(answer, "allow, deny", false)
	hint("Esc", "back to panes", false)
	g := gtx
	g.Constraints = layout.Exact(r.Size())
	hrow(g, h, gtx.Dp(10), items...)
}

// kbd is a key's name in a small ringed box.
func kbd(gtx layout.Context, th *theme.Theme, keys string) layout.Dimensions {
	m := op.Record(gtx.Ops)
	d := label(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.Fg, keys)
	call := m.Stop()
	px := gtx.Dp(5)
	size := image.Pt(d.Size.X+2*px, d.Size.Y+gtx.Dp(2))
	rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(4))
	paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	o := op.Offset(image.Pt(px, gtx.Dp(1))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: size}
}

// para draws txt wrapped to gtx's width, at most lines lines.
func para(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, txt string, lines int) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{MaxLines: lines}.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
}

// cmdPara is para for a command in the mono face.
func cmdPara(gtx layout.Context, th *theme.Theme, c color.NRGBA, txt string, lines int) layout.Dimensions {
	return para(gtx, th, th.MonoFont, th.Sp(theme.Small), c, txt, lines)
}

// cardBg is a card's fill: lighter than its column in either theme.
func cardBg(th *theme.Theme) color.NRGBA {
	if theme.Contrast(th.Bg, color.NRGBA{A: 255}) > theme.Contrast(th.Bg, color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		return th.Bg // a light theme
	}
	return th.SurfaceSecondary
}

// kindColor is the accent of a card's state.
func kindColor(th *theme.Theme, k board.Kind) color.NRGBA {
	switch k {
	case board.KindApproval, board.KindInput:
		return th.Yellow
	case board.KindPlan:
		return th.Purple
	case board.KindError:
		return th.Red
	case board.KindWorking, board.KindWatching, board.KindPR:
		return th.Blue
	case board.KindDone:
		return th.Green
	case board.KindRunning:
		return StateColor(th, model.StateTerminalRunning)
	}
	return th.Muted
}
