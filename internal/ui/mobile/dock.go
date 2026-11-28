package mobile

// The list's Needs you section: the inbox dock on the phone. Every paired
// machine's items, each a card with its answers as buttons, Deny and
// Allow for an approval, a question's options with the recommended one
// starred, else Open. The header folds it to one line; the phone keeps
// that in its settings.

import (
	"context"
	"image"
	"log"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// phoneDock is the Needs you section's state.
type phoneDock struct {
	fold widget.Clickable
	btns map[string]*widget.Clickable
	busy map[string]bool // items whose answer is on its way, by key
	list layout.List
}

func (d *phoneDock) btn(key string) *widget.Clickable {
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

// dockRow is one item, with the machine and the tab it is in.
type dockRow struct {
	m  phone.Machine
	t  remote.Tab
	it model.InboxItem
}

// key names the row across frames.
func (r dockRow) key() string {
	return r.m.ID + "\x00" + string(r.it.Kind) + "\x00" + r.t.ID + "\x00" + r.it.PaneID + "\x00" + r.t.Pane
}

// tabItems are what tab t waits on: the daemon's list, or for a daemon
// older than it, what the tab's state says.
func tabItems(t remote.Tab) []model.InboxItem {
	if len(t.Inbox) > 0 {
		return t.Inbox
	}
	it := model.InboxItem{WorkspaceID: t.ID, PaneID: t.Pane, At: t.At, Tab: t.Tab, Text: oneLine(t.Detail)}
	switch model.AgentState(t.State) {
	case model.StatePendingApproval:
		it.Kind = model.InboxApproval
	case model.StateAwaitingInput:
		it.Kind = model.InboxQuestion
	case model.StatePlanReady:
		it.Kind, it.Text = model.InboxPlan, "has a plan to approve"
	case model.StateError:
		it.Kind = model.InboxBlocked
	default:
		return nil
	}
	return []model.InboxItem{it}
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// dockRows are every paired machine's items, in pairing order, the
// answers before failures.
func (a *App) dockRows() (rows []dockRow) {
	if a.fleet == nil {
		return nil
	}
	var failed []dockRow
	for _, m := range a.fleet.Machines {
		for _, t := range a.fleet.TabsOf(m.ID) {
			for _, it := range tabItems(t) {
				if it.Tab == "" {
					it.Tab = t.Tab
				}
				if it.Kind.ForYou() {
					rows = append(rows, dockRow{m, t, it})
				} else {
					failed = append(failed, dockRow{m, t, it})
				}
			}
		}
	}
	return append(rows, failed...)
}

// dockOptions are the question's options the phone answers from the
// tab's screen: one choice, not a multi-select or a typed answer.
func dockOptions(r dockRow) []agent.QuestionOption {
	if q := r.t.Question; r.it.Kind == model.InboxQuestion && q != nil && !q.Multi && len(q.Options) > 0 && len(q.Options) <= 4 {
		return q.Options
	}
	return nil
}

// dockAnswer sends option i of r's question, or Allow or Deny with i -1
// or -2, as the chat's cards and the list's rows do.
func (a *App) dockAnswer(r dockRow, i int) {
	d := &a.dock
	if d.busy == nil {
		d.busy = map[string]bool{}
	}
	key := r.key()
	if d.busy[key] {
		return
	}
	d.busy[key], a.list.note = true, ""
	call := func(ctx context.Context, c *phone.Client) error { return c.Answer(ctx, r.t.Pane, r.t.At, i == -1) }
	if i >= 0 {
		q := r.t.Question
		call = func(ctx context.Context, c *phone.Client) error { return c.Question(ctx, r.t.Pane, q.ID, i, "") }
	}
	a.do(r.m.ID, call, func(err error) {
		delete(d.busy, key)
		if err != nil {
			a.list.note = sentence(err.Error())
			a.list.notePane, a.list.noteAt = phone.NS(r.m.ID, r.t.Pane), r.t.At
			a.list.noteUntil = time.Now().Add(6 * time.Second)
		}
	})
}

// saveDockFold keeps the section folded or open in the phone's settings.
func (a *App) saveDockFold(folded bool) {
	s := a.updates.settings
	s.DockFolded = folded
	a.updates.settings = s
	if err := savePhoneSettings(a.dir, s); err != nil {
		log.Printf("mobile: inbox fold: %v", err)
	}
}

// inbox is the Needs you section at the top of the list: nothing while
// nothing waits, one line while folded.
func (a *App) inbox(gtx C) D {
	th, l, d := a.th, &a.list, &a.dock
	rows := a.dockRows()
	if len(rows) == 0 && l.note == "" {
		return D{}
	}
	for d.fold.Clicked(gtx) {
		a.saveDockFold(!a.updates.settings.DockFolded)
	}
	open := !a.updates.settings.DockFolded && len(rows) > 0
	fillMax(gtx, th.Sidebar) // ponytail: fills the rest too; the tree paints over it
	var ws []layout.Widget
	if l.note != "" {
		ws = append(ws, func(gtx C) D {
			return layout.Inset{Left: pad, Right: pad, Top: 6}.Layout(gtx, func(gtx C) D {
				return small(gtx, th, th.Readable(th.Red, th.Sidebar), l.note)
			})
		})
	}
	ws = append(ws, func(gtx C) D { return a.inboxHead(gtx, rows, open) })
	if open {
		maxH := gtx.Constraints.Max.Y * 45 / 100
		ws = append(ws, func(gtx C) D {
			gtx.Constraints.Max.Y = maxH
			d.list.Axis = layout.Vertical
			return d.list.Layout(gtx, len(rows), func(gtx C, i int) D {
				return layout.Inset{Left: pad, Right: pad, Bottom: 8}.Layout(gtx, func(gtx C) D { return a.inboxCard(gtx, rows[i]) })
			})
		})
	}
	ws = append(ws, func(gtx C) D {
		return layout.Inset{Top: 2}.Layout(gtx, func(gtx C) D { return divider(gtx, th.Border) })
	})
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigids(ws)...)
}

// inboxHead is the section's header: Needs you and its counts, with a
// chevron that folds it; Inbox when only a note shows.
func (a *App) inboxHead(gtx C, rows []dockRow, open bool) D {
	th, d := a.th, &a.dock
	var items []model.InboxItem
	for _, r := range rows {
		items = append(items, r.it)
	}
	title := "Needs you"
	if len(rows) == 0 {
		title = "Inbox"
	}
	counts := strings.Join(model.InboxHead(items), " · ")
	h := gtx.Dp(rowH)
	draw := func(gtx C) D {
		size := image.Pt(gtx.Constraints.Max.X, h)
		if d.fold.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.Rect{Max: size}.Op())
		}
		gtx.Constraints = layout.Exact(size)
		layout.Inset{Left: pad, Right: pad}.Layout(gtx, func(gtx C) D {
			return layout.W.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, font.SemiBold), th.Sp(theme.Body), th.Fg, 1, title)
					}),
					layout.Flexed(1, func(gtx C) D {
						return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
							return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, counts)
						})
					}),
					layout.Rigid(func(gtx C) D {
						if len(rows) == 0 {
							return D{}
						}
						n := gtx.Dp(14)
						name := "chevron-down"
						if open {
							name = "chevron-up"
						}
						sidebar.Icon(gtx, name, n, th.Muted)
						return D{Size: image.Pt(n, n)}
					}),
				)
			})
		})
		return D{Size: size}
	}
	if len(rows) == 0 {
		return draw(gtx)
	}
	return d.fold.Layout(gtx, draw)
}

