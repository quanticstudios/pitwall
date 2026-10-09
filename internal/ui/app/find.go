package app

import (
	"fmt"
	"image"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/settings"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Finder is optionally implemented by a Backend: the reply to the pane's
// last proto.Search, and the line its last frame's top row shows
// (proto.Frame's ScrollPushed minus ScrollOffset).
type Finder interface {
	Found(pane string) (r proto.SearchResult, top uint64)
}

// findBar is the find bar over the focused pane. The daemon searches, since
// the history lives there; the bar keeps its reply and which match is
// current. The current match counts back from the newest, so Enter walks
// up into history.
type findBar struct {
	pane string // the pane it searches; "" while closed
	ed   widget.Editor
	sent string             // the query of the last proto.Search
	res  proto.SearchResult // the reply to sent, once have is set
	have bool
	cur  int      // the current match, 0 being the newest
	at   vt.Match // the current match, where the next query starts looking
	atOK bool
	jump bool // scroll the current match into view
	keep bool // the pane is in copy mode: Esc leaves its view where it is
}

// current is the current match, if there is one.
func (f *findBar) current() (vt.Match, bool) {
	n := len(f.res.Matches)
	if !f.have || n == 0 {
		return vt.Match{}, false
	}
	return f.res.Matches[n-1-f.cur], true
}

// step moves d matches back into history (forward when d < 0), wrapping
// around at either end.
func (f *findBar) step(d int) {
	n := len(f.res.Matches)
	if !f.have || n == 0 {
		return
	}
	f.cur = ((f.cur+d)%n + n) % n
	f.at, f.atOK = f.current()
	f.jump = true
}

// take adopts r, the reply to the query sent. The current match is the
// newest one at or above the last current match, so a longer query stays
// near where the shorter one was; else the newest.
func (f *findBar) take(r proto.SearchResult) {
	f.res, f.have, f.cur = r, true, 0
	n := len(r.Matches)
	for i := range n {
		m := r.Matches[n-1-i]
		if !f.atOK || m.Line < f.at.Line || m.Line == f.at.Line && m.Col <= f.at.Col {
			f.cur = i
			break
		}
	}
	if n > 0 {
		f.at, f.atOK = f.current()
		f.jump = true
	}
}

// label is the bar's count: "3 of 17", "No matches", or "" before a reply.
func (f *findBar) label() string {
	switch n := len(f.res.Matches); {
	case !f.have || f.sent == "":
		return ""
	case n == 0:
		return "No matches"
	case f.res.More:
		return fmt.Sprintf("%d of %d+", f.cur+1, n)
	default:
		return fmt.Sprintf("%d of %d", f.cur+1, n)
	}
}

// reveal is the proto.Scroll distance that shows line in a view of rows
// rows whose top row shows line top, scrolled off lines back of at most
// most: 0 when it shows already, else enough to put it mid-view.
func reveal(line, top uint64, off, most, rows int) int {
	y := int(int64(line - top))
	if y >= 0 && y < rows {
		return 0
	}
	return min(max(off+rows/2-y, 0), most) - off
}

// searchOld is the find bar's text while the daemon predates proto.Search,
// which it would drop the connection on: the bar sends nothing.
const searchOld = "Search needs the background service restarted"

// canSearch reports whether the daemon's proto.Level knows proto.Search.
func (u *ui) canSearch() bool { return u.link().Level >= proto.Since(proto.Search{}) }

// openFind opens the find bar on the focused pane, or selects its query
// when it is open there already.
func (u *ui) openFind() {
	id := u.nav.focused()
	if id == "" {
		return
	}
	if u.find.pane != id {
		u.find = findBar{pane: id}
		u.find.ed.SingleLine = true
	}
	u.find.ed.SetCaret(u.find.ed.Len(), 0)
}

// closeFind closes the find bar and, when snap is set, brings its pane back
// to the live screen.
func (u *ui) closeFind(snap bool) {
	if snap {
		u.send(proto.Scroll{Pane: u.find.pane, Lines: -1 << 30})
	}
	u.find = findBar{}
}

// keepFind closes the find bar once its pane loses focus or the settings
// page covers it.
func (u *ui) keepFind(st *model.State) {
	if id := u.find.pane; id != "" && (id != u.nav.focused() || u.settings.Shown()) {
		u.closeFind(findPane(st, id) != nil)
	}
}

// findFrame runs the find bar's keys, sends a changed query, takes the
// daemon's reply and scrolls to the current match. It returns what the
// pane highlights: the query typed so far and the current match's cell in
// g, Y -1 for none.
func (u *ui) findFrame(gtx gl.Context, g *vt.Grid) (string, image.Point) {
	f := &u.find
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &f.ed, Name: key.NameEscape},
			key.Filter{Focus: &f.ed, Name: key.NameReturn, Optional: key.ModShift},
			key.Filter{Focus: &f.ed, Name: key.NameEnter, Optional: key.ModShift},
			key.Filter{Focus: &f.ed, Name: key.NameF3, Optional: key.ModShift},
		)
		if !ok {
			break
		}
		switch e, _ := ev.(key.Event); {
		case e.State != key.Press:
		case e.Name == key.NameEscape:
			u.closeFind(!f.keep)
			gtx.Execute(op.InvalidateCmd{}) // this frame drew the pane without key focus
			return "", image.Pt(0, -1)
		case e.Modifiers&key.ModShift != 0:
			f.step(-1)
		default:
			f.step(1)
		}
	}
	f.ed.ReadOnly = !u.canSearch()
	for {
		if _, ok := f.ed.Update(gtx); !ok {
			break
		}
	}
	if f.ed.ReadOnly {
		return "", image.Pt(0, -1)
	}
	if q := f.ed.Text(); q != f.sent {
		f.sent, f.have, f.res, f.cur = q, false, proto.SearchResult{}, 0
		if q != "" {
			u.send(proto.Search{Pane: f.pane, Query: q})
		}
	}
	fd, ok := u.b.(Finder)
	if !ok {
		return f.sent, image.Pt(0, -1)
	}
	r, top := fd.Found(f.pane)
	if !f.have && f.sent != "" && r.Pane == f.pane && r.Query == f.sent {
		f.take(r)
	}
	m, ok := f.current()
	if !ok {
		return f.sent, image.Pt(0, -1)
	}
	if f.jump && g.Rows > 0 {
		f.jump = false
		off, most := 0, 0
		if s, ok := u.b.(Scroller); ok {
			off, most, _ = s.Scroll(f.pane)
		}
		if d := reveal(m.Line, top, off, most, g.Rows); d != 0 {
			u.send(proto.Scroll{Pane: f.pane, Lines: d})
		}
	}
	if y := int(int64(m.Line - top)); y >= 0 && y < g.Rows {
		return f.sent, image.Pt(m.Col, y)
	}
	return f.sent, image.Pt(0, -1)
}

