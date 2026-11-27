package mobile

import (
	"context"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// triageView is the Agents feed: one card per agent on every paired
// machine, what needs you first.
type triageView struct {
	list  layout.List
	cards map[string]*triageCard // by NS(machine, pane)
	// later are the cards swiped left, by key, to the At they were
	// deferred at: they sink below the other needs-you cards until the
	// agent asks something new.
	later map[string]int64
}

// triageCard is one card's widgets and drag.
type triageCard struct {
	open, deny, allow widget.Clickable
	drag              swipe
	sentAt            int64 // the At an answer went out for; its buttons rest until At moves
}

func (v *triageView) card(key string) *triageCard {
	if v.cards == nil {
		v.cards = map[string]*triageCard{}
	}
	c := v.cards[key]
	if c == nil {
		c = new(triageCard)
		v.cards[key] = c
	}
	return c
}

// Feed groups, in the feed's order.
const (
	needsYou = iota
	working
	done
)

// triageItem is one agent tab on a machine, as a feed card shows it.
type triageItem struct {
	Machine phone.Machine
	Tab     remote.Tab
	Group   int
	Later   bool
}

func (it triageItem) key() string { return phone.NS(it.Machine.ID, it.Tab.Pane) }

// triage is every machine's agent tabs as the feed orders them: needs you
// first, oldest first, cards deferred with later after those; then working,
// then done, each in the machines' and the sidebar's order.

func triage(ms []phone.Machine, tabsOf func(string) []remote.Tab, later map[string]int64) []triageItem {
	var out []triageItem
	for _, m := range ms {
		for _, t := range tabsOf(m.ID) {
			if !model.IsAgent(model.Provider(t.Agent)) || t.Agent == string(model.ProviderTerminal) {
				continue
			}
			it := triageItem{Machine: m, Tab: t, Group: groupOf(t)}
			if at, ok := later[it.key()]; ok && it.Group == needsYou && at == t.At {
				it.Later = true
			}
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b triageItem) int {
		if a.Group != b.Group {
			return a.Group - b.Group
		}
		if a.Group != needsYou {
			return 0
		}
		if a.Later != b.Later {
			if a.Later {
				return 1
			}
			return -1
		}
		return compare(a.Tab.At, b.Tab.At)
	})
	return out
}

func compare(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// groupOf is the feed group of t: a tab with inbox items (tabItems, as
// Needs you lists them) needs you, a running turn works, the rest is done.
func groupOf(t remote.Tab) int {
	// An exited tab still needs you when the daemon says so, such as for failed CI.
	if len(t.Inbox) > 0 || !t.Exited && len(tabItems(t)) > 0 {
		return needsYou
	}
	if !t.Exited && phone.Busy(t) {
		return working
	}
	return done
}

// approval is the chat request a card's Allow answers, nil for none.
func approval(t remote.Tab) *remote.Request {
	for i, r := range t.Requests {
		if r.Kind == "approval" {
			return &t.Requests[i]
		}
	}
	return nil
}

// allowable reports whether the feed answers t's prompt with Allow and
// Deny: a terminal agent's prompt the phone may allow, or a chat's approval.
func allowable(t remote.Tab) bool {
	if t.State != string(model.StatePendingApproval) {
		return false
	}
	return t.Answer && t.AllowBlocked == "" || approval(t) != nil
}

// cardLine is the card's one line: what the agent wants, its current step
// or its last result.
func cardLine(t remote.Tab) string {
	or := func(s, alt string) string {
		if s = firstLine(s); s != "" {
			return s
		}
		return alt
	}
	if len(t.Inbox) > 0 && !t.Inbox[0].Kind.ForYou() {
		return or(t.Inbox[0].Text, "Needs you") // failed CI or a stop; answers sort first
	}
	if t.Exited {
		return t.Label
	}
	switch model.AgentState(t.State) {
	case model.StatePendingApproval:
		if r := approval(t); r != nil {
			return "Wants to run " + or(strings.Trim(r.Tool+": "+r.Command, ": "), "a tool")
		}
		return or("Wants to run "+firstLine(t.Detail), "Wants your approval")
	case model.StateAwaitingInput:
		if t.Question != nil {
			return or(t.Question.Prompt, "Asks you something")
		}
		for _, r := range t.Requests {
			if len(r.Questions) > 0 {
				return or(r.Questions[0].Prompt, "Asks you something")
			}
		}
		return or(t.Detail, "Asks you something")
	case model.StatePlanReady:
		return "Plan ready" + prefixed(": ", firstLine(t.Detail))
	case model.StateError:
		return "Stopped" + prefixed(": ", firstLine(t.Detail))
	}
	if phone.Busy(t) {
		return or(t.Spinner, or(t.Detail, or(t.Label, "Working")))
	}
	if t.State == "" {
		return "Idle"
	}
	return or(t.Label, "Done") + prefixed(": ", firstLine(t.Detail))
}

func prefixed(p, s string) string {
	if s == "" {
		return ""
	}
	return p + s
}

// age is how long ago at (Unix nanoseconds) was, as "now", "4m", "3h" or "2d".
func age(at int64, now time.Time) string {
	if at <= 1 {
		return ""
	}
	d := now.Sub(time.Unix(0, at))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// swipe follows one finger across a card: it takes the pointer once the
// finger moves sideways past the slop, and leaves a vertical move to the
// list's scroll.
type swipe struct {
	id               pointer.ID
	start            f32.Point
	dx               float32
	down, took, vert bool
}

// slop is how far a finger moves before the drag is a swipe or a scroll.
const slop unit.Dp = 10

// update reads the pointer on tag, a card width px wide, and returns
// +1 for a swipe right that crossed the threshold, -1 for left, else 0.
func (s *swipe) update(gtx C, tag event.Tag, width int) int {
	verdict := 0
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			return verdict
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			if !s.down {
				*s = swipe{id: e.PointerID, start: e.Position, down: true}
			}
		case pointer.Drag:
			if !s.down || e.PointerID != s.id || s.vert {
				continue
			}
			d := e.Position.Sub(s.start)
			if !s.took {
				sl := float32(gtx.Dp(slop))
				switch {
				case abs(d.Y) > sl && abs(d.Y) >= abs(d.X):
					s.vert = true
					continue
				case abs(d.X) > sl:
					s.took = true
					gtx.Execute(pointer.GrabCmd{Tag: tag, ID: e.PointerID})
				default:
					continue
				}
			}
			s.dx = d.X
		case pointer.Release:
			if e.PointerID == s.id && s.took {
				verdict = swipeVerdict(s.dx, width)
			}
			*s = swipe{}
		case pointer.Cancel:
			*s = swipe{}
		}
	}
}

// swipeVerdict is what a swipe dx px long does on a card width px wide:
// +1 past 30% of the width to the right, -1 to the left, else 0.
func swipeVerdict(dx float32, width int) int {
	t := 0.3 * float32(width)
	switch {
	case width <= 0:
	case dx >= t:
		return 1
	case dx <= -t:
		return -1
	}
	return 0
}

func abs(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// triageLayout draws the feed. It draws in real dp, as the chat does.
func (a *App) triageLayout(gtx C) D {
	gtx.Metric.PxPerDp /= zoom
	gtx.Metric.PxPerSp /= zoom
	th, v := a.th, &a.triage
	fillMax(gtx, th.Bg)
	var ms []phone.Machine
	if a.fleet != nil {
		ms = a.fleet.Machines
	}
	items := triage(ms, a.fleet.TabsOf, v.later)
	n := 0
	for _, it := range items {
		if it.Group == needsYou {
			n++
		}
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Name
	}
	head := func(gtx C) D {
		return layout.Inset{Left: 16, Right: 16, Top: 12, Bottom: 4}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			title := "Nothing needs you"
			if n > 0 {
				title = fmt.Sprintf("%d need%s you", n, map[bool]string{true: "s", false: ""}[n == 1])
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 17, th.Fg, 1, title)
						}),
						layout.Rigid(func(gtx C) D {
							gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(180))
							return label(gtx, th, th.UIFont, 13, th.Muted, 1, strings.Join(names, " · "))
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if a.list.note == "" {
						return D{}
					}
					return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D {
						return label(gtx, th, th.UIFont, 13.5, th.Readable(th.Red, th.Bg), 2, a.list.note)
					})
				}),
			)
		})
	}
	if len(items) == 0 {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(head),
			layout.Flexed(1, func(gtx C) D {
				return layout.Center.Layout(gtx, func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Muted, 0, "No agents are running.") })
			}),
		)
	}
	v.list.Axis = layout.Vertical
	return v.list.Layout(gtx, len(items)+1, func(gtx C, i int) D {
		if i == 0 {
			return head(gtx)
		}
		it := items[i-1]
		bottom := unit.Dp(0)
		if i == len(items) {
			bottom = 16
		}
		return layout.Inset{Left: 12, Right: 12, Top: 8, Bottom: bottom}.Layout(gtx, func(gtx C) D {
			return a.triageCard(gtx, it)
		})
	})
}

