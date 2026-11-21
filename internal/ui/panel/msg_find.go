package panel

import (
	"image"
	"strconv"
	"strings"
	"time"

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

// FindBar is a find bar: its query field, keys and buttons, and how it
// draws at the top right of an area. The chat, the Changes panel and the
// review view each keep one.
type FindBar struct {
	// EnterUp makes Enter go to the match before, up, and Shift+Enter to
	// the one after, for a list whose first match is its newest, at its
	// end; else Enter goes down.
	EnterUp bool

	on, focus bool
	ed        widget.Editor
	prev      widget.Clickable
	next      widget.Clickable
	shut      widget.Clickable
	bg        int // the bar's own area
}

// Open opens the bar, its query selected, or focuses it again.
func (b *FindBar) Open() {
	if !b.on {
		b.on = true
		b.ed.SingleLine, b.ed.Submit = true, true
	}
	b.focus = true
	b.ed.SetCaret(b.ed.Len(), 0)
}

// On reports whether the bar is open.
func (b *FindBar) On() bool { return b.on }

// Close closes the bar; its query stays for the next Open.
func (b *FindBar) Close() { b.on = false }

// Query is the bar's query, trimmed and folded as session.Fold folds;
// empty for none.
func (b *FindBar) Query() []rune { return session.Fold(strings.TrimSpace(b.ed.Text())) }

// SetQuery types q into the bar.
func (b *FindBar) SetQuery(q string) { b.ed.SetText(q) }

// Update takes the bar's keys and buttons: the matches to move by, down
// positive, and whether Escape or Close shut the bar, which gives the
// keys back.
func (b *FindBar) Update(gtx layout.Context) (step int, shut bool) {
	if !b.on {
		return 0, false
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &b.ed, Name: key.NameEscape},
			key.Filter{Focus: &b.ed, Name: key.NameReturn, Optional: key.ModShift},
			key.Filter{Focus: &b.ed, Name: key.NameEnter, Optional: key.ModShift},
			key.Filter{Focus: &b.ed, Name: key.NameUpArrow},
			key.Filter{Focus: &b.ed, Name: key.NameDownArrow},
			pointer.Filter{Target: &b.bg, Kinds: pointer.Press},
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
			b.on = false
			gtx.Execute(key.FocusCmd{Tag: nil})
			return 0, true
		case e.Name == key.NameDownArrow:
			step++
		case e.Name == key.NameUpArrow:
			step--
		case e.Modifiers.Contain(key.ModShift) != b.EnterUp:
			step--
		default:
			step++
		}
	}
	for b.prev.Clicked(gtx) {
		step--
	}
	for b.next.Clicked(gtx) {
		step++
	}
	for b.shut.Clicked(gtx) {
		b.on = false
		return 0, true
	}
	for {
		if _, ok := b.ed.Update(gtx); !ok {
			break
		}
	}
	return step, false
}

// findWidth is the find bar's width at most.
const findWidth = 360

// Layout draws the bar at the top right of an area of size: the query,
// placeholder while it is empty, count, previous, next and close.
func (b *FindBar) Layout(gtx layout.Context, th *theme.Theme, size image.Point, placeholder, count string) {
	if !b.on {
		return
	}
	d := &cdraw{th: th}
	m := gtx.Dp(theme.SpaceM)
	w := min(gtx.Dp(findWidth), size.X-2*m)
	h := gtx.Dp(hit) + 2*gtx.Dp(theme.SpaceXS)
	rect := image.Rect(size.X-m-w, m, size.X-m, m+h)
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	area := clip.Rect(rect).Push(gtx.Ops)
	pointer.CursorDefault.Add(gtx.Ops)
	gioevent.Op(gtx.Ops, &b.bg)
	area.Pop()
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	pad, bs := gtx.Dp(theme.SpaceS), gtx.Dp(hit)
	isz := gtx.Dp(theme.SpaceM + theme.Space2XS)
	y := (h - bs) / 2
	at(gtx, image.Pt(pad, (h-isz)/2), func(gtx layout.Context) layout.Dimensions {
		return sidebar.IconPath(gtx, icSearch, isz, th.Muted)
	})
	prevTip, nextTip := "Previous (Shift+Enter)", "Next (Enter)"
	if b.EnterUp {
		prevTip, nextTip = "Previous (Enter)", "Next (Shift+Enter)"
	}
	// The buttons from the right: close, next, previous, then the count.
	x := w - gtx.Dp(theme.SpaceXS)
	for _, bt := range []struct {
		c       *widget.Clickable
		ic, tip string
	}{{&b.shut, icClose, "Close (Esc)"}, {&b.next, icChevDn, nextTip}, {&b.prev, icChevUp, prevTip}} {
		x -= bs
		r := image.Rect(x, y, x+bs, y+bs)
		at(gtx, r.Min, func(gtx layout.Context) layout.Dimensions {
			return d.toolButton(gtx, bt.c, bt.ic, th.Muted, false)
		})
		if bt.c.Hovered() {
			defer d.tooltip(gtx, bt.tip, r.Add(rect.Min), size)
		}
	}
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
		if b.ed.Len() == 0 {
			m := op.Record(gtx.Ops)
			widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), placeholder, material(gtx, th.Muted))
			m.Stop().Add(gtx.Ops)
		}
		return b.ed.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), material(gtx, th.Fg), material(gtx, d.selColor()))
	})
	at(gtx, image.Pt(ex, (h-ed.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return ed })
	// A focus asked for before the field was laid out is lost: ask until it holds.
	if b.focus && gtx.Focused(&b.ed) {
		b.focus = false
	} else if b.focus {
		gtx.Execute(key.FocusCmd{Tag: &b.ed})
		gtx.Execute(op.InvalidateCmd{})
	}
}

