package sidebar

import (
	"image"
	"image/color"
	"slices"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// answerH is the answer buttons' height in dp.
const answerH = 18

// answering reports whether tab id's row, with its own activity a, shows
// Allow and Deny. A tab with agent sub-rows answers on the asking one's,
// and a worker on its own sub-row.
func (s *Sidebar) answering(v *view, id string, ghost bool, a *model.Activity) bool {
	return s.Answers && !ghost && a != nil && remote.Answerable(*a) && v.agents[id] == nil
}

// planning reports whether tab id's row, with its own activity a, shows
// Request changes… for its agent's plan. A tab with agent sub-rows has
// none.
func (s *Sidebar) planning(v *view, id string, ghost bool, a *model.Activity) bool {
	return !ghost && a != nil && a.State == model.StatePlanReady && v.agents[id] == nil
}

// answerClicks turns clicks on row r's Allow and Deny into Answer events
// for the prompt the row shows now, one on its other answers into Press
// events, one on Request changes… into the field for the reply, and one on
// Reply into the sheet, or the tab when the screen shows a menu of options.
func (s *Sidebar) answerClicks(gtx layout.Context, v *view, ws string, r *rowState) {
	for r.reply.Clicked(gtx) {
		if a := v.ownActivity(ws); s.replyable(v, ws, false, a) {
			if a.Menu {
				s.click(v, ws, 0)
			} else {
				s.openReply(ws, a.PaneID)
			}
		}
	}
	for r.plan.Clicked(gtx) {
		if a := v.ownActivity(ws); s.planning(v, ws, false, a) {
			s.startReply(ws, a.PaneID)
		}
	}
	for _, b := range []struct {
		c     *widget.Clickable
		allow bool
	}{{&r.allow, true}, {&r.deny, false}} {
		for b.c.Clicked(gtx) {
			if a := v.ownActivity(ws); s.answering(v, ws, false, a) {
				s.events = append(s.events, Answer{PaneID: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: b.allow})
			}
		}
	}
	var a *model.Activity
	if v.agents[ws] == nil {
		a = v.ownActivity(ws)
	}
	s.choiceClicks(gtx, &r.choices, a, func() { s.click(v, ws, 0) })
}

// maxChoices is how many answers a row keeps buttons for: an agent's digit
// keys stop at 9.
const maxChoices = 9

// choices are a row's buttons for its activity's Buttons, and the +N one
// that opens the tab when they do not all fit.
type choices struct {
	btn  [maxChoices]widget.Clickable
	more widget.Clickable
	// shown is the answers as last drawn, and held the one each button had
	// when the pointer went down on it.
	shown choiceSet
	held  [maxChoices]choiceSet
}

// choiceSet is the buttons of one activity, which a click belongs to.
type choiceSet struct {
	pane   string
	at     int64
	labels []string
}

func choicesOf(a *model.Activity) choiceSet {
	if a == nil {
		return choiceSet{}
	}
	return choiceSet{a.PaneID, a.UpdatedAt.UnixNano(), a.Buttons}
}

// same reports whether button i of cur is the button c had: a click that
// began on a question that has since changed does not answer the new one.
func (c choiceSet) same(cur choiceSet, i int) bool {
	return c.pane == cur.pane && c.at == cur.at && i < len(c.labels) && i < len(cur.labels) && c.labels[i] == cur.labels[i]
}

func (c *choices) hovered() bool {
	for i := range c.btn {
		if c.btn[i].Hovered() {
			return true
		}
	}
	return c.more.Hovered()
}

// drain drops clicks on c's buttons.
func (c *choices) drain(gtx layout.Context) {
	for i := range c.btn {
		for c.btn[i].Clicked(gtx) {
		}
	}
	for c.more.Clicked(gtx) {
	}
}

// choiceClicks turns clicks on c's buttons into Press events for a, which
// may be nil, and one on +N into open. A click whose activity or label changed
// after the press is dropped.
func (s *Sidebar) choiceClicks(gtx layout.Context, c *choices, a *model.Activity, open func()) {
	cur := choicesOf(a)
	for i := range c.btn {
		for c.btn[i].Clicked(gtx) {
			from := c.held[i]
			if from.pane == "" { // pressed and released within this frame
				from = c.shown
			}
			if from.same(cur, i) {
				s.events = append(s.events, Press{PaneID: cur.pane, At: cur.at, Label: cur.labels[i]})
			}
			c.held[i] = choiceSet{}
		}
		switch {
		case !c.btn[i].Pressed():
			c.held[i] = choiceSet{}
		case c.held[i].pane == "":
			c.held[i] = c.shown
		}
	}
	c.shown = cur
	for c.more.Clicked(gtx) {
		open()
	}
}

// answerBtns are the clickables an answer area draws with; left zero, it
// draws without input, to measure.
type answerBtns struct {
	allow, deny, plan, reply *widget.Clickable
	choices                  *choices
}

// choice is button i of b's choices, nil without input.
func (b answerBtns) choice(i int) *widget.Clickable {
	if b.choices == nil {
		return nil
	}
	return &b.choices.btn[i]
}

// answerSize is the size answerArea takes in gtx's width.
func answerSize(gtx layout.Context, th *theme.Theme, a model.Activity, base color.NRGBA) image.Point {
	m := op.Record(gtx.Ops)
	d := answerArea(gtx, th, answerBtns{}, a, base)
	m.Stop()
	return d.Size
}

// answerArea draws what a row offers for a in gtx's width, one line high:
// an approval's Allow and Deny, a plan's buttons, a question's options, or
// else its Reply.
func answerArea(gtx layout.Context, th *theme.Theme, b answerBtns, a model.Activity, base color.NRGBA) layout.Dimensions {
	h := answerHeight(gtx, th)
	switch a.State {
	case model.StatePendingApproval:
		return answerButtons(gtx, th, b, a, base)
	case model.StatePlanReady:
		// why: both whole do not fit a default sidebar's row.
		changes := "Request changes…"
		if len(a.Buttons) > 0 {
			changes = "Changes…"
		}
		return hrowFit(gtx, h, gtx.Dp(4), append(firstButton(th, b, a, base, a.Buttons, th.Green),
			item{opt: len(a.Buttons) > 0, w: pillButton(b.plan, th, base, h, changes, th.Purple, false)})...)
	}
	switch {
	case len(a.Buttons) > 0:
		return choicePills(gtx, th, b, a.Buttons, base)
	case a.Menu:
		return pillButton(b.reply, th, base, h, "Open tab", th.Yellow, false)(gtx)
	}
	return pillButton(b.reply, th, base, h, "Reply", th.Yellow, false)(gtx)
}

// firstButton is a's first extra answer as a pill in col reading texts[0],
// if it has one.
func firstButton(th *theme.Theme, b answerBtns, a model.Activity, base color.NRGBA, texts []string, col color.NRGBA) []item {
	if len(a.Buttons) == 0 {
		return nil
	}
	return []item{{w: func(gtx layout.Context) layout.Dimensions {
		return pillButton(b.choice(0), th, base, answerHeight(gtx, th), texts[0], col, false)(gtx)
	}}}
}

// answerButtons draws a pending approval's Allow and Deny, small pills in
// green and red, with Always before them where the prompt offers Allow
// always, so they stay put when it appears; it gives way when there is no
// room. The one the decision model advises is outlined, so its recommendation
// in the pill above points at a button.
func answerButtons(gtx layout.Context, th *theme.Theme, b answerBtns, a model.Activity, base color.NRGBA) layout.Dimensions {
	h := answerHeight(gtx, th)
	always := firstButton(th, b, a, base, []string{"Always"}, th.Green)
	for i := range always {
		always[i].opt = true
	}
	return hrowFit(gtx, h, gtx.Dp(4), append(always,
		item{w: pillButton(b.allow, th, base, h, "Allow", th.Green, a.Advice == "allow")},
		item{w: pillButton(b.deny, th, base, h, "Deny", th.Red, a.Advice == "deny")})...)
}

// minPill is how narrow a question's option may get, in dp, before it
// leaves for the +N button.
const minPill = 48

// choicePills draws a question's options, yellow like its Reply, in gtx's
// width and on one line, so no option can push the row. Long labels shrink
// to an ellipsis, and those that still do not fit leave for a +N button that
// opens the tab.
func choicePills(gtx layout.Context, th *theme.Theme, b answerBtns, labels []string, base color.NRGBA) layout.Dimensions {
	h, gap := answerHeight(gtx, th), gtx.Dp(4)
	measure := func(text string) int {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(1<<20, h)}
		d := pillButton(nil, th, base, h, text, th.Yellow, false)(g)
		m.Stop()
		return d.Size.X
	}
	labels = labels[:min(len(labels), maxChoices)]
	natural := make([]int, len(labels))
	for i, l := range labels {
		natural[i] = measure(l)
	}
	shown, widest := fitPills(natural, gtx.Constraints.Max.X, gap, gtx.Dp(minPill), measure("+"+strconv.Itoa(len(labels))))
	x := 0
	place := func(c *widget.Clickable, text string, w int) {
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w, h)}
		off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		d := pillButton(c, th, base, h, text, th.Yellow, false)(g)
		off.Pop()
		x += d.Size.X + gap
	}
	for i := range shown {
		place(b.choice(i), labels[i], min(natural[i], widest))
	}
	if shown < len(labels) {
		var more *widget.Clickable
		if b.choices != nil {
			more = &b.choices.more
		}
		place(more, "+"+strconv.Itoa(len(labels)-shown), 1<<20)
	}
	return layout.Dimensions{Size: image.Pt(max(x-gap, 0), h)}
}

