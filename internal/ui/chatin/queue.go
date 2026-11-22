package chatin

import (
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// queueFold is how many queued messages the strip lists before it starts
// folded to a count.
const queueFold = 2

// queueState is the strip of queued messages above the card.
type queueState struct {
	gone  map[string]bool // removed or sent here, still listed by the chat
	order []string        // a move made here, shown until the chat lists it
	until time.Time       // when order stops standing in for the chat's
	open  bool            // the folded strip was opened
	hov   map[string]*gesture.Hover
	// pending is what this window just queued, shown until the chat lists
	// it, so a message never looks swallowed meanwhile.
	pending []pendingRow
	// joined is the rows the last steer took along, back in view should
	// the chat refuse it.
	joined []string
}

// pendingRow is a queued message the chat has yet to list, until when
// the strip shows it anyway.
type pendingRow struct {
	q     session.QueuedInput
	until time.Time
}

// localID starts a pending row's id: the chat knows no such row, so it
// has no actions.
const localID = "local:"

// queuedNow shows message m in the strip at once, as queued.
func (c *Composer) queuedNow(m proto.ChatSend, now time.Time) {
	q := session.QueuedInput{ID: localID + itoa(len(c.q.pending)), Text: m.Text, Attachments: m.Attachments}
	c.q.pending = append(c.q.pending, pendingRow{q, now.Add(holdFor)})
}

// takeQueue takes the queue's messages a steer joins out of view at
// once, the pending ones too, and keeps their ids for a refusal.
func (c *Composer) takeQueue(env Env, now time.Time) {
	if c.q.gone == nil {
		c.q.gone = map[string]bool{}
	}
	c.q.joined = nil
	for _, q := range joinable(c.shownQueue(env.Snap, now)) {
		c.q.gone[q.ID] = true
		c.q.joined = append(c.q.joined, q.ID)
	}
	c.q.pending = slices.DeleteFunc(c.q.pending, func(p pendingRow) bool { return !command(p.q.Text) })
}

// stripQueue is shownQueue and after it the pending rows the chat has
// yet to list.
func (c *Composer) stripQueue(s *session.Snapshot, now time.Time) []session.QueuedInput {
	qs := c.shownQueue(s, now)
	c.q.pending = slices.DeleteFunc(c.q.pending, func(p pendingRow) bool {
		return now.After(p.until) || slices.ContainsFunc(s.QueuedInputs, func(q session.QueuedInput) bool { return q.Text == p.q.Text })
	})
	for _, p := range c.q.pending {
		qs = append(qs, p.q)
	}
	return qs
}

// Reorder is qs in order's order when order lists exactly qs's ids, else
// qs as it is.
func Reorder(qs []session.QueuedInput, order []string) []session.QueuedInput {
	if len(order) != len(qs) {
		return qs
	}
	out := make([]session.QueuedInput, 0, len(qs))
	for _, id := range order {
		i := slices.IndexFunc(qs, func(q session.QueuedInput) bool { return q.ID == id })
		if i < 0 {
			return qs
		}
		out = append(out, qs[i])
	}
	return out
}

// MoveTo is the ChatQueue Index that moves shown[i] by dir (-1 up, 1
// down) past its neighbor, as the chat's queue all holds them; false
// when there is no neighbor that way.
func MoveTo(all, shown []session.QueuedInput, i, dir int) (int, bool) {
	n := i + dir
	if i < 0 || i >= len(shown) || n < 0 || n >= len(shown) {
		return 0, false
	}
	j := slices.IndexFunc(all, func(q session.QueuedInput) bool { return q.ID == shown[n].ID })
	return j, j >= 0
}

// shownQueue is what the strip lists at now: the chat's queue less the
// rows taken out here, in a move's order until the chat catches up.
func (c *Composer) shownQueue(s *session.Snapshot, now time.Time) []session.QueuedInput {
	var qs []session.QueuedInput
	for _, q := range s.QueuedInputs {
		if !c.q.gone[q.ID] {
			qs = append(qs, q)
		}
	}
	if c.q.order != nil {
		ids := make([]string, len(qs))
		for i, q := range qs {
			ids[i] = q.ID
		}
		if slices.Equal(ids, c.q.order) || now.After(c.q.until) {
			c.q.order = nil
		}
	}
	return Reorder(qs, c.q.order)
}

// queueEvents runs the strip's clicks: Send now, Edit (out of the queue
// and back into the box), Move up and down, Remove, Retry, Resume and
// the fold.
func (c *Composer) queueEvents(gtx layout.Context, env Env) {
	for id := range c.q.gone {
		if !slices.ContainsFunc(env.Snap.QueuedInputs, func(q session.QueuedInput) bool { return q.ID == id }) {
			delete(c.q.gone, id)
		}
	}
	shown := c.shownQueue(env.Snap, gtx.Now)
	if len(shown) == 0 {
		c.q.open = false
	}
	if c.click("qfold").Clicked(gtx) {
		c.q.open = !c.q.open
	}
	if c.click("qresume").Clicked(gtx) {
		c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: "resume"})
	}
	now := "steer"
	if env.Queue {
		now = "now"
	}
	take := func(q session.QueuedInput, action string) {
		if c.q.gone == nil {
			c.q.gone = map[string]bool{}
		}
		c.q.gone[q.ID] = true
		c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: action, ID: q.ID})
	}
	for i, q := range shown {
		switch {
		case c.click("qnow:" + q.ID).Clicked(gtx), c.click("qretry:" + q.ID).Clicked(gtx):
			take(q, now)
		case c.click("qsep:" + q.ID).Clicked(gtx):
			action := "separate"
			if q.Alone {
				action = "together"
			}
			c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: action, ID: q.ID})
		case c.click("qrm:" + q.ID).Clicked(gtx):
			take(q, "remove")
		case c.click("qedit:" + q.ID).Clicked(gtx):
			take(q, "remove")
			c.PullBack(q)
			gtx.Execute(key.FocusCmd{Tag: &c.ed})
		case c.click("qup:" + q.ID).Clicked(gtx):
			c.move(env, shown, i, -1, gtx.Now)
		case c.click("qdown:" + q.ID).Clicked(gtx):
			c.move(env, shown, i, 1, gtx.Now)
		}
	}
}

