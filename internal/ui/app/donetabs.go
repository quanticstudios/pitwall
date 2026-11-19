package app

import (
	"image"
	"slices"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// doneMax is how many notices the window lists; the oldest goes first.
const doneMax = 4

// doneItem is one finished orchestrator tab: a click opens it, the × drops it.
type doneItem struct {
	ws    string
	act   model.Activity
	title string
	open  widget.Clickable
	close widget.Clickable
}

// doneList lists finished orchestrator tabs, newest last.
type doneList struct{ items []*doneItem }

// add lists a's tab as title, replacing an earlier entry for it.
func (d *doneList) add(a model.Activity, title string) {
	d.drop(a.WorkspaceID)
	d.items = append(d.items, &doneItem{ws: a.WorkspaceID, act: a, title: title})
	if len(d.items) > doneMax {
		d.items = d.items[len(d.items)-doneMax:]
	}
}

// drop removes the entry for workspace ws, if any.
func (d *doneList) drop(ws string) {
	d.items = slices.DeleteFunc(d.items, func(it *doneItem) bool { return it.ws == ws })
}

// doneNote is a finished tab from the notifier, waiting for the next frame.
type doneNote struct {
	act   model.Activity
	title string
}

// queueDone hands a finished tab to the next frame, from the notifier.
func (u *ui) queueDone(a model.Activity, title string) {
	u.focusMu.Lock()
	u.doneNotes = append(u.doneNotes, doneNote{a, title})
	u.focusMu.Unlock()
}

// applyDone moves queued tabs into the list and, while the window has
// focus, drops the tab on screen: the user is looking at it.
func (u *ui) applyDone(st *model.State) {
	u.focusMu.Lock()
	notes := u.doneNotes
	u.doneNotes = nil
	u.focusMu.Unlock()
	current := st
	if len(notes) > 0 && u.b != nil {
		// A notifier can enqueue after this frame captured st. Read after
		// draining so a fresh wait is never pruned against that older frame.
		latest := u.b.State()
		current = &latest
	}
	u.doneTabs.items = slices.DeleteFunc(u.doneTabs.items, func(it *doneItem) bool {
		a, keep := noticeActivity(current, it.act)
		it.act = a
		return !keep
	})
	// Answered requests must not consume a slot and evict a live card.
	for _, n := range notes {
		if a, keep := noticeActivity(current, n.act); keep {
			u.doneTabs.add(a, n.title)
		}
	}
	if ws := u.doneView(st); u.winFocused && ws != "" {
		u.doneTabs.drop(ws)
	}
}

// noticeActivity keeps a finished turn, or the latest detail of a pane
// still waiting in the notice's state. Same-state updates do not notify
// again, but their request text must stay current.
func noticeActivity(st *model.State, a model.Activity) (model.Activity, bool) {
	if !model.WaitsOnUser(a) {
		return a, true
	}
	i := slices.IndexFunc(st.Activities, func(c model.Activity) bool { return c.PaneID == a.PaneID })
	if i < 0 || st.Activities[i].State != a.State {
		return a, false
	}
	return st.Activities[i], true
}

// doneView is the tab whose panes are visible, following markSeen's
// policy for covering pages and peeks. A selected but hidden tab is unseen.
func (u *ui) doneView(st *model.State) string {
	if u.nav.peek != "" {
		if p := findPane(st, u.nav.peek); p != nil {
			return p.WorkspaceID
		}
		return ""
	}
	if u.settings.Shown() || u.reader.On() || u.review.on || u.boardOn || u.mon.on {
		return ""
	}
	return u.nav.workspace
}

// drawDone stacks the notices bottom-right in the pane area gtx fills, as
// herdr's toasts: a dot in the state's color, the tab on the first line, what
// happened on a muted second one. A click on a card jumps to its tab; the ×
// drops it.
// noticeBody is a notice's one line: its reply's bold, italic and code in
// their own fonts, the markers gone.
func noticeBody(gtx gl.Context, th *theme.Theme, s string) (op.CallOp, image.Point) {
	txt, styles := mdview.Inline(th, th.UIFont, s)
	m := op.Record(gtx.Ops)
	mat := op.Record(gtx.Ops)
	paint.ColorOp{Color: th.Muted}.Add(gtx.Ops)
	matCall := mat.Stop()
	gtx.Constraints.Min = image.Point{}
	dims := widget.Label{MaxLines: 1, Styles: styles}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), txt, matCall)
	return m.Stop(), dims.Size
}

