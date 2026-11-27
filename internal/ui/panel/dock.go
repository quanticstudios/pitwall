package panel

// The inbox dock: under a chat, above its composer, everything that
// waits on the user in the other tabs and machines, each with its
// answers as buttons. It folds to one line, and draws nothing while
// nothing waits and no agent works.

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Dock answers and opens a chat's inbox items. Open is whether it lists
// them or folds to its header; the window keeps it in config.
type Dock struct {
	Open bool
	fold widget.Clickable
	btns map[string]*widget.Clickable
	list layout.List
	acts []DockAction
	// toggled is set when the header's chevron flipped Open this frame.
	toggled bool
}

// DockAction is a click on an inbox item's button: Answer is "allow" or
// "deny" for an approval, one of Item.Answers for proto.Press, "" to
// open the item's tab.
type DockAction struct {
	Item   model.InboxItem
	Answer string
}

// Answers for an approval's buttons.
const (
	DockAllow = "allow"
	DockDeny  = "deny"
)

// dockMaxAnswers is how many of a question's answers show as buttons;
// more opens the tab.
const dockMaxAnswers = 3

// dockRows is how many rows show whole before the list scrolls.
const dockRows = 4

// Actions are the clicks since the last call.
func (d *Dock) Actions() []DockAction {
	a := d.acts
	d.acts = nil
	return a
}

// Toggled reports whether the header folded or opened the dock since the
// last call, for the window to remember Open.
func (d *Dock) Toggled() bool {
	t := d.toggled
	d.toggled = false
	return t
}

func (d *Dock) btn(key string) *widget.Clickable {
	if d.btns == nil {
		d.btns = map[string]*widget.Clickable{}
	}
	b := d.btns[key]
	if b == nil {
		b = new(widget.Clickable)
		d.btns[key] = b
	}
	return b
}

// DockItems are in's items but those of tab here, which its own chat
// shows as cards.
func DockItems(in model.Inbox, here string) []model.InboxItem {
	var out []model.InboxItem
	for _, it := range in.Items {
		if it.WorkspaceID != here {
			out = append(out, it)
		}
	}
	return out
}

// dockAnswers are an item's buttons: Deny and Allow, a question's or
// plan's answers, else Open.
func dockAnswers(it model.InboxItem) []string {
	switch {
	case it.Kind == model.InboxApproval:
		return []string{DockDeny, DockAllow}
	case (it.Kind == model.InboxQuestion || it.Kind == model.InboxPlan) && len(it.Answers) > 0 && len(it.Answers) <= dockMaxAnswers:
		return it.Answers
	}
	return []string{""}
}

// dockLine is the row's text after the tab's name.
func dockLine(it model.InboxItem) string {
	switch it.Kind {
	case model.InboxApproval, model.InboxPlan:
		return " " + it.Text
	}
	return ": " + it.Text
}

// Layout draws the dock in the column at x, w wide, for the chat of tab
// here, and returns its height. It runs its clicks first.
func (d *Dock) Layout(gtx layout.Context, th *theme.Theme, in model.Inbox, here string, x, w int) layout.Dimensions {
	items := DockItems(in, here)
	if len(items) == 0 && in.Moving == 0 || w <= 0 {
		return layout.Dimensions{}
	}
	for d.fold.Clicked(gtx) {
		d.Open, d.toggled = !d.Open, true
	}
	for _, it := range items {
		for _, a := range dockAnswers(it) {
			for d.btn(dockKey(it, a)).Clicked(gtx) {
				d.acts = append(d.acts, DockAction{Item: it, Answer: a})
			}
		}
	}
	open := d.Open && len(items) > 0
	px := gtx.Dp(theme.SpaceM)
	small := th.Sp(theme.Small)
	headH := gtx.Dp(30)
	rowH := gtx.Dp(34)
	// Past dockRows, half of the next row shows, so the list reads as one that scrolls.
	listH := len(items) * rowH
	if len(items) > dockRows {
		listH = dockRows*rowH + rowH/2
	}
	h := headH
	if open {
		h += listH
	}
	top := gtx.Dp(theme.SpaceXS)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, top+h+top)}.Op())
	defer op.Offset(image.Pt(x, top)).Push(gtx.Ops).Pop()
	rad := gtx.Dp(theme.RadiusCard)
	rrect(gtx, image.Rect(0, 0, w, h), rad, th.Surface, th.BorderSubtle)

	// The header: Inbox, the counts, the chevron; all of it folds.
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w, headH))
	head := func(gtx layout.Context) layout.Dimensions {
		if len(items) > 0 && (d.fold.Hovered() || d.fold.Pressed()) {
			c := th.Hover
			if d.fold.Pressed() {
				c = th.Pressed
			}
			r := clip.RRect{Rect: image.Rect(1, 1, w-1, headH), NW: rad, NE: rad}
			if !open {
				r.SE, r.SW, r.Rect.Max.Y = rad, rad, headH-1
			}
			paint.FillShape(gtx.Ops, c, r.Op(gtx.Ops))
		}
		parts := []part{{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), small, th.Fg, "Inbox", 1)
		}}}
		for _, s := range model.InboxHead(items, in.Moving) {
			parts = append(parts, part{gap: gtx.Dp(theme.SpaceM), w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, small, th.Muted, s, 1)
			}})
		}
		parts = append(parts, part{flex: true, w: func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }})
		if len(items) > 0 {
			parts = append(parts, part{w: func(gtx layout.Context) layout.Dimensions {
				name := "chevron-down"
				if open {
					name = "chevron-up"
				}
				return sidebar.Icon(gtx, name, gtx.Dp(14), th.Muted)
			}})
		}
		at(gtx, image.Pt(px, (headH-gtx.Sp(small)*4/3)/2), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Max: image.Pt(w-2*px, headH)}
			return row(gtx, parts...)
		})
		return layout.Dimensions{Size: image.Pt(w, headH)}
	}
	if len(items) > 0 {
		kit.Clickable(g, &d.fold, head)
	} else {
		head(g)
	}
	if !open {
		return layout.Dimensions{Size: image.Pt(w, top+h+gtx.Dp(theme.SpaceXS))}
	}

	// The rows, under a line, scrolling past dockRows.
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(1, headH-1), Max: image.Pt(w-1, headH)}.Op())
	lo := op.Offset(image.Pt(0, headH)).Push(gtx.Ops)
	cl := clip.RRect{Rect: image.Rect(1, 0, w-1, listH-1), SE: rad - 1, SW: rad - 1}.Push(gtx.Ops)
	g = gtx
	g.Constraints = layout.Exact(image.Pt(w, listH))
	d.list.Axis = layout.Vertical
	d.list.Layout(g, len(items), func(gtx layout.Context, i int) layout.Dimensions {
		return d.row(gtx, th, items[i], i > 0, w, rowH, px)
	})
	cl.Pop()
	lo.Pop()
	return layout.Dimensions{Size: image.Pt(w, top+h+gtx.Dp(theme.SpaceXS))}
}

