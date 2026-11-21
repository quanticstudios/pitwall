package panel

import (
	"image"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
	gioevent "gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The transcript selects text itself, as a browser does over a page: one
// selection from a press on any block's text to wherever the drag goes,
// across blocks and messages, in reading order. Its blocks only paint it:
// a block's own selectable would take the key focus on a press, and the
// composer takes it back each frame, which is why a selection never
// showed. It needs no key focus, so the composer keeps the typing.

// selPos is a place in the transcript's text: a selectable's key and a
// rune offset in its text.
type selPos struct {
	key string
	off int
}

// textSel is the transcript's selection and what finds and orders its text.
type textSel struct {
	a, b selPos // where it started and where it ends now; a == b is none
	drag bool   // the primary button is held from a press on text
	grab bool   // the drag moved: the transcript holds the pointer
	from f32.Point
	at   f32.Point // the pointer while dragging, in the window
	n    int       // presses in a row, for a double or triple click
	last time.Time
	lpos f32.Point
	won  bool // a press on text started a selection; Began takes it

	pressed *widget.Selectable // the block a press landed on this frame

	menu   bool        // the right-click menu shows
	menuAt image.Point // its corner, in the transcript
	menuR  image.Rectangle
	copyB  widget.Clickable

	rank, seq int // the order key of the row being drawn, and the selectables in it so far
	ord       map[string]int
	size      map[*widget.Selectable]image.Point // as last drawn
	whole     map[*widget.Selectable]bool        // blocks that select whole, see cdraw.whole
	typed     map[string]ctext                   // texts whose chips copy as the tokens they stand for
}

// multiClick is how soon a press after the last makes a double or triple
// click, and slop how far a press moves before it is a drag.
const (
	multiClick = 500 * time.Millisecond
	slop       = 4
)

func (s *textSel) has() bool { return s.a.key != "" && s.a != s.b }

// ClearSelection drops the transcript's selection.
func (t *Transcript) ClearSelection() {
	t.span.a, t.span.b, t.span.drag, t.span.menu = selPos{}, selPos{}, false, false
}

// Began reports, once, that a press on the transcript's text started a
// selection, so the window can clear the composer's: one selection at a
// time, the newest.
func (t *Transcript) Began() bool {
	b := t.span.won
	t.span.won = false
	return b
}

// order is a and b in reading order.
func (s *textSel) order() (selPos, selPos) {
	if s.ord[s.b.key] < s.ord[s.a.key] || s.a.key == s.b.key && s.b.off < s.a.off {
		return s.b, s.a
	}
	return s.a, s.b
}

// holds is whether the selection reaches key's text, so it stays.
func (s *textSel) holds(key string) bool {
	if !s.has() {
		return false
	}
	lo, hi := s.order()
	o, ok := s.ord[key]
	return key == lo.key || key == hi.key || ok && o >= s.ord[lo.key] && o <= s.ord[hi.key]
}

// rangeOf is the runes of key's text, n long, under the selection.
func (s *textSel) rangeOf(key string, n int) (int, int, bool) {
	if !s.has() {
		return 0, 0, false
	}
	o, ok := s.ord[key]
	lo, hi := s.order()
	ol, okl := s.ord[lo.key]
	oh, okh := s.ord[hi.key]
	if !ok || !okl || !okh || o < ol || o > oh {
		return 0, 0, false
	}
	a, b := 0, n
	if key == lo.key {
		a = min(lo.off, n)
	}
	if key == hi.key {
		b = min(hi.off, n)
	}
	return a, b, a < b
}

// press notes a press on sl, a block drawn this frame, for selectEvents.
func (s *textSel) press(gtx layout.Context, sl *widget.Selectable) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: sl, Kinds: pointer.Press})
		if !ok {
			return
		}
		if _, ok := ev.(pointer.Event); ok {
			s.pressed = sl
		}
	}
}

