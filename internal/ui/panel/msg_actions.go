package panel

import (
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/gesture"
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

// MessageKind is what a MessageAction asks the window to do.
type MessageKind uint8

const (
	ActEdit   MessageKind = iota + 1 // put Text in the composer
	ActRewind                        // rewind to before Turn's prompt, Text back in the composer
	ActFork                          // fork the chat from the end of Turn
)

// MessageAction is a prompt's hover button the window acts on.
type MessageAction struct {
	Kind MessageKind
	Turn string // the prompt's turn
	Text string // the prompt's text, without its image tokens
}

// Lucide's rotate-ccw, git-fork and chevron-up, for the prompts' buttons
// and the find bar.
const (
	icRewind  = "M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8M3 3v5h5"
	icFork    = "M12 15a3 3 0 1 0 0 6a3 3 0 1 0 0-6zM6 3a3 3 0 1 0 0 6a3 3 0 1 0 0-6zM18 3a3 3 0 1 0 0 6a3 3 0 1 0 0-6zM18 9v2c0 .6-.4 1-1 1H7c-.6 0-1-.4-1-1V9M12 12v3"
	icChevUp  = "m18 15-6-6-6 6"
	icChevDn  = "m6 9 6 6 6-6"
	icClose   = "M18 6 6 18M6 6l12 12"
	icPencil  = "M21.174 6.812a1 1 0 0 0-3.986-3.987L3.842 16.174a2 2 0 0 0-.5.83l-1.321 4.352a.5.5 0 0 0 .623.622l4.353-1.32a2 2 0 0 0 .83-.497zM15 5l4 4"
	icClip    = "M9 2h6a1 1 0 0 1 1 1v2a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1zM16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"
	icCheck   = "M20 6 9 17l-5-5"
	icSearch  = "m21 21-4.34-4.34M11 3a8 8 0 1 0 0 16a8 8 0 1 0 0-16z"
	icAskSide = "M7.9 20A9 9 0 1 0 4 16.1L2 22ZM9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3M12 17h.01"
)

// msgState is the transcript's message tools: the prompts' hover buttons,
// the find bar and the side questions.
type msgState struct {
	hovers map[string]*gesture.Hover     // per prompt
	keys   map[*widget.Selectable]string // each selectable's key: its item's id, then "/" and more
	acts   []MessageAction               // not taken by Actions yet

	confirm     string // the prompt whose Rewind asks to confirm
	confirmAt   anchor // its Rewind button
	confirmTurn string // the prompt's turn and text
	confirmText string
	ok, no      widget.Clickable // the confirm's buttons
	confirmBg   int              // the confirm's own area, so a press in it does not close it

	tipOf    *widget.Clickable // the button hovered for a tooltip
	tipSince time.Time
	tipShown time.Time // the last frame a tooltip showed, so a neighbor's shows at once
	tip      string
	tipAt    anchor

	el   int       // the list element being laid out
	dy   int       // and where its row starts in it, under its gap
	laid []laidRow // the list's elements laid out this frame
	ys   map[int]rowSpan

	find  finder
	aside asides
}

// anchor is a rectangle in a list element's own space.
type anchor struct {
	el int // the list element, rows offset by one
	r  image.Rectangle
}

// laidRow is a list element laid out this frame and its height.
type laidRow struct{ el, h int }

// rowSpan is where a list element is in the transcript's area.
type rowSpan struct{ y, h int }

// Actions returns the hover buttons clicked since the last call, once
// each.
func (t *Transcript) Actions() []MessageAction {
	a := t.msg.acts
	t.msg.acts = nil
	return a
}

// keyOf notes sl's key, so the find bar knows the item it draws.
func (m *msgState) keyOf(sl *widget.Selectable, key string) {
	if m.keys == nil {
		m.keys = map[*widget.Selectable]string{}
	}
	m.keys[sl] = key
}

// hover is prompt id's hover gesture.
func (m *msgState) hover(id string) *gesture.Hover {
	if m.hovers == nil {
		m.hovers = map[string]*gesture.Hover{}
	}
	h := m.hovers[id]
	if h == nil {
		h = new(gesture.Hover)
		m.hovers[id] = h
	}
	return h
}

// spans places the elements laid out this frame from the list's position:
// the first in view at minus its offset, the rest after it in order.
func (m *msgState) spans(pos layout.Position) {
	if m.ys == nil {
		m.ys = map[int]rowSpan{}
	}
	clear(m.ys)
	slices.SortFunc(m.laid, func(a, b laidRow) int { return a.el - b.el })
	y := -pos.Offset
	for _, r := range m.laid {
		if r.el >= pos.First {
			m.ys[r.el] = rowSpan{y, r.h}
			y += r.h
		}
	}
	y = -pos.Offset
	for i := len(m.laid) - 1; i >= 0; i-- {
		if r := m.laid[i]; r.el < pos.First {
			y -= r.h
			m.ys[r.el] = rowSpan{y, r.h}
		}
	}
}

// record wraps the list's element function: it notes each element and
// its height as it is laid out.
func (m *msgState) record(el layout.ListElement) layout.ListElement {
	m.laid = m.laid[:0]
	return func(gtx layout.Context, i int) layout.Dimensions {
		m.el, m.dy = i, 0
		d := el(gtx, i)
		m.laid = append(m.laid, laidRow{i, d.Size.Y})
		return d
	}
}

// at is a in the transcript's area, false while its element is not in
// view.
func (m *msgState) at(a anchor, x0 int) (image.Rectangle, bool) {
	s, ok := m.ys[a.el]
	if !ok {
		return image.Rectangle{}, false
	}
	return a.r.Add(image.Pt(x0, s.y)), true
}

// promptText is what a prompt's Copy and Edit take: its text as shown,
// without the sender line or its image tokens.
func promptText(it *session.Item) string {
	shown, _ := promptParts(it.Text)
	_, rest := promptSender(shown)
	return strings.TrimSpace(imageToken.ReplaceAllString(rest, ""))
}

// promptTools draws prompt it's hover buttons left of its bubble, whose
// left edge is at x in a row bh tall, while the pointer is on the row:
// Copy, Edit, and Rewind and Fork where the chat can. The row's hover
// area goes on top of what it holds, passing presses through.
func (d *cdraw) promptTools(gtx layout.Context, it *session.Item, x, bh int) {
	t, m := d.t, &d.t.msg
	hv := m.hover(it.ID)
	on := hv.Update(gtx.Source)
	turn := deref(it.TurnID)
	idle := !d.s.Busy()
	type tool struct {
		key, icon, tip string
		show           bool
	}
	tools := []tool{
		{"copy", icClip, "Copy", true},
		{"edit", icPencil, "Edit", true},
		{"rewind", icRewind, "Rewind to here", t.CanRewind && turn != "" && idle},
		{"fork", icFork, "Fork from here", t.CanFork && turn != "" && idle},
	}
	tools = slices.DeleteFunc(tools, func(b tool) bool { return !b.show })
	clicks := make([]*widget.Clickable, len(tools))
	for i, b := range tools {
		clicks[i] = t.click("msg:" + b.key + ":" + it.ID)
		on = on || clicks[i].Hovered()
	}
	copied := t.isCopied("prompt:" + it.ID)
	on = on || copied || m.confirm == it.ID
	for i, b := range tools {
		for clicks[i].Clicked(gtx) {
			switch b.key {
			case "copy":
				t.copy(gtx, "prompt:"+it.ID, promptText(it))
			case "edit":
				m.acts = append(m.acts, MessageAction{Kind: ActEdit, Text: promptText(it)})
			case "rewind":
				m.confirm = it.ID
				gtx.Execute(op.InvalidateCmd{}) // its confirm draws over the rows laid out after this one
			case "fork":
				m.acts = append(m.acts, MessageAction{Kind: ActFork, Turn: turn})
			}
		}
	}
	if m.confirm == it.ID {
		m.confirmTurn, m.confirmText = turn, promptText(it)
	}
	if on {
		bs, gap := gtx.Dp(hit), gtx.Dp(theme.Space2XS)
		w := len(tools)*bs + (len(tools)-1)*gap
		x0 := max(0, x-gtx.Dp(theme.SpaceXS)-w)
		y0 := bh - bs
		if x0+w > x { // no room beside a wide bubble: a raised strip over its corner
			r := image.Rect(x0-gap, y0-gap, x0+w+gap, bh+gap)
			kit.Surface(gtx, r, gtx.Dp(theme.RadiusPopover), kit.Floating, d.th.BorderStrong, d.th.SurfaceElevated)
		}
		for i, b := range tools {
			r := image.Rect(x0+i*(bs+gap), y0, x0+i*(bs+gap)+bs, bh)
			ic, col := b.icon, d.th.Muted
			if b.key == "copy" && copied {
				ic, col = icCheck, d.th.Readable(d.th.Green, d.th.Bg)
			}
			at(gtx, r.Min, func(gtx layout.Context) layout.Dimensions {
				return d.iconButton(gtx, clicks[i], ic, col, b.key == "rewind" && m.confirm == it.ID)
			})
			d.tipFor(gtx, clicks[i], b.tip, r)
			if b.key == "rewind" {
				m.confirmAnchor(it.ID, r)
			}
		}
	}
	area := clip.Rect{Max: image.Pt(d.col, bh)}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	hv.Add(gtx.Ops)
	pass.Pop()
	area.Pop()
}

// iconButton is a ghost button of icon path ic, hit square, the Hover
// fill under the pointer, Pressed while held or held open.
func (d *cdraw) iconButton(gtx layout.Context, c *widget.Clickable, ic string, col color.NRGBA, held bool) layout.Dimensions {
	th := d.th
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		bs, isz := gtx.Dp(hit), gtx.Dp(theme.SpaceM+theme.Space2XS)
		r := clip.UniformRRect(image.Rect(0, 0, bs, bs), gtx.Dp(theme.RadiusControl))
		switch {
		case c.Pressed() || held:
			paint.FillShape(gtx.Ops, th.Pressed, r.Op(gtx.Ops))
		case c.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, r.Op(gtx.Ops))
		}
		if c.Hovered() || held {
			col = th.Fg
		}
		at(gtx, image.Pt((bs-isz)/2, (bs-isz)/2), func(gtx layout.Context) layout.Dimensions {
			return sidebar.IconPath(gtx, ic, isz, col)
		})
		return layout.Dimensions{Size: image.Pt(bs, bs)}
	})
}