// Height is the bar's height with its margin above, which a view keeps a
// match it scrolls to under.
func (b *FindBar) Height(gtx layout.Context) int {
	return gtx.Dp(theme.SpaceM+theme.SpaceS) + gtx.Dp(hit) + 2*gtx.Dp(theme.SpaceXS)
}

// Mark paints the matches of q, folded, in a line of mono text drawn at
// x, y, cw a rune along and lh high: the current one, at rune cur, in the
// accent, the others quieter; cur -1 for none.
func Mark(gtx layout.Context, th *theme.Theme, text string, q []rune, cur, x, y, cw, lh int) {
	if len(q) == 0 {
		return
	}
	other, now := theme.Mix(th.Bg, th.Yellow, 0.28), theme.Mix(th.Bg, th.Yellow, 0.62)
	for _, a := range session.FindRunes(session.Fold(text), q) {
		c := other
		if a == cur {
			c = now
		}
		r := image.Rect(x+a*cw, y, x+(a+len(q))*cw, y+lh)
		paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, gtx.Dp(theme.Space2XS)).Op(gtx.Ops))
	}
}

// finder is the chat's find bar. It finds a query in the prompts,
// replies, thinking and plans (session.FindText). With a daemon that
// finds (Transcript.Find), the daemon searches the whole chat and a match
// in history not read yet reads older pages until it is; with an older
// one it searches what the window holds while it reads the history back
// to its start. Matches count top to bottom; the current one starts at
// the newest, and Enter walks up.
type finder struct {
	FindBar
	q      []rune // the query, folded; nil for none
	hits   []findHit
	cur    int // the current hit; -1 for none
	sig    string
	counts map[string]itemHits // per item, its hits as last counted, for countQ
	countQ string
	jump   bool           // bring the current hit into view
	jumped bool           // the view went to its row; one more frame places it in the row
	drawn  map[string]int // per item, the matches drawn this frame

	// What the daemon found: asked is the query asked last, at askAt,
	// when the chat was askSig; typed is when the query last changed.
	remote       bool
	asked        string
	askSig       string
	askAt, typed time.Time
	waiting      bool
	found        []session.FindHit
	more         bool
	err          string
}

// wrap is match cur moved by step among n, past either end to the other.
func wrap(cur, step, n int) int { return ((cur+step)%n + n) % n }

// findDebounce is how long the query rests before the daemon is asked;
// findRefresh, how often a chat that changed is asked again.
const (
	findDebounce = 150 * time.Millisecond
	findRefresh  = time.Second
)

// findHit is one match: the n-th in item i, at rune at of its len runes;
// i is -1 for an item the window has not read yet.
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

// OpenFind opens a find bar, its query selected, or focuses it again:
// the Changes panel's when the pointer is on it, else the chat's.
func (t *Transcript) OpenFind() {
	if t.side.on && !t.side.file && t.side.find.over {
		t.side.find.Open()
		return
	}
	f := &t.msg.find
	if !f.on {
		f.cur = -1
		f.EnterUp = true
	}
	f.Open()
}