// move asks the chat to move shown[i] by dir and shows it moved at once.
func (c *Composer) move(env Env, shown []session.QueuedInput, i, dir int, now time.Time) {
	to, ok := MoveTo(env.Snap.QueuedInputs, shown, i, dir)
	if !ok {
		return
	}
	c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: "move", ID: shown[i].ID, Index: to})
	ids := make([]string, len(shown))
	for k, q := range shown {
		ids[k] = q.ID
	}
	ids[i], ids[i+dir] = ids[i+dir], ids[i]
	c.q.order, c.q.until = ids, now.Add(holdFor)
}

// queued is the messages waiting for the turn to end, above the card.
// One muted line says why they wait, or that they go as one message, with
// Resume or Send when they wait for the user. Up to queueFold show as
// rows; more start folded to a count that opens on a click.
func (c *Composer) queued(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	qs := c.stripQueue(env.Snap, gtx.Now)
	if len(qs) == 0 {
		return layout.Dimensions{}
	}
	folds := len(qs) > queueFold
	var kids []layout.FlexChild
	s := QueueStatus(env.Snap, qs, env.Separate)
	if env.Snap.QueueReason == "" && !env.Snap.QueuePaused {
		s = queueHint(env, s)
	}
	if s != "" || env.Snap.QueuePaused {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.queueStatus(gtx, th, env, s)
		}))
	}
	if folds {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.queueHead(gtx, th, env, qs, folds)
		}))
	}
	if !folds || c.q.open {
		for i, q := range qs {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return c.queuedRow(gtx, th, env, q, i, len(qs))
			}))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// queueHead is the folded strip's line: the fold's chevron and the
