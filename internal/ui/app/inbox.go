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

// inboxMax is how many finished tabs the inbox keeps; the oldest goes first.
const inboxMax = 4

// inboxItem is one finished orchestrator tab: a click opens it, the × drops it.
type inboxItem struct {
	ws    string
	act   model.Activity
	title string
	open  widget.Clickable
	close widget.Clickable
}

// inbox lists finished orchestrator tabs, newest last.
type inbox struct{ items []*inboxItem }

// add lists a's tab as title, replacing an earlier entry for it.
func (ib *inbox) add(a model.Activity, title string) {
	ib.drop(a.WorkspaceID)
	ib.items = append(ib.items, &inboxItem{ws: a.WorkspaceID, act: a, title: title})
	if len(ib.items) > inboxMax {
		ib.items = ib.items[len(ib.items)-inboxMax:]
	}
}

// drop removes the entry for workspace ws, if any.
func (ib *inbox) drop(ws string) {
	ib.items = slices.DeleteFunc(ib.items, func(it *inboxItem) bool { return it.ws == ws })
}

// inboxNote is a finished tab from the notifier, waiting for the next frame.
type inboxNote struct {
	act   model.Activity
	title string
}

// queueInbox hands a finished tab to the next frame, from the notifier.
func (u *ui) queueInbox(a model.Activity, title string) {
	u.focusMu.Lock()
	u.inboxNotes = append(u.inboxNotes, inboxNote{a, title})
	u.focusMu.Unlock()
}

// applyInbox moves queued tabs into the inbox and drops the tab on screen,
// which the user is already looking at.
func (u *ui) applyInbox() {
	u.focusMu.Lock()
	notes := u.inboxNotes
	u.inboxNotes = nil
	u.focusMu.Unlock()
	for _, n := range notes {
		u.inbox.add(n.act, n.title)
	}
	if u.nav.workspace != "" {
		u.inbox.drop(u.nav.workspace)
	}
}

// drawInbox stacks the inbox's chips bottom-right in the pane area gtx
// fills. A click on a chip jumps to its tab; the × drops it.
func (u *ui) drawInbox(gtx gl.Context) {
	th := u.th
	is, gap := gtx.Dp(14), gtx.Dp(6)
	pad := image.Pt(gtx.Dp(10), gtx.Dp(6))
	closeW := gtx.Dp(24)
	right := gtx.Constraints.Max.X - gtx.Dp(16)
	y := gtx.Constraints.Max.Y - gtx.Dp(24)
	for i := len(u.inbox.items) - 1; i >= 0; i-- {
		it := u.inbox.items[i]
		if it.close.Clicked(gtx) {
			u.inbox.drop(it.ws)
			continue
		}
		if it.open.Clicked(gtx) {
			u.queueJump(it.act)
			u.inbox.drop(it.ws)
			continue
		}
		call, ts := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, it.title)
		h := max(ts.Y, is) + 2*pad.Y
		w := pad.X + is + gap + ts.X + pad.X + closeW
		at := image.Pt(right-w, y-h)
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