// row is one item: its dot, the tab and what it wants, and its buttons.
func (d *Dock) row(gtx layout.Context, th *theme.Theme, it model.InboxItem, line bool, w, h, px int) layout.Dimensions {
	if line {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(px, 0), Max: image.Pt(w-px, 1)}.Op())
	}
	body := th.Sp(theme.Body)
	dot := th.Yellow
	if !it.Kind.ForYou() {
		dot = th.Red
	}
	parts := []part{
		{w: func(gtx layout.Context) layout.Dimensions {
			s := gtx.Dp(6)
			circle(gtx, image.Pt(0, (h-s)/2), s, dot)
			return layout.Dimensions{Size: image.Pt(s, h)}
		}},
		{gap: gtx.Dp(theme.SpaceS), flex: true, w: func(gtx layout.Context) layout.Dimensions {
			return at(gtx, image.Pt(0, (h-gtx.Sp(body)*4/3)/2), func(gtx layout.Context) layout.Dimensions {
				return row(gtx,
					part{w: func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(180))
						return label(gtx, th, medium(th.UIFont), body, th.Fg, it.Tab, 1)
					}},
					part{flex: true, w: func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, th.UIFont, body, th.Muted, dockLine(it), 1)
					}})
			})
		}},
	}
	for _, a := range dockAnswers(it) {
		parts = append(parts, part{gap: gtx.Dp(theme.SpaceXS + theme.Space2XS), w: func(gtx layout.Context) layout.Dimensions {
			return at(gtx, image.Pt(0, (h-gtx.Dp(24))/2), func(gtx layout.Context) layout.Dimensions {
				return dockButton(gtx, th, d.btn(dockKey(it, a)), it, a)
			})
		}})
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(w-2*px, h)}
	at(gtx, image.Pt(px, 0), func(gtx layout.Context) layout.Dimensions { return row(g, parts...) })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// dockKey names an item's button across frames.
func dockKey(it model.InboxItem, answer string) string {
	return string(it.Kind) + "\x00" + it.PaneID + "\x00" + it.WorkspaceID + "\x00" + strconv.FormatInt(it.At, 10) + "\x00" + answer
}

// dockButton is one of a row's buttons: Allow filled, the rest bordered,
// a recommended answer with a star.
func dockButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, it model.InboxItem, a string) layout.Dimensions {
	switch {
	case a == DockAllow:
		return kit.Button(gtx, th, c, kit.Primary, kit.Small, "Allow")
	case a == DockDeny:
		return kit.Button(gtx, th, c, kit.Secondary, kit.Small, "Deny")
	case a == "":
		name := "Open"
		if it.Kind == model.InboxForm {
			name = "Open form"
		}
		return kit.Button(gtx, th, c, kit.Secondary, kit.Small, name)
	}
	name, star := model.Recommended(a)
	if !star {
		return kit.Button(gtx, th, c, kit.Secondary, kit.Small, shorten(name))
	}
	// why: kit.Button draws text only; the star sits in room its trailing spaces leave.
	m := op.Record(gtx.Ops)
	dims := kit.Button(gtx, th, c, kit.Secondary, kit.Small, shorten(name)+"     ")
	call := m.Stop()
	call.Add(gtx.Ops)
	s := gtx.Dp(11)
	at(gtx, image.Pt(dims.Size.X-gtx.Dp(10)-s, (dims.Size.Y-s)/2), func(gtx layout.Context) layout.Dimensions {
		return sidebar.Icon(gtx, "star", s, th.Yellow)
	})
	return dims
}

// shorten cuts a long answer to 24 characters.
func shorten(s string) string {
	if r := []rune(s); len(r) > 24 {
		return strings.TrimSpace(string(r[:23])) + "…"
	}
	return s
}
