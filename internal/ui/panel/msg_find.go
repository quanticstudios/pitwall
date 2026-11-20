package panel

import (
	"image"
	"strconv"
	"strings"
	"unicode"

	gioevent "gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// finder is the chat's find bar. It finds a query in the prompts and
// Claude's text, older pages too: while it is open with a query it reads
// the chat's history back to its start. Matches count top to bottom;
// the current one starts at the newest, and Enter walks up.
type finder struct {
	on, focus bool
	ed        widget.Editor
	q         []rune // the query, folded; nil for none
	hits      []findHit
	cur       int // the current hit; -1 for none
	sig       string
	counts    map[string]itemHits // per item, its hits as last counted, for countQ
	countQ    string
	jump      bool           // bring the current hit into view
	jumped    bool           // the view went to its row; one more frame places it in the row
	drawn     map[string]int // per item, the matches drawn this frame
	prev      widget.Clickable
	next      widget.Clickable
	shut      widget.Clickable
	bg        int // the bar's own area
}

// findHit is one match: the n-th in item i, at rune at of its len runes.
type findHit struct {
	item     string
	i, n     int
	at, runs int
}

// itemHits is an item's matches, counted at its text's length and time.
type itemHits struct {
	size    int
	updated string
	at      []int // the rune offset of each match
	runes   int
}

// fold is s with each rune lower-cased: as many runes as s, so a match's
// offsets in it are its offsets in s.
func fold(s string) []rune {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return r
}

// findRunes is the rune offset of each match of q in text, both folded,
// left to right, not overlapping.
func findRunes(text, q []rune) []int {
	var out []int
	if len(q) == 0 {
		return nil
	}
	for i := 0; i+len(q) <= len(text); {
		if text[i] == q[0] && string(text[i:i+len(q)]) == string(q) {
			out = append(out, i)
			i += len(q)
			continue
		}
		i++
	}
	return out
}

// searchable is whether the find bar looks in item it: a prompt or
// Claude's text.
func searchable(it *session.Item) bool {
	return it.Kind == session.KindUserPrompt || it.Kind == session.KindAssistantText
}

// OpenFind opens the find bar, its query selected, or focuses it again.
func (t *Transcript) OpenFind() {
	f := &t.msg.find
	if !f.on {
		f.on, f.cur = true, -1
		f.ed.SingleLine, f.ed.Submit = true, true
	}
	f.focus = true
	f.ed.SetCaret(f.ed.Len(), 0)
}

// FindOpen reports whether the find bar is open.
func (t *Transcript) FindOpen() bool { return t.msg.find.on }

// closeFind closes the find bar and drops its matches.
func (f *finder) close() {
	f.on, f.q, f.hits, f.cur, f.sig, f.jump = false, nil, nil, -1, "", false
}

// update takes the bar's keys and buttons, recounts the matches when the
// query or the chat changed, and reads older pages while there are some.
func (t *Transcript) findUpdate(gtx layout.Context, s *session.Snapshot) {
	f := &t.msg.find
	clear(f.drawn)
	if !f.on {
		return
	}
	if f.drawn == nil {
		f.drawn = map[string]int{}
	}
	step := 0
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &f.ed, Name: key.NameEscape},
			key.Filter{Focus: &f.ed, Name: key.NameReturn, Optional: key.ModShift},
			key.Filter{Focus: &f.ed, Name: key.NameEnter, Optional: key.ModShift},
			key.Filter{Focus: &f.ed, Name: key.NameUpArrow},
			key.Filter{Focus: &f.ed, Name: key.NameDownArrow},
			pointer.Filter{Target: &f.bg, Kinds: pointer.Press},
		)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch {
		case e.Name == key.NameEscape:
			f.close()
			gtx.Execute(key.FocusCmd{Tag: nil})
			return
		case e.Name == key.NameDownArrow || e.Modifiers.Contain(key.ModShift):
			step++
		default:
			step--
		}
	}
	for f.prev.Clicked(gtx) {
		step--
	}
	for f.next.Clicked(gtx) {
		step++
	}
	for f.shut.Clicked(gtx) {
		f.close()
		return
	}
	for {
		if _, ok := f.ed.Update(gtx); !ok {
			break
		}
	}
	q := fold(strings.TrimSpace(f.ed.Text()))
	if string(q) != string(f.q) {
		f.q, f.cur, f.sig = q, -1, ""
	}
	if f.recount(s) && f.cur < 0 && len(f.hits) > 0 {
		f.cur, f.jump = len(f.hits)-1, true
	}
	if step != 0 && len(f.hits) > 0 {
		f.cur = ((f.cur+step)%len(f.hits) + len(f.hits)) % len(f.hits)
		f.jump = true
	}
	// why: the view may never have scrolled near the top, so nothing asked yet whether older pages exist.
	if len(f.q) > 0 && t.Older != nil {
		t.older = t.Older()
	}
}

