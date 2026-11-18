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
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// doneMax is how many finished tabs the window lists; the oldest goes first.
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
	for _, n := range notes {
		u.doneTabs.add(n.act, n.title)
	}
	if ws := u.doneView(st); u.winFocused && ws != "" {
		u.doneTabs.drop(ws)
	}
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

// drawDone stacks the finished tabs' chips bottom-right in the pane area gtx
// fills. A click on a chip jumps to its tab; the × drops it.
func (u *ui) drawDone(gtx gl.Context) {
	th := u.th
	is, gap := gtx.Dp(14), gtx.Dp(6)
	pad := image.Pt(gtx.Dp(10), gtx.Dp(6))
	closeW := gtx.Dp(24)
	margin := gtx.Dp(16)
	right := gtx.Constraints.Max.X - margin
	textWidth := right - margin - (2*pad.X + is + gap + closeW)
	if textWidth <= 0 {
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
		call, ts := textCall(tgtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, it.title)
		h := max(ts.Y, is) + 2*pad.Y
		w := pad.X + is + gap + ts.X + pad.X + closeW
		at := image.Pt(right-w, y-h)
		if at.Y < 0 {
			break // keep older notices queued until there is room to draw them
		}
		y = at.Y - gtx.Dp(6)

		chip := op.Offset(at).Push(gtx.Ops)
		rad := gtx.Dp(6)
		box := image.Pt(w, h)
		paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
		openSz := image.Pt(w-closeW, h)
		g := gtx
		g.Constraints = gl.Constraints{Min: openSz, Max: openSz}
		it.open.Layout(g, func(g gl.Context) gl.Dimensions {
			o := op.Offset(image.Pt(pad.X, (h-is)/2)).Push(g.Ops)
			sidebar.Icon(g, "check", is, th.Green)
			o.Pop()
			o = op.Offset(image.Pt(pad.X+is+gap, (h-ts.Y)/2)).Push(g.Ops)
			call.Add(g.Ops)
			o.Pop()
			return gl.Dimensions{Size: openSz}
		})
		c := gtx
		c.Constraints = gl.Constraints{Min: image.Pt(closeW, h), Max: image.Pt(closeW, h)}
		o := op.Offset(image.Pt(w-closeW, 0)).Push(gtx.Ops)
		it.close.Layout(c, func(c gl.Context) gl.Dimensions {
			cc, cs := textCall(c, th, th.UIFont, th.Sp(theme.Small), th.Muted, "×")
			off := op.Offset(image.Pt((closeW-cs.X)/2, (h-cs.Y)/2)).Push(c.Ops)
			cc.Add(c.Ops)
			off.Pop()
			return gl.Dimensions{Size: image.Pt(closeW, h)}
		})
		o.Pop()
		chip.Pop()
	}
}