// fitPills decides how a row of pills of natural widths fits in w with gap
// between them: how many show, and the width none may pass. The widest give
// way first; a pill that would get narrower than least leaves for a +N of
// width more.
func fitPills(natural []int, w, gap, least, more int) (shown, widest int) {
	for shown = len(natural); shown > 0; shown-- {
		room := w - gap*(shown-1)
		if shown < len(natural) {
			room -= more + gap
		}
		widest = capWidth(natural[:shown], room)
		if widest >= least || widest >= slices.Max(natural[:shown]) {
			return shown, widest
		}
	}
	return 0, 0
}

// capWidth is the width at which pills of the given natural widths, each cut
// to at most it, fill room.
func capWidth(natural []int, room int) int {
	left, n := room, len(natural)
	sorted := slices.Sorted(slices.Values(natural))
	for _, nw := range sorted {
		if nw > left/n {
			break
		}
		left -= nw
		n--
		if n == 0 {
			return sorted[len(sorted)-1]
		}
	}
	return left / n
}

// pillButton is one of a row's small answer pills, h high, in col; an
// advised one is outlined. With a nil c it draws without input.
func pillButton(c *widget.Clickable, th *theme.Theme, base color.NRGBA, h int, text string, col color.NRGBA, advised bool) layout.Widget {
	draw := func(gtx layout.Context) layout.Dimensions {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Max.X = max(gtx.Constraints.Max.X-gtx.Dp(16), 0)
		d := label(g, th, semibold(th.UIFont), th.Sp(theme.Caption), col, text)
		call := m.Stop()
		sz := image.Pt(d.Size.X+gtx.Dp(16), h)
		fill := float32(0.14)
		if c != nil && c.Hovered() {
			fill = 0.28
		}
		rect := image.Rectangle{Max: sz}
		paint.FillShape(gtx.Ops, theme.Mix(base, col, fill), clip.UniformRRect(rect, h/2).Op(gtx.Ops))
		if advised {
			paint.FillShape(gtx.Ops, theme.Mix(base, col, 0.7), clip.Stroke{Path: clip.UniformRRect(rect, h/2).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		}
		off := op.Offset(sz.Sub(d.Size).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		return layout.Dimensions{Size: sz}
	}
	if c == nil {
		return draw
	}
	return func(gtx layout.Context) layout.Dimensions { return clickable(gtx, c, draw) }
}

// answerRoom is what line 2 keeps for its own items before a row's answers:
// the fold chip, or the inbox's group tag.
func answerRoom(gtx layout.Context, v *view, id string, subs bool) int {
	room := 0
	if subs {
		room += gtx.Dp(44)
	}
	if v.inbox && v.groupOf(id) != "" {
		room += gtx.Dp(72)
	}
	return room
}