// recount finds the query in s again when s or the query changed since
// the last count, recounting only the items that changed, and reports
// whether it did. The current hit stays on its item and match.
func (f *finder) recount(s *session.Snapshot) bool {
	sig := string(f.q)
	if n := len(s.Content); n > 0 {
		last := &s.Content[n-1]
		sig += "\x00" + strconv.Itoa(n) + s.Content[0].ID + last.ID + last.UpdatedAt + strconv.Itoa(len(last.Text))
	}
	if sig == f.sig {
		return false
	}
	f.sig = sig
	var keep findHit
	if f.cur >= 0 && f.cur < len(f.hits) {
		keep = f.hits[f.cur]
	}
	f.hits = f.hits[:0]
	if len(f.q) == 0 {
		f.cur = -1
		return true
	}
	if f.counts == nil || f.countQ != string(f.q) {
		f.counts, f.countQ = map[string]itemHits{}, string(f.q)
	}
	seen := map[string]bool{}
	for i := range s.Content {
		it := &s.Content[i]
		if !searchable(it) {
			continue
		}
		seen[it.ID] = true
		c, ok := f.counts[it.ID]
		if !ok || c.size != len(it.Text) || c.updated != it.UpdatedAt {
			txt := fold(it.Text)
			c = itemHits{size: len(it.Text), updated: it.UpdatedAt, at: findRunes(txt, f.q), runes: len(txt)}
			f.counts[it.ID] = c
		}
		for n, at := range c.at {
			if keep.item == it.ID && keep.n == n {
				f.cur = len(f.hits)
			}
			f.hits = append(f.hits, findHit{item: it.ID, i: i, n: n, at: at, runs: c.runes})
		}
	}
	for id := range f.counts {
		if !seen[id] {
			delete(f.counts, id)
		}
	}
	if f.cur >= len(f.hits) {
		f.cur = len(f.hits) - 1
	}
	return true
}

// counted is the bar's count: "3 of 12", "No results", "" without a
// query; more says older history is still being read.
func (f *finder) counted(more bool) string {
	switch {
	case len(f.q) == 0:
		return ""
	case len(f.hits) == 0 && more:
		return "Searching…"
	case len(f.hits) == 0:
		return "No results"
	}
	s := strconv.Itoa(f.cur+1) + " of " + strconv.Itoa(len(f.hits))
	if more {
		s += "+"
	}
	return s
}

// findRow is the row that draws content item i, opening the work log it
// is in when it is closed; -1 while no row draws it.
func (t *Transcript) findRow(s *session.Snapshot, i int) int {
	for k, r := range t.rows {
		if rowItem(r) == i && r.tail == nil && r.res == nil {
			return k
		}
		if r.seg == segHead && !r.open && r.b.lo <= i && i < r.b.hi {
			t.open[logID(s, r.b)] = true
			return -1 // the next frame's rows hold it
		}
	}
	return -1
}

// findJump brings the current hit's row into view, about a third down
// the view of height h, near its match once the row was laid out.
func (t *Transcript) findJump(gtx layout.Context, s *session.Snapshot, h int) {
	f := &t.msg.find
	if !f.on || !f.jump || f.cur < 0 || f.cur >= len(f.hits) {
		return
	}
	hit := f.hits[f.cur]
	k := t.findRow(s, hit.i)
	gtx.Execute(op.InvalidateCmd{})
	if k < 0 {
		return
	}
	el := k + 1
	off := 0
	if sp, ok := t.msg.ys[el]; ok && sp.h > h/2 {
		off = min(max(0, sp.h*hit.at/max(1, hit.runs)-h/3), max(0, sp.h-h/2))
	}
	t.list.Position.First, t.list.Position.Offset, t.list.Position.BeforeEnd = el, off, true
	if _, ok := t.msg.ys[el]; ok && f.jumped {
		f.jump, f.jumped = false, false
		return
	}
	f.jumped = true
}