// count, and how many could not be sent.
func (c *Composer) queueHead(gtx layout.Context, th *theme.Theme, env Env, qs []session.QueuedInput, folds bool) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Dp(28)
	failed := 0
	for _, q := range qs {
		if q.Error != "" {
			failed++
		}
	}
	var right layout.Dimensions
	label := func(gtx layout.Context) layout.Dimensions {
		ws := []layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, itoa(len(qs))+" queued")
		}}
		if failed > 0 {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Readable(th.Red, th.Bg), itoa(failed)+" not sent")
			})
		}
		return row(gtx, gtx.Dp(theme.SpaceS), h, ws...)
	}
	lg := gtx
	lg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-right.Size.X-gtx.Dp(theme.SpaceS)), h)}
	if !folds {
		o := op.Offset(image.Pt(gtx.Dp(theme.SpaceM), 0)).Push(gtx.Ops)
		label(lg)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	b := c.click("qfold")
	kit.Clickable(lg, b, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(itoa(len(qs)) + " queued messages").Add(gtx.Ops)
		pad := gtx.Dp(theme.SpaceS)
		m := op.Record(gtx.Ops)
		chev := "chevron-right"
		if c.q.open {
			chev = "chevron-down"
		}
		d := row(gtx, gtx.Dp(6), h,
			func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, chev, gtx.Dp(12), th.Muted) },
			label)
		call := m.Stop()
		size := image.Pt(d.Size.X+2*pad, h)
		ghostFill(gtx, th, b, size)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// queuedRow is one queued message: the queue icon and its first line,
// and under the pointer Send now, Edit, Move up and down and Remove. One
// that could not be sent shows the alert icon and Retry at all times.
func (c *Composer) queuedRow(gtx layout.Context, th *theme.Theme, env Env, q session.QueuedInput, i, n int) layout.Dimensions {
	h := gtx.Dp(30)
	w := gtx.Constraints.Max.X
	if c.q.hov == nil {
		c.q.hov = map[string]*gesture.Hover{}
	}
	hv := c.q.hov[q.ID]
	if hv == nil {
		hv = new(gesture.Hover)
		c.q.hov[q.ID] = hv
	}
	rect := image.Rectangle{Max: image.Pt(w, h)}
	// An action with no key holds its slot, so the buttons keep their
	// places from row to row: the first has no Move up, the last no Move down.
	type action struct {
		key, icon, tip string
	}
	var acts []action
	if env.Queue {
		up, down := action{"qup:", "chevron-up", "Move up"}, action{"qdown:", "chevron-down", "Move down"}
		if i == 0 {
			up.key = ""
		}
		if i == n-1 {
			down.key = ""
		}
		now := action{"qnow:", "arrow-up", "Send now"}
		if !env.Steers {
			now.tip = "Stop the turn and send this now"
		}
		acts = append(acts, now, action{"qedit:", "pencil", "Edit"}, up, down)
		if env.Separate && !command(q.Text) {
			sep := action{"qsep:", "split", "Send separately"}
			if q.Alone {
				sep.tip = "Send with the others"
			}
			acts = append(acts, sep)
		}
	} else {
		acts = append(acts, action{"qedit:", "pencil", "Edit"})
	}
	acts = append(acts, action{"qrm:", "x", "Remove"})
	failed := q.Error != ""
	if failed {
		acts = []action{{"qrm:", "x", "Remove"}}
	}
	// The row Alt+Up is editing stays lit, its buttons giving way to its keys.
	editing := q.ID == c.qedit.id
	if editing || strings.HasPrefix(q.ID, localID) {
		acts = nil
	}
	over := hv.Update(gtx.Source)
	for _, a := range acts {
		if b := c.click(a.key + q.ID); a.key != "" && (b.Hovered() || gtx.Focused(b)) {
			over = true
		}
	}
	switch {
	case editing:
		paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	case over:
		paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	}
	area := clip.Rect(rect).Push(gtx.Ops)
	hv.Add(gtx.Ops)
	area.Pop()
	// The buttons first, from the right, to know what the text may take.
	var ws []layout.Widget
	if editing {
		send, _ := sendKeys(env)
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Editing · "+send+" saves · Esc cancels")
			})
		})
	}
	if failed && !editing {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			b := c.click("qretry:" + q.ID)
			d := textButton(gtx, th, b, "Retry")
			c.tip(gtx, th, b, d.Size, "Not sent: "+q.Error, true)
			return d
		})
	}
	if over || failed {
		for _, a := range acts {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				if a.key == "" {
					return layout.Dimensions{Size: image.Pt(gtx.Dp(28), gtx.Dp(28))}
				}
				b := c.click(a.key + q.ID)
				d := iconButton(gtx, th, b, a.icon, a.tip)
				c.tip(gtx, th, b, d.Size, a.tip, true)
				return d
			})
		}
	}
	m := op.Record(gtx.Ops)
	bg := gtx
	bg.Constraints = layout.Constraints{Max: image.Pt(w, h)}
	bd := row(bg, gtx.Dp(theme.Space2XS), h, ws...)
	bcall := m.Stop()
	o := op.Offset(image.Pt(w-bd.Size.X-gtx.Dp(1), 0)).Push(gtx.Ops)
	bcall.Add(gtx.Ops)
	o.Pop()
	line, _, _ := strings.Cut(strings.TrimSpace(q.Text), "\n")
	if line == "" && len(q.Attachments) > 0 {
		line = "Image"
	}
	icon, ic, fg := "list-plus", th.Muted, theme.Mix(th.Fg, th.Bg, 0.2)
	if q.Alone && env.Separate {
		icon = "split"
	}
	if failed {
		icon, ic, fg = "circle-alert", th.Readable(th.Red, th.Bg), th.Muted
	}
	if editing {
		icon, ic = "pencil", th.Readable(th.Primary, th.Bg)
	}
	tg := gtx
	tg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-bd.Size.X-gtx.Dp(theme.SpaceS)-gtx.Dp(theme.SpaceM)), h)}
	o = op.Offset(image.Pt(gtx.Dp(theme.SpaceM), 0)).Push(gtx.Ops)
	row(tg, gtx.Dp(theme.SpaceS), h,
		func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, icon, gtx.Dp(14), ic) },
		func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Body), fg, line)
		})
	o.Pop()
	return layout.Dimensions{Size: rect.Max}
}