// triageCard draws one agent's card: a dot in its state's color, its
// name, machine and age, its line, and for a prompt the phone can answer
// Deny and Allow. A needs-you card swipes: right allows an approval or
// opens a question, left defers it as later. A tap opens its chat.
func (a *App) triageCard(gtx C, it triageItem) D {
	th, v, t := a.th, &a.triage, it.Tab
	c := v.card(it.key())
	act := func(allow bool) {
		c.sentAt = t.At
		if r := approval(t); r != nil {
			id, decision := r.ID, map[bool]string{true: "allow", false: "deny"}[allow]
			a.do(it.Machine.ID, func(ctx context.Context, cl *phone.Client) error {
				return cl.Approve(ctx, t.Pane, id, decision, "")
			}, func(err error) {
				if err != nil {
					c.sentAt = 0
					a.list.note, a.list.notePane, a.list.noteUntil = sentence(err.Error()), "", time.Now().Add(6*time.Second)
				}
			})
			return
		}
		a.answerRow(sidebar.Answer{PaneID: phone.NS(it.Machine.ID, t.Pane), At: t.At, Allow: allow})
	}
	open := func() { a.openChat(phone.NS(it.Machine.ID, t.ID)) }
	answerable := allowable(t)
	live := c.sentAt != t.At
	// why: a tap on Deny or Allow reaches the card's own Clickable around them too.
	denied, allowed := c.deny.Clicked(gtx), c.allow.Clicked(gtx)
	if c.open.Clicked(gtx) && !denied && !allowed {
		defer open()
	}
	if denied && live {
		act(false)
	}
	if allowed && live {
		act(true)
	}
	swipes := it.Group == needsYou
	if swipes {
		switch c.drag.update(gtx, c, gtx.Constraints.Max.X) {
		case 1:
			if answerable && live {
				act(true)
			} else {
				defer open()
			}
		case -1:
			if v.later == nil {
				v.later = map[string]int64{}
			}
			v.later[it.key()] = t.At
		}
	}

	col := sidebar.StateColor(th, model.AgentState(t.State))
	if t.Exited {
		col = th.Muted
	}
	meta := it.Machine.Name
	if it.Group == needsYou {
		meta += prefixed(" · ", age(t.At, gtx.Now))
	}
	name := t.Tab
	if name == "" {
		name = t.Branch
	}
	if name == "" {
		name = t.Repo
	}
	line := cardLine(t)
	lineCol := th.Muted
	if it.Group == needsYou {
		lineCol = th.Fg
	}
	if t.State == string(model.StateError) {
		lineCol = th.Readable(th.Red, th.Surface)
	}

	// The card, recorded to learn its size before the reveal under it.
	m := op.Record(gtx.Ops)
	d := c.open.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		ring := th.BorderSubtle
		if it.Group == needsYou && !it.Later {
			ring = theme.Mix(th.BorderSubtle, col, 0.5)
		}
		fill := th.Surface
		if c.open.Pressed() && !c.drag.took && !c.deny.Pressed() && !c.allow.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.06)
		}
		return box(gtx, fill, ring, 14, layout.Inset{Left: 14, Right: 14, Top: 12, Bottom: 12}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							r := gtx.Dp(4)
							paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, 0, 2*r, 2*r)).Op(gtx.Ops))
							return D{Size: image.Pt(2*r+gtx.Dp(8), 2*r)}
						}),
						layout.Flexed(1, func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 15.5, th.Fg, 1, name)
						}),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
								gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(150))
								return label(gtx, th, th.UIFont, 13, th.Muted, 1, meta)
							})
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 6, Left: 16}.Layout(gtx, func(gtx C) D {
						return label(gtx, th, th.UIFont, 14.5, lineCol, 2, line)
					})
				}),
			}
			if answerable {
				rows = append(rows, layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 12}.Layout(gtx, func(gtx C) D {
						return twoButtons(gtx, th, &c.deny, "Deny", &c.allow, "Allow", live)
					})
				}))
			}
			if swipes {
				rows = append(rows, layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
						return swipeHint(gtx, th, map[bool]string{true: "allow", false: "open"}[answerable])
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	})
	call := m.Stop()

	defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, c)
	dx := 0
	if c.drag.took {
		dx = int(c.drag.dx)
		gtx.Execute(op.InvalidateCmd{})
		right := map[bool]string{true: "Allow", false: "Open"}[answerable]
		reveal(gtx, th, d.Size, dx, right, swipeVerdict(c.drag.dx, d.Size.X) != 0)
	}
	o := op.Offset(image.Pt(dx, 0)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return d
}