// Tooltip timing, as the chat header's: the first waits tipDelay, a
// neighbor hovered within tipChain of the last one shows at once.
const (
	tipDelay = 500 * time.Millisecond
	tipChain = 300 * time.Millisecond
)

// tipFor notes tip for button c at r, in the element being laid out,
// while c is hovered; drawTip shows it once it is due.
func (d *cdraw) tipFor(gtx layout.Context, c *widget.Clickable, tip string, r image.Rectangle) {
	m := &d.t.msg
	if !c.Hovered() {
		return
	}
	if m.tipOf != c {
		m.tipOf, m.tipSince = c, gtx.Now
	}
	m.tip, m.tipAt = tip, anchor{el: m.el, r: r.Add(image.Pt(0, m.dy))}
}

// drawTip draws the hovered button's tooltip under it, over everything,
// in the transcript's area of size; x0 is the column's left edge.
func (t *Transcript) drawTip(gtx layout.Context, d *cdraw, x0 int, size image.Point) {
	m := &t.msg
	if m.tipOf == nil || !m.tipOf.Hovered() || m.tip == "" || m.confirm != "" {
		m.tipOf, m.tip = nil, ""
		return
	}
	if gtx.Now.Sub(m.tipSince) < tipDelay && gtx.Now.Sub(m.tipShown) > tipChain {
		gtx.Execute(op.InvalidateCmd{At: m.tipSince.Add(tipDelay)})
		return
	}
	r, ok := m.at(m.tipAt, x0)
	if !ok {
		return
	}
	m.tipShown = gtx.Now
	d.tooltip(gtx, m.tip, r, size)
}

// tooltip is tip in a small popover under r, or over it near the bottom,
// kept inside size.
func (d *cdraw) tooltip(gtx layout.Context, tip string, r image.Rectangle, size image.Point) {
	th := d.th
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Fg, tip)
	})
	px, py := gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceXS)
	box := image.Pt(td.Size.X+2*px, td.Size.Y+2*py)
	p := kit.Place(r, box, image.Rectangle{Max: size}, kit.BelowStart, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceXS))
	rect := image.Rectangle{Min: p, Max: p.Add(box)}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	at(gtx, p.Add(image.Pt(px, py)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
}