// whole draws w, a block with no selectable text of its own, a table, as
// one piece of the selection by key: any part of the selection over it
// takes all of it, filled in the selection's color under it, and copies
// as txt.
func (d *cdraw) whole(gtx layout.Context, key, txt string, w layout.Widget) layout.Dimensions {
	s := &d.t.span
	sl := d.t.sel(key)
	sl.SetText(txt)
	s.whole[sl] = true
	call, dims := measure(gtx, w)
	r := image.Rectangle{Max: dims.Size}
	if s.holds(key) {
		if _, _, ok := s.rangeOf(key, utf8.RuneCountInString(txt)); ok {
			paint.FillShape(gtx.Ops, d.selColor(), clip.UniformRRect(r.Inset(-gtx.Dp(2)), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
	}
	// Its area, under what it draws, places it for the selection.
	area := clip.Rect(r).Push(gtx.Ops)
	gioevent.Op(gtx.Ops, sl)
	area.Pop()
	s.press(gtx, sl)
	call.Add(gtx.Ops)
	s.size[sl] = dims.Size
	return dims
}

// paint sets sl's selection to its part of the transcript's.
func (s *textSel) paint(key string, sl *widget.Selectable, txt string) {
	if !s.has() {
		if sl.SelectionLen() > 0 {
			sl.ClearSelection()
		}
		return
	}
	if a, b, ok := s.rangeOf(key, utf8.RuneCountInString(txt)); ok {
		sl.SetCaret(a, b)
	} else if sl.SelectionLen() > 0 {
		sl.ClearSelection()
	}
}

// text is the selection as the reader sees it: each block's part in
// reading order, a line apart. A prompt's image chips copy as the tokens
// they stand for.
func (t *Transcript) selText() string { return t.selJoined("\n") }

// selJoined is the selection as selText has it, its blocks sep apart.
func (t *Transcript) selJoined(sep string) string {
	type part struct {
		o   int
		txt string
	}
	var parts []part
	for key, sl := range t.sels {
		txt := sl.Text()
		a, b, ok := t.span.rangeOf(key, utf8.RuneCountInString(txt))
		if !ok {
			continue
		}
		s := string([]rune(txt)[a:b])
		if t.span.whole[sl] {
			s = txt
		} else if ct, ok := t.span.typed[key]; ok && ct.text == txt {
			s = ct.typed(a, b)
		}
		parts = append(parts, part{t.span.ord[key], s})
	}
	slices.SortFunc(parts, func(x, y part) int { return x.o - y.o })
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.txt
	}
	return strings.Join(out, sep)
}

// copySel copies the selection, and tells the window so it shows its
// notice over the transcript.
func (t *Transcript) copySel(gtx layout.Context) {
	s := t.selText()
	if s == "" {
		return
	}
	t.copy(gtx, "selection", s)
	if t.Copied != nil {
		r := image.Rectangle{Max: t.size}
		if tr, ok := gtx.Source.Transform(&t.span); ok {
			r = r.Add(tr.Transform(f32.Point{}).Round())
		}
		t.Copied(gtx, s, r)
	}
}

// hitBox is a selectable drawn in the last frame, in the window.
type hitBox struct {
	key   string
	sl    *widget.Selectable
	tr    f32.Affine2D
	r     image.Rectangle
	whole bool
}

// boxes are the selectables the last frame drew, topmost last.
func (t *Transcript) boxes(gtx layout.Context, drawn []*widget.Selectable) []hitBox {
	var out []hitBox
	for _, sl := range drawn {
		tr, ok := gtx.Source.Transform(sl)
		sz := t.span.size[sl]
		key := t.msg.keys[sl]
		if !ok || sz == (image.Point{}) || key == "" {
			continue
		}
		p0, p1 := tr.Transform(f32.Point{}).Round(), tr.Transform(f32.Pt(float32(sz.X), float32(sz.Y))).Round()
		out = append(out, hitBox{key, sl, tr, image.Rectangle{Min: p0, Max: p1}.Canon(), t.span.whole[sl]})
	}
	return out
}

// runeAt is the rune of b nearest w, a point in the window.
func (b hitBox) runeAt(w f32.Point) int {
	p := b.tr.Invert().Transform(w)
	sz := b.r.Size()
	if b.whole { // its start above its middle, its end below
		if p.Y < float32(sz.Y)/2 {
			return 0
		}
		return utf8.RuneCountInString(b.sl.Text())
	}
	if b.sl.Truncated() && p.X >= float32(sz.X-2) { // past a cut line's ellipsis: all of it
		return utf8.RuneCountInString(b.sl.Text())
	}
	p.X = float32(math.Max(0, math.Min(float64(p.X), float64(sz.X-1))))
	return b.sl.RuneAt(p.Round())
}

// hit is the text under w, a point in the window: the topmost box that
// holds it.
func hitAt(bs []hitBox, w f32.Point) (selPos, bool) {
	p := w.Round()
	for i := len(bs) - 1; i >= 0; i-- {
		if p.In(bs[i].r) {
			return selPos{bs[i].key, bs[i].runeAt(w)}, true
		}
	}
	return selPos{}, false
}

// nearest is where a drag at w reaches: the text under it, else the
// nearest text on its line, else the end of the last block above it, else
// the start of the first below it.
func (s *textSel) nearest(bs []hitBox, w f32.Point) (selPos, bool) {
	if p, ok := hitAt(bs, w); ok {
		return p, true
	}
	y := int(w.Y)
	best, dist := -1, math.MaxInt
	for i, b := range bs {
		if y >= b.r.Min.Y && y < b.r.Max.Y {
			d := max(b.r.Min.X-int(w.X), int(w.X)-b.r.Max.X+1, 0)
			if d < dist {
				best, dist = i, d
			}
		}
	}
	if best >= 0 {
		return selPos{bs[best].key, bs[best].runeAt(w)}, true
	}
	above, below := -1, -1
	for i, b := range bs {
		o := s.ord[b.key]
		if b.r.Max.Y <= y && (above < 0 || o > s.ord[bs[above].key]) {
			above = i
		}
		if b.r.Min.Y > y && (below < 0 || o < s.ord[bs[below].key]) {
			below = i
		}
	}
	switch {
	case above >= 0:
		return selPos{bs[above].key, utf8.RuneCountInString(bs[above].sl.Text())}, true
	case below >= 0:
		return selPos{bs[below].key, 0}, true
	}
	return selPos{}, false
}

// wordAt is the word around rune i of txt, or the one rune there that is
// not a word's; lineAt is its line, all of a paragraph's text.
func wordAt(txt string, i int) (int, int) {
	r := []rune(txt)
	if len(r) == 0 {
		return 0, 0
	}
	i = min(max(i, 0), len(r)-1)
	word := func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' }
	if !word(r[i]) && i > 0 && word(r[i-1]) {
		i--
	}
	if !word(r[i]) {
		return i, i + 1
	}
	a, b := i, i+1
	for a > 0 && word(r[a-1]) {
		a--
	}
	for b < len(r) && word(r[b]) {
		b++
	}
	return a, b
}

func lineAt(txt string, i int) (int, int) {
	r := []rune(txt)
	i = min(max(i, 0), len(r))
	a, b := i, i
	for a > 0 && r[a-1] != '\n' {
		a--
	}
	for b < len(r) && r[b] != '\n' {
		b++
	}
	return a, b
}

// selectEvents takes the pointer on the transcript's text and the
// menu's keys, once the rows laid out and took their presses: drawn are
// the selectables the last frame drew, where the pointer was. A change
// shows in the next frame.
func (t *Transcript) selectEvents(gtx layout.Context, drawn []*widget.Selectable) {
	s := &t.span
	was := [2]selPos{s.a, s.b}
	defer func() {
		s.pressed = nil
		if was != [2]selPos{s.a, s.b} {
			gtx.Execute(op.InvalidateCmd{})
		}
	}()
	var bs []hitBox
	boxes := func() []hitBox {
		if bs == nil {
			bs = t.boxes(gtx, drawn)
		}
		return bs
	}
	tr, _ := gtx.Source.Transform(&t.span)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &t.span, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		w := tr.Transform(e.Position)
		local := e.Position.Round()
		switch e.Kind {
		case pointer.Press:
			if s.menu && local.In(s.menuR) || local.In(t.cite.r) {
				continue // the menu's or the toolbar's own button takes it
			}
			s.menu = false
			if e.Buttons == pointer.ButtonSecondary {
				if s.has() {
					s.menu, s.menuAt = true, local
				}
				continue
			}
			if e.Buttons != pointer.ButtonPrimary || e.Source != pointer.Mouse {
				continue // a finger on a touch screen scrolls
			}
			// The block the press landed on, by Gio's own hit test: none
			// where a card, a field or a button is drawn over text.
			var p selPos
			ok := false
			for _, b := range boxes() {
				if b.sl == s.pressed {
					p, ok = selPos{b.key, b.runeAt(w)}, true
				}
			}
			if !ok {
				t.ClearSelection() // a press off the text, as a browser's
				continue
			}
			if gtx.Now.Sub(s.last) < multiClick && w.Sub(s.lpos).Round().In(image.Rect(-slop, -slop, slop+1, slop+1)) {
				s.n++
			} else {
				s.n = 1
			}
			s.last, s.lpos = gtx.Now, w
			s.drag, s.grab, s.from, s.at, s.won = true, false, w, w, true
			txt := t.sels[p.key].Text()
			switch {
			case s.n >= 2 && s.whole[t.sels[p.key]]:
				s.a, s.b = selPos{p.key, 0}, selPos{p.key, utf8.RuneCountInString(txt)}
			case e.Modifiers.Contain(key.ModShift) && s.a.key != "":
				s.b = p
			case s.n == 2:
				a, b := wordAt(txt, p.off)
				s.a, s.b = selPos{p.key, a}, selPos{p.key, b}
			case s.n >= 3:
				a, b := lineAt(txt, p.off)
				s.a, s.b = selPos{p.key, a}, selPos{p.key, b}
			default:
				s.a, s.b = p, p
			}
		case pointer.Drag:
			if !s.drag {
				continue
			}
			s.at = w
			if !s.grab && !w.Sub(s.from).Round().In(image.Rect(-slop, -slop, slop+1, slop+1)) {
				// A drag selects: the row it started on does not open.
				s.grab = true
				gtx.Execute(pointer.GrabCmd{Tag: &t.span, ID: e.PointerID})
			}
			if s.grab && s.n < 2 {
				if p, ok := s.nearest(boxes(), w); ok {
					s.b = p
				}
			}
		case pointer.Release, pointer.Cancel:
			if !s.drag {
				continue
			}
			s.drag = false
			if e.Kind == pointer.Release && s.has() && t.CopyOnSelect {
				t.copySel(gtx)
			}
		}
	}
	if s.menu && s.copyB.Clicked(gtx) {
		s.menu = false
		t.copySel(gtx)
	}
	if s.menu {
		for {
			if _, ok := gtx.Event(key.Filter{Name: key.NameEscape}); !ok {
				break
			}
			s.menu = false
		}
	}
}

// CopyKey takes Ctrl+C (Cmd+C) while the transcript holds a selection,
// and copies it. The window calls it before the composer lays out, while
// the chat holds the keys; without a selection Ctrl+C stays the
// composer's.
func (t *Transcript) CopyKey(gtx layout.Context) {
	if !t.span.has() {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: "C", Required: key.ModShortcut})
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			t.copySel(gtx)
		}
	}
}