// marks paints the find matches in sl's text txt, laid out as call, under
// it: the current match in the accent, the others quieter.
func (t *Transcript) marks(gtx layout.Context, d *cdraw, sl *widget.Selectable, txt string) {
	f := &t.msg.find
	if !f.on || len(f.q) == 0 {
		return
	}
	key := t.msg.keys[sl]
	item, _, _ := strings.Cut(key, "/")
	at := findRunes(fold(txt), f.q)
	if len(at) == 0 {
		return
	}
	var cur findHit
	if f.cur >= 0 && f.cur < len(f.hits) {
		cur = f.hits[f.cur]
	}
	other := theme.Mix(d.th.Bg, d.th.Yellow, 0.28)
	now := theme.Mix(d.th.Bg, d.th.Yellow, 0.62)
	var rg []widget.Region
	for _, a := range at {
		n := f.drawn[item]
		f.drawn[item] = n + 1
		c := other
		if item != "" && item == cur.item && n == cur.n {
			c = now
		}
		rg = sl.Regions(a, a+len(f.q), rg[:0])
		for _, g := range rg {
			r := g.Bounds.Inset(-gtx.Dp(1))
			paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, gtx.Dp(theme.Space2XS)).Op(gtx.Ops))
		}
	}
}

// findWidth is the find bar's width at most.
const findWidth = 360

// drawFind draws the find bar at the transcript area's top right: the
// query, the count, previous, next and close.
func (t *Transcript) drawFind(gtx layout.Context, d *cdraw, size image.Point) {
	f := &t.msg.find
	if !f.on {
		return
	}
	th := d.th
	m := gtx.Dp(theme.SpaceM)
	w := min(gtx.Dp(findWidth), size.X-2*m)
	h := gtx.Dp(hit) + 2*gtx.Dp(theme.SpaceXS)
	rect := image.Rect(size.X-m-w, m, size.X-m, m+h)
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	area := clip.Rect(rect).Push(gtx.Ops)
	pointer.CursorDefault.Add(gtx.Ops)
	gioevent.Op(gtx.Ops, &f.bg)
	area.Pop()
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	pad, bs := gtx.Dp(theme.SpaceS), gtx.Dp(hit)
	isz := gtx.Dp(theme.SpaceM + theme.Space2XS)
	y := (h - bs) / 2
	at(gtx, image.Pt(pad, (h-isz)/2), func(gtx layout.Context) layout.Dimensions {
		return sidebar.IconPath(gtx, icSearch, isz, th.Muted)
	})
	// The buttons from the right: close, next, previous, then the count.
	x := w - gtx.Dp(theme.SpaceXS)
	for _, b := range []struct {
		c       *widget.Clickable
		ic, tip string
	}{{&f.shut, icClose, "Close (Esc)"}, {&f.next, icChevDn, "Next (Shift+Enter)"}, {&f.prev, icChevUp, "Previous (Enter)"}} {
		x -= bs
		r := image.Rect(x, y, x+bs, y+bs)
		at(gtx, r.Min, func(gtx layout.Context) layout.Dimensions {
			return d.toolButton(gtx, b.c, b.ic, th.Muted, false)
		})
		if b.c.Hovered() {
			defer d.tooltip(gtx, b.tip, r.Add(rect.Min), size)
		}
	}
	count := f.counted(t.older)
	cw := 0
	if count != "" {
		call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, count)
		})
		cw = td.Size.X + gtx.Dp(theme.SpaceS)
		at(gtx, image.Pt(x-cw, (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	}
	ex := pad + isz + gtx.Dp(theme.SpaceS)
	ew := max(0, x-cw-ex-gtx.Dp(theme.SpaceXS))
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(ew, 0), Max: image.Pt(ew, h)}
	call, ed := measure(g, func(gtx layout.Context) layout.Dimensions {
		if f.ed.Len() == 0 {
			m := op.Record(gtx.Ops)
			widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), "Find in chat", material(gtx, th.Muted))
			m.Stop().Add(gtx.Ops)
		}
		return f.ed.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), material(gtx, th.Fg), material(gtx, d.selColor()))
	})
	at(gtx, image.Pt(ex, (h-ed.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return ed })
	// A focus asked for before the field was laid out is lost: ask until it holds.
	if f.focus && gtx.Focused(&f.ed) {
		f.focus = false
	} else if f.focus {
		gtx.Execute(key.FocusCmd{Tag: &f.ed})
		gtx.Execute(op.InvalidateCmd{})
	}
}
