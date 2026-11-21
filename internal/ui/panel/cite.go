package panel

import (
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A selection within one reply gets a small toolbar under its end, T3
// Code's: Cite in composer, which quotes it in the draft, and Copy. A
// quote's chip brings its source back into view and flashes it once.

// Cite is a passage of a reply the toolbar quoted, for the composer.
type Cite struct {
	Text string
	Src  chatin.Source
}

// citeState is the toolbar, the quotes it made, and a reveal on its way.
type citeState struct {
	citeB, copyB widget.Clickable
	r            image.Rectangle // the toolbar, in the transcript, as last drawn; empty while hidden
	cites        []Cite          // not taken by Cites yet
	content      []session.Item  // the last frame's items, for Reveal
	goTo         *chatin.Source  // a source to bring into view
	moved        bool            // goTo's row is in view: next its text goes a third down
	flash        chatin.Source   // the source flashing, Item "" for none
	flashAt      time.Time
}

// Lucide's quote, for Cite in composer.
const icQuote = "M16 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2zM5 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2z"

// flashFor is how long a revealed source's flash takes to fade.
const flashFor = 1200 * time.Millisecond

// Cites returns the passages quoted since the last call, once each.
func (t *Transcript) Cites() []Cite {
	c := t.cite.cites
	t.cite.cites = nil
	return c
}

// Reveal brings src, a quote's source, into view and flashes it; false
// when the chat no longer has its reply.
func (t *Transcript) Reveal(src chatin.Source) bool {
	for _, it := range t.cite.content {
		if it.ID == src.Item && it.Kind == session.KindAssistantText {
			t.cite.goTo, t.cite.moved = &src, false
			return true
		}
	}
	return false
}

// itemOf is the item a selectable's key draws: the part before its /.
func itemOf(key string) string {
	item, _, _ := strings.Cut(key, "/")
	return item
}

// quotable is the selection as a quote's source when it lies within one
// reply of s, ok false otherwise.
func (t *Transcript) quotable(s *session.Snapshot) (chatin.Source, bool) {
	sp := &t.span
	if !sp.has() || sp.drag || sp.menu {
		return chatin.Source{}, false
	}
	lo, hi := sp.order()
	item := itemOf(lo.key)
	if itemOf(hi.key) != item || !strings.Contains(lo.key, "/") || !strings.Contains(hi.key, "/") {
		return chatin.Source{}, false
	}
	for i := len(s.Content) - 1; i >= 0; i-- {
		if it := &s.Content[i]; it.ID == item {
			if it.Kind != session.KindAssistantText {
				return chatin.Source{}, false
			}
			return chatin.Source{Item: item, FromKey: lo.key, From: lo.off, ToKey: hi.key, To: hi.off}, true
		}
	}
	return chatin.Source{}, false
}

// local is p, a point in the window, in the transcript.
func (t *Transcript) local(gtx layout.Context, p f32.Point) image.Point {
	if tr, ok := gtx.Source.Transform(&t.span); ok {
		return tr.Invert().Transform(p).Round()
	}
	return p.Round()
}

// endRect is the selection's last line's box at its end, in the
// transcript, from the frame before's drawn selectables; ok false when it
// is not drawn.
func (t *Transcript) endRect(gtx layout.Context, drawn []*widget.Selectable, key string, off int) (image.Rectangle, bool) {
	for _, b := range t.boxes(gtx, drawn) {
		if b.key != key {
			continue
		}
		if b.whole {
			return image.Rectangle{Min: t.local(gtx, f32.Pt(float32(b.r.Max.X), float32(b.r.Max.Y))), Max: t.local(gtx, f32.Pt(float32(b.r.Max.X), float32(b.r.Max.Y)))}, true
		}
		rg := b.sl.Regions(max(0, off-1), off, nil)
		if len(rg) == 0 {
			return image.Rectangle{}, false
		}
		g := rg[len(rg)-1].Bounds
		p0 := t.local(gtx, b.tr.Transform(f32.Pt(float32(g.Min.X), float32(g.Min.Y))))
		p1 := t.local(gtx, b.tr.Transform(f32.Pt(float32(g.Max.X), float32(g.Max.Y))))
		return image.Rectangle{Min: p0, Max: p1}.Canon(), true
	}
	return image.Rectangle{}, false
}

// drawCite draws the toolbar under the end of a selection within one
// reply, above it near the transcript's bottom, and takes its clicks.
func (t *Transcript) drawCite(gtx layout.Context, d *cdraw, s *session.Snapshot, drawn []*widget.Selectable, size image.Point) {
	c, th := &t.cite, d.th
	c.content = s.Content
	src, ok := t.quotable(s)
	if c.citeB.Clicked(gtx) && ok {
		if txt := t.selJoined("\n\n"); utf8.RuneCountInString(txt) <= chatin.MaxQuote && strings.TrimSpace(txt) != "" {
			c.cites = append(c.cites, Cite{Text: txt, Src: src})
			t.ClearSelection()
			c.r = image.Rectangle{}
			return
		}
	}
	if c.copyB.Clicked(gtx) && ok {
		t.copySel(gtx)
	}
	c.r = image.Rectangle{}
	if !ok {
		return
	}
	end, ok := t.endRect(gtx, drawn, src.ToKey, src.To)
	if !ok || end.Max.Y < 0 || end.Min.Y > size.Y {
		return
	}
	long := utf8.RuneCountInString(t.selJoined("\n\n")) > chatin.MaxQuote
	label := "Cite in composer"
	if long {
		label = "Too long to cite"
	}
	pad, h, gap := gtx.Dp(theme.SpaceXS), gtx.Dp(28), gtx.Dp(theme.Space2XS)
	citeCall, citeD := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.labelButton(gtx, &c.citeB, icQuote, label, !long)
	})
	copyCall, copyD := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.labelButton(gtx, &c.copyB, icClip, "Copy", true)
	})
	w := citeD.Size.X + gap + copyD.Size.X + 2*pad
	bh := h + 2*pad
	x := min(max(0, end.Max.X-w/2), size.X-w)
	y := end.Max.Y + gtx.Dp(theme.SpaceS)
	if y+bh > size.Y {
		y = end.Min.Y - gtx.Dp(theme.SpaceS) - bh
	}
	y = max(0, y)
	r := image.Rect(x, y, x+w, y+bh)
	c.r = r
	kit.Surface(gtx, r, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, r.Min.Add(image.Pt(pad, pad)), func(gtx layout.Context) layout.Dimensions { citeCall.Add(gtx.Ops); return citeD })
	at(gtx, r.Min.Add(image.Pt(pad+citeD.Size.X+gap, pad)), func(gtx layout.Context) layout.Dimensions { copyCall.Add(gtx.Ops); return copyD })
	// The toolbar takes presses so they do not reach the text under it.
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	pointer.CursorDefault.Add(gtx.Ops)
}