// FindOpen reports whether a find bar is open.
func (t *Transcript) FindOpen() bool { return t.msg.find.on || t.side.find.on }

// Found takes the daemon's matches of query, as Find asked for them: the
// hits by item, more when it left older ones out, err for none. A
// result for another query is stale and dropped.
func (t *Transcript) Found(query string, hits []session.FindHit, more bool, err string) {
	f := &t.msg.find
	if query != f.asked {
		return
	}
	f.found, f.more, f.err, f.waiting, f.sig = hits, more, err, false, ""
}

// close closes the find bar and drops its matches.
func (f *finder) close() {
	f.on, f.q, f.hits, f.cur, f.sig, f.jump = false, nil, nil, -1, "", false
	f.asked, f.found, f.waiting, f.err = "", nil, false, ""
}

// chatSig is what the find counts change with: the chat's first and last
// items and the last's length.
func chatSig(s *session.Snapshot) string {
	n := len(s.Content)
	if n == 0 {
		return ""
	}
	last := &s.Content[n-1]
	return strconv.Itoa(n) + s.Content[0].ID + last.ID + last.UpdatedAt + strconv.Itoa(len(last.Text))
}

// findUpdate takes the bar's keys and buttons, asks the daemon or
// recounts the matches when the query or the chat changed, and without a
// daemon that finds reads older pages while there are some.
func (t *Transcript) findUpdate(gtx layout.Context, s *session.Snapshot) {
	f := &t.msg.find
	clear(f.drawn)
	if !f.on {
		return
	}
	if f.drawn == nil {
		f.drawn = map[string]int{}
	}
	step, shut := f.Update(gtx)
	if shut {
		f.close()
		return
	}
	f.remote = t.Find != nil
	q := f.Query()
	if string(q) != string(f.q) {
		f.q, f.cur, f.sig, f.typed = q, -1, "", gtx.Now
		f.found, f.more, f.err, f.waiting = nil, false, "", len(q) > 0
	}
	if f.remote {
		t.findAsk(gtx, s, step != 0)
	}
	if f.recount(s) && f.cur < 0 && len(f.hits) > 0 {
		f.cur, f.jump = len(f.hits)-1, true
	}
	if step != 0 && len(f.hits) > 0 {
		f.cur, f.jump = wrap(f.cur, step, len(f.hits)), true
	}
	// why: the view may never have scrolled near the top, so nothing asked yet whether older pages exist.
	if !f.remote && len(f.q) > 0 && t.Older != nil {
		t.older = t.Older()
	}
}

// findAsk asks the daemon for the query's matches once it rested
// findDebounce, again when the chat changed since, at most every
// findRefresh, and when retry follows a failed search.
func (t *Transcript) findAsk(gtx layout.Context, s *session.Snapshot, retry bool) {
	f := &t.msg.find
	q, sig := string(f.q), chatSig(s)
	if q == "" {
		return
	}
	switch {
	case q != f.asked:
		if wait := f.typed.Add(findDebounce); gtx.Now.Before(wait) {
			gtx.Execute(op.InvalidateCmd{At: wait})
			return
		}
	case retry && f.err != "":
	case sig == f.askSig || f.waiting:
		return
	case gtx.Now.Before(f.askAt.Add(findRefresh)):
		gtx.Execute(op.InvalidateCmd{At: f.askAt.Add(findRefresh)})
		return
	}
	f.asked, f.askSig, f.askAt, f.waiting, f.err = q, sig, gtx.Now, true, ""
	t.Find(q)
}