func (u *ui) drawDone(gtx gl.Context) {
	th := u.th
	dot, gap := gtx.Dp(8), gtx.Dp(12)
	pad := image.Pt(gtx.Dp(16), gtx.Dp(12))
	closeW := gtx.Dp(32)
	margin := gtx.Dp(16)
	right := gtx.Constraints.Max.X - margin
	w := min(gtx.Dp(400), right-margin)
	textWidth := w - (pad.X + dot + gap + closeW)
	if textWidth <= gtx.Dp(80) {
		return
	}
	y := gtx.Constraints.Max.Y - gtx.Dp(24)
	for i := len(u.doneTabs.items) - 1; i >= 0; i-- {
		it := u.doneTabs.items[i]
		if it.close.Clicked(gtx) {
			u.doneTabs.drop(it.ws)
			continue
		}
		if it.open.Clicked(gtx) {
			u.queueJump(it.act)
			u.doneTabs.drop(it.ws)
			continue
		}
		tgtx := gtx
		tgtx.Constraints.Max.X = textWidth
		head, hs := textCall(tgtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, it.title)
		body, bs := noticeBody(tgtx, th, model.NoticeBody(it.act))
		lineGap := gtx.Dp(3)
		h := pad.Y + hs.Y + lineGap + bs.Y + pad.Y
		at := image.Pt(right-w, y-h)
		if at.Y < 0 {
			break // keep older notices queued until there is room to draw them
		}
		y = at.Y - gtx.Dp(8)

		chip := op.Offset(at).Push(gtx.Ops)
		rad := gtx.Dp(8)
		box := image.Pt(w, h)
		state := sidebar.StateColor(th, it.act.State)
		border := th.Border
		if model.WaitsOnUser(it.act) {
			border = theme.Mix(th.Border, state, 0.6)
		}
		paint.FillShape(gtx.Ops, border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
		openSz := image.Pt(w-closeW, h)
		g := gtx
		g.Constraints = gl.Constraints{Min: openSz, Max: openSz}
		it.open.Layout(g, func(g gl.Context) gl.Dimensions {
			if it.open.Hovered() {
				paint.FillShape(g.Ops, theme.Mix(th.Surface, th.Fg, 0.05), clip.UniformRRect(image.Rect(1, 1, openSz.X, openSz.Y-1), rad-1).Op(g.Ops))
			}
			dy := pad.Y + (hs.Y-dot)/2
			paint.FillShape(g.Ops, state, clip.Ellipse{Min: image.Pt(pad.X, dy), Max: image.Pt(pad.X+dot, dy+dot)}.Op(g.Ops))
			x := pad.X + dot + gap
			o := op.Offset(image.Pt(x, pad.Y)).Push(g.Ops)
			head.Add(g.Ops)
			o.Pop()
			o = op.Offset(image.Pt(x, pad.Y+hs.Y+lineGap)).Push(g.Ops)
			body.Add(g.Ops)
			o.Pop()
			return gl.Dimensions{Size: openSz}
		})
		c := gtx
		c.Constraints = gl.Constraints{Min: image.Pt(closeW, h), Max: image.Pt(closeW, h)}
		o := op.Offset(image.Pt(w-closeW, 0)).Push(gtx.Ops)
		it.close.Layout(c, func(c gl.Context) gl.Dimensions {
			cc, cs := textCall(c, th, th.UIFont, th.Sp(theme.Large), th.Muted, "×")
			off := op.Offset(image.Pt((closeW-cs.X)/2, pad.Y+(hs.Y-cs.Y)/2)).Push(c.Ops)
			cc.Add(c.Ops)
			off.Pop()
			return gl.Dimensions{Size: image.Pt(closeW, h)}
		})
		o.Pop()
		chip.Pop()
	}
}