// inboxCard is one item: its dot, tab and machine, what it wants, and
// its buttons.
func (a *App) inboxCard(gtx C, r dockRow) D {
	th, d := a.th, &a.dock
	dot := th.Yellow
	if !r.it.Kind.ForYou() {
		dot = th.Red
	}
	where := ""
	if a.many() {
		where = r.m.Name
	}
	key := r.key()
	live := !d.busy[key]
	if lk := a.links[r.m.ID]; lk == nil || !lk.online {
		live = false
	}
	openTab := func() { a.openChat(phone.NS(r.m.ID, r.t.ID)) }
	var buttons []layout.Widget
	gap8 := func(gtx C) D { return D{Size: image.Pt(gtx.Dp(8), gtx.Dp(8))} }
	wide := func(c *widget.Clickable, k kit.Kind, s string, star bool) layout.Widget {
		return func(gtx C) D {
			k = k.When(live)
			dims := kit.WideButton(gtx, th, c, k, kit.Large, s)
			if star {
				n := gtx.Dp(14)
				defer op.Offset(image.Pt(dims.Size.X-gtx.Dp(14)-n, (dims.Size.Y-n)/2)).Push(gtx.Ops).Pop()
				sidebar.Icon(gtx, "star", n, th.Yellow)
			}
			return dims
		}
	}
	switch opts := dockOptions(r); {
	case r.it.Kind == model.InboxApproval && r.t.Answer:
		deny, allow := d.btn(key+"\x00deny"), d.btn(key+"\x00allow")
		if deny.Clicked(gtx) {
			a.dockAnswer(r, -2)
		}
		if allow.Clicked(gtx) {
			if r.t.AllowBlocked != "" {
				openTab()
			} else {
				a.dockAnswer(r, -1)
			}
		}
		buttons = append(buttons, func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, wide(deny, kit.Secondary, "Deny", false)),
				layout.Rigid(gap8),
				layout.Flexed(1, wide(allow, kit.Primary, "Allow", false)))
		})
	case len(opts) > 0:
		for i, o := range opts {
			c := d.btn(key + "\x00" + o.Label)
			if c.Clicked(gtx) {
				a.dockAnswer(r, i)
			}
			name, star := model.Recommended(o.Label)
			if len(buttons) > 0 {
				buttons = append(buttons, gap8)
			}
			buttons = append(buttons, wide(c, kit.Secondary, name, star))
		}
	default:
		c := d.btn(key + "\x00open")
		if c.Clicked(gtx) {
			openTab()
		}
		name := "Open"
		if r.it.Kind == model.InboxQuestion || r.it.Kind == model.InboxPlan {
			name = "Answer in chat"
		}
		buttons = append(buttons, func(gtx C) D {
			return kit.WideButton(gtx, th, c, kit.Secondary, kit.Large, name)
		})
	}
	return box(gtx, th.Surface, th.BorderSubtle, theme.RadiusCard, layout.UniformInset(12), func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		ws := []layout.Widget{
			func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						s := gtx.Dp(6)
						paint.FillShape(gtx.Ops, dot, clip.Ellipse(image.Rect(0, 0, s, s)).Op(gtx.Ops))
						return D{Size: image.Pt(s+gtx.Dp(8), s)}
					}),
					layout.Flexed(1, func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, font.SemiBold), th.Sp(theme.Body), th.Fg, 1, r.it.Tab)
					}),
					layout.Rigid(func(gtx C) D {
						if where == "" {
							return D{}
						}
						return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
							gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(120))
							return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, 1, where)
						})
					}))
			},
			gap(4),
			func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, 2, r.it.Text) },
			gap(10),
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigids(append(ws, buttons...))...)
	})
}