// autoScroll scrolls while a drag holds past the transcript's top or
// bottom edge, faster the farther past, and moves the selection's end
// over the text that scrolls in.
func (t *Transcript) autoScroll(gtx layout.Context, drawn []*widget.Selectable, h int) {
	s := &t.span
	if !s.drag || !s.grab {
		return
	}
	tr, ok := gtx.Source.Transform(&t.span)
	if !ok {
		return
	}
	y := tr.Invert().Transform(s.at).Y
	var dy float32
	switch {
	case y < 0:
		dy = y
	case y > float32(h):
		dy = y - float32(h)
	}
	if dy != 0 {
		lim := float32(gtx.Dp(48))
		dy = max(-lim, min(lim, dy/3))
		if dy > -1 && dy < 1 {
			dy = float32(math.Copysign(1, float64(dy)))
		}
		t.list.Position.Offset += int(dy)
		t.list.Position.BeforeEnd = true
		gtx.Execute(op.InvalidateCmd{})
	}
	if s.n < 2 {
		if p, ok := s.nearest(t.boxes(gtx, drawn), s.at); ok {
			s.b = p
		}
	}
}

// selMenu draws the right-click menu over a selection: Copy.
func (d *cdraw) selMenu(gtx layout.Context, size image.Point) {
	s, th := &d.t.span, d.th
	if !s.menu || !s.has() {
		s.menu = false
		return
	}
	mw, ih, pad := gtx.Dp(168), gtx.Dp(hit), gtx.Dp(4)
	p := s.menuAt
	p.X = max(0, min(p.X, size.X-mw))
	p.Y = max(0, min(p.Y, size.Y-ih-2*pad))
	r := image.Rect(p.X, p.Y, p.X+mw, p.Y+ih+2*pad)
	s.menuR = r
	kit.Surface(gtx, r, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, r.Min.Add(image.Pt(pad, pad)), func(gtx layout.Context) layout.Dimensions {
		return clickable(gtx, &s.copyB, func(gtx layout.Context) layout.Dimensions {
			iw := mw - 2*pad
			if s.copyB.Hovered() {
				paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceElevated, th.Fg, 0.06), clip.UniformRRect(image.Rect(0, 0, iw, ih), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			}
			c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, "Copy")
			})
			at(gtx, image.Pt(gtx.Dp(8), (ih-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
			return layout.Dimensions{Size: image.Pt(iw, ih)}
		})
	})
	// The menu takes presses so they do not reach the transcript under it.
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	pointer.CursorDefault.Add(gtx.Ops)
}