// labelButton is a 28 dp ghost button with a 14 dp icon and a label, dim
// and inert when off.
func (d *cdraw) labelButton(gtx layout.Context, b *widget.Clickable, icon, label string, on bool) layout.Dimensions {
	th := d.th
	gtx.Constraints.Min = image.Point{}
	body := func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		h, pad, is := gtx.Dp(28), gtx.Dp(theme.SpaceS), gtx.Dp(14)
		col := th.Fg
		if !on {
			col = th.Muted
		}
		tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), col, label)
		})
		w := pad + is + gtx.Dp(6) + td.Size.X + pad
		if on && (b.Hovered() || b.Pressed()) {
			fill := th.Hover
			if b.Pressed() {
				fill = th.Pressed
			}
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		at(gtx, image.Pt(pad, (h-is)/2), func(gtx layout.Context) layout.Dimensions { return sidebar.IconPath(gtx, icon, is, col) })
		at(gtx, image.Pt(pad+is+gtx.Dp(6), (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	if !on {
		return body(gtx)
	}
	return clickable(gtx, b, body)
}

// revealJump brings the source Reveal asked for into view: its row
// first, then, once laid out, its text a third down the view of height
// h, and starts its flash.
func (t *Transcript) revealJump(gtx layout.Context, s *session.Snapshot, drawn []*widget.Selectable, h int) {
	c := &t.cite
	if c.goTo == nil {
		return
	}
	src := *c.goTo
	i := -1
	for k := len(s.Content) - 1; k >= 0; k-- {
		if s.Content[k].ID == src.Item {
			i = k
			break
		}
	}
	if i < 0 {
		c.goTo = nil
		return
	}
	gtx.Execute(op.InvalidateCmd{})
	if c.moved {
		if r, ok := t.endRect(gtx, drawn, src.FromKey, src.From+1); ok {
			t.list.Position.Offset += r.Min.Y - h/3
			t.list.Position.BeforeEnd = true
		}
		c.goTo, c.moved = nil, false
		c.flash, c.flashAt = src, gtx.Now
		return
	}
	k := t.findRow(s, i)
	if k < 0 {
		return // a closed fold opened: the next frame's rows hold it
	}
	t.list.Position.First, t.list.Position.Offset, t.list.Position.BeforeEnd = k+1, 0, true
	if _, ok := t.msg.ys[k+1]; ok {
		c.moved = true
	}
}

// flashMark paints a revealed source's part of sl's text txt, under it,
// fading out over flashFor.
func (t *Transcript) flashMark(gtx layout.Context, d *cdraw, sl *widget.Selectable, txt string) {
	c := &t.cite
	if c.flash.Item == "" {
		return
	}
	el := gtx.Now.Sub(c.flashAt)
	if el >= flashFor || el < 0 {
		c.flash = chatin.Source{}
		return
	}
	key := t.msg.keys[sl]
	if itemOf(key) != c.flash.Item {
		return
	}
	f := textSel{a: selPos{c.flash.FromKey, c.flash.From}, b: selPos{c.flash.ToKey, c.flash.To}, ord: t.span.ord}
	a, b, ok := f.rangeOf(key, utf8.RuneCountInString(txt))
	if !ok {
		return
	}
	k := 1 - float32(el)/float32(flashFor)
	col := theme.Mix(d.th.Bg, d.th.Yellow, 0.55)
	col.A = uint8(float32(col.A) * k * k)
	for _, g := range sl.Regions(a, b, nil) {
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(g.Bounds.Inset(-gtx.Dp(1)), gtx.Dp(theme.Space2XS)).Op(gtx.Ops))
	}
	gtx.Execute(op.InvalidateCmd{})
}