// command reports whether text is a slash command or a ! shell command,
// which the daemon sends alone.
func command(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "/") || strings.HasPrefix(t, "!")
}

// QueueStatus is the line above queue qs of snapshot s: why it waits, or
// Paused, or, where the daemon joins the queue (separate), how many go as
// one message next; "" for none.
func QueueStatus(s *session.Snapshot, qs []session.QueuedInput, separate bool) string {
	switch {
	case s.QueueReason != "":
		return s.QueueReason
	case s.QueuePaused:
		return "Paused"
	case !separate:
		return ""
	}
	n := 0
	for _, q := range qs {
		if q.Error != "" && n == 0 {
			continue
		}
		if q.Error != "" || q.Alone || command(q.Text) {
			break
		}
		n++
	}
	if n < 2 {
		return ""
	}
	return "The next " + itoa(n) + " go as one message"
}

// queueStatus draws status, muted, with Send where a failed turn holds
// the queue and Resume where Stop did.
func (c *Composer) queueStatus(gtx layout.Context, th *theme.Theme, env Env, status string) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Dp(28)
	var right layout.Dimensions
	if env.Snap.QueuePaused {
		label := "Resume"
		if env.Snap.QueueReason != "" {
			label = "Send"
		}
		m := op.Record(gtx.Ops)
		right = textButton(gtx, th, c.click("qresume"), label)
		call := m.Stop()
		o := op.Offset(image.Pt(w-right.Size.X, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
	}
	lg := gtx
	lg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-right.Size.X-gtx.Dp(theme.SpaceS)-gtx.Dp(theme.SpaceM)), h)}
	o := op.Offset(image.Pt(gtx.Dp(theme.SpaceM), 0)).Push(gtx.Ops)
	row(lg, 0, h, func(gtx layout.Context) layout.Dimensions {
		return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, status)
	})
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}