// drawFind draws the find bar at the top right of area, the pane's
// terminal, and gives it key focus when focus is set.
func (u *ui) drawFind(gtx gl.Context, area image.Rectangle, focus bool) {
	th, f := u.th, &u.find
	if focus && !gtx.Focused(&f.ed) {
		gtx.Execute(key.FocusCmd{Tag: &f.ed})
	}
	in, w := gtx.Dp(8), gtx.Dp(320)
	hint := "Find"
	if f.ed.ReadOnly {
		hint, w = searchOld, gtx.Dp(360)
	}
	w, h := min(w, area.Dx()-2*in), gtx.Dp(32)
	if w < gtx.Dp(120) || area.Dy() < h+2*in {
		return // no room; the keys still work
	}
	r := image.Rect(area.Max.X-in-w, area.Min.Y+in, area.Max.X-in, area.Min.Y+in+h)
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	box := image.Rectangle{Max: r.Size()}
	rr := gtx.Dp(8)
	border := theme.Mix(th.SurfaceElevated, th.Fg, 0.1)
	if gtx.Focused(&f.ed) {
		border = theme.Mix(th.SurfaceElevated, th.Primary, 0.6)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(box, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(box.Inset(1), rr-1).Op(gtx.Ops))

	is := gtx.Dp(14)
	o := op.Offset(image.Pt(gtx.Dp(10), (h-is)/2)).Push(gtx.Ops)
	settings.SearchIcon(gtx, th.Muted, is)
	o.Pop()

	right := w - gtx.Dp(10)
	if s := f.label(); s != "" {
		col := th.Muted
		if s == "No matches" {
			col = th.Red
		}
		call, sz := textCall(gtx, th, th.UIFont, 12, col, s)
		right -= sz.X
		o := op.Offset(image.Pt(right, (h-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		right -= gtx.Dp(10)
	}
	x := gtx.Dp(32)
	eg := gtx
	eg.Constraints = gl.Exact(image.Pt(max(0, right-x), h))
	o = op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
	if f.ed.Len() == 0 {
		gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
			call, sz := textCall(gtx, th, th.UIFont, 13, th.Muted, hint)
			call.Add(gtx.Ops)
			return gl.Dimensions{Size: sz}
		})
	}
	gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return f.ed.Layout(gtx, th.Shaper, th.UIFont, 13, colorCall(gtx, th.Fg), colorCall(gtx, theme.Mix(th.SurfaceElevated, th.Primary, 0.35)))
	})
	o.Pop()
}