// swipeHint is the needs-you card's last line: "← later" and right →.
func swipeHint(gtx C, th *theme.Theme, right string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return hintArrow(gtx, th, "← later") }),
		layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
		layout.Rigid(func(gtx C) D { return hintArrow(gtx, th, right+" →") }),
	)
}

func hintArrow(gtx C, th *theme.Theme, s string) D {
	return label(gtx, th, th.UIFont, 13, th.Muted, 1, s)
}

// reveal is what shows under a card dragged dx px: the right action in
// green at the left for a drag right, Later at the right for a drag left,
// fuller once the drag passes the threshold.
func reveal(gtx C, th *theme.Theme, size image.Point, dx int, right string, past bool) {
	if dx == 0 {
		return
	}
	base, s := th.Green, right
	if dx < 0 {
		base, s = th.Muted, "Later"
	}
	k := float32(0.18)
	if past {
		k = 0.4
	}
	rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(14))
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, base, k), rr.Op(gtx.Ops))
	g := gtx
	g.Constraints = layout.Constraints{Max: size}
	m := op.Record(gtx.Ops)
	ld := label(g, th, weight(th.UIFont, font.SemiBold), 15, th.Readable(base, th.Bg), 1, s)
	call := m.Stop()
	x := gtx.Dp(20)
	if dx < 0 {
		x = size.X - ld.Size.X - gtx.Dp(20)
	}
	defer op.Offset(image.Pt(x, (size.Y-ld.Size.Y)/2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
}