// recount finds the query in s again when s, the query or the daemon's
// matches changed since the last count, and reports whether it did. The
// current hit stays on its item and match.
func (f *finder) recount(s *session.Snapshot) bool {
	sig := string(f.q) + "\x00" + chatSig(s)
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
	add := func(id string, i int, at []int, runes int) {
		for n, a := range at {
			if keep.item == id && keep.n == n {
				f.cur = len(f.hits)
			}
			f.hits = append(f.hits, findHit{item: id, i: i, n: n, at: a, runs: runes})
		}
	}
	if f.remote {
		idx := make(map[string]int, len(s.Content))
		for i := range s.Content {
			idx[s.Content[i].ID] = i
		}
		for _, h := range f.found {
			i, ok := idx[h.Item]
			if !ok {
				i = -1
			}
			add(h.Item, i, h.At, h.Runes)
		}
	} else {
		if f.counts == nil || f.countQ != string(f.q) {
			f.counts, f.countQ = map[string]itemHits{}, string(f.q)
		}
		seen := map[string]bool{}
		for i := range s.Content {
			it := &s.Content[i]
			txt := session.FindText(it)
			if txt == "" {
				continue
			}
			seen[it.ID] = true
			c, ok := f.counts[it.ID]
			if !ok || c.size != len(txt) || c.updated != it.UpdatedAt {
				folded := session.Fold(txt)
				c = itemHits{size: len(txt), updated: it.UpdatedAt, at: session.FindRunes(folded, f.q), runes: len(folded)}
				f.counts[it.ID] = c
			}
			add(it.ID, i, c.at, c.runes)
		}
		for id := range f.counts {
			if !seen[id] {
				delete(f.counts, id)
			}
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
	if f.remote {
		more = f.more
		switch {
		case len(f.q) == 0:
			return ""
		case f.err != "":
			return "Search failed"
		case len(f.hits) == 0 && f.waiting:
			return "Searching…"
		}
	}
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

// unfold opens what hides content item i: its turn's work, its own fold,
// a thought's or a call's, and the <details> in its text. It reports
// whether it opened any, which the next frame's rows show.
func (t *Transcript) unfold(s *session.Snapshot, i int) bool {
	opened := false
	open := func(id string, def bool) {
		if !isOpen(t.open, id, def) {
			t.open[id], opened = true, true
		}
	}
	for _, b := range t.blocks {
		if b.kind == blockTurn && b.lo <= i && i < b.hi {
			open(turnID(s, b), true)
		}
	}
	it := &s.Content[i]
	if it.Kind != session.KindUserPrompt && it.Kind != session.KindAssistantText {
		open(it.ID, false)
	}
	if r := t.docs[it.ID]; r != nil {
		for _, b := range r.blocks {
			if b.Summary {
				open(it.ID+"/fold/"+strconv.Itoa(b.Fold), b.Open)
			}
		}
	}
	return opened
}

// findRow is the row that draws content item i, opening what hides it;
// -1 while no row draws it.
func (t *Transcript) findRow(s *session.Snapshot, i int) int {
	if t.unfold(s, i) {
		return -1 // the next frame's rows hold it
	}
	for k, r := range t.rows {
		if rowItem(r) == i && r.tail == nil && r.res == nil {
			return k
		}
		if r.seg == segHead && !r.open && r.b.lo <= i && i < r.b.hi {
			t.open[logID(s, r.b)] = true
			return -1
		}
	}
	return -1
}

// findJump brings the current hit's row into view, about a third down
// the view of height h, near its match once the row was laid out. A hit
// in history not read yet reads the page before until it is.
func (t *Transcript) findJump(gtx layout.Context, s *session.Snapshot, h int) {
	f := &t.msg.find
	if !f.on || !f.jump || f.cur < 0 || f.cur >= len(f.hits) {
		return
	}
	cur := f.hits[f.cur]
	if cur.i < 0 {
		if t.Older == nil {
			f.jump = false
			return
		}
		if t.older = t.Older(); !t.older {
			f.jump = false // no page holds it
			return
		}
		gtx.Execute(op.InvalidateCmd{})
		return
	}
	k := t.findRow(s, cur.i)
	gtx.Execute(op.InvalidateCmd{})
	if k < 0 {
		return
	}
	el := k + 1
	// A short row goes just under the find bar, which would cover it at the top.
	off := -f.Height(gtx)
	if sp, ok := t.msg.ys[el]; ok && sp.h > h/2 {
		off = min(max(0, sp.h*cur.at/max(1, cur.runs)-h/3), max(0, sp.h-h/2))
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
	key := t.msg.keys[sl]
	if strings.HasPrefix(key, "box:side:") {
		t.sideMarks(gtx, d, sl, key, txt)
	}
	f := &t.msg.find
	if !f.on || len(f.q) == 0 {
		return
	}
	item, _, _ := strings.Cut(key, "/")
	at := session.FindRunes(session.Fold(txt), f.q)
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

// drawFind draws the chat's find bar at the transcript area's top right.
func (t *Transcript) drawFind(gtx layout.Context, d *cdraw, size image.Point) {
	t.msg.find.Layout(gtx, d.th, size, "Find in chat", t.msg.find.counted(t.older))
}
