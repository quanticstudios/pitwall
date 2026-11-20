package panel

import (
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A denial can carry a reason, as Claude Code's own "No, and tell Claude
// what to do differently": Deny or 3 on an approval, Refine or 2 on a plan,
// opens a one-line field in the card. Enter sends the denial with what it
// holds, and Esc sends it without.

// openReason opens request id's reason field and puts the keys in it.
// With the field already open, Deny sends what it holds.
func (t *Transcript) openReason(id string) {
	if id == "" || t.sent[id] {
		return
	}
	if e := t.reasons[id]; e != nil {
		t.decideWhy(id, "deny", strings.TrimSpace(e.Text()))
		return
	}
	e := &widget.Editor{SingleLine: true, Submit: true}
	t.reasons[id] = e
	t.typeIn = e
}

// reasonEvents takes the reason fields' Enter and Esc, and drops the
// fields of requests no longer pending.
func (t *Transcript) reasonEvents(gtx layout.Context, pending map[string]bool) {
	for id, e := range t.reasons {
		if !pending[id] {
			delete(t.reasons, id)
			continue
		}
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				t.decideWhy(id, "deny", strings.TrimSpace(e.Text()))
			}
		}
		for {
			ev, ok := gtx.Event(key.Filter{Focus: e, Name: key.NameEscape})
			if !ok {
				break
			}
			if k, ok := ev.(key.Event); ok && k.State == key.Press {
				t.decideWhy(id, "deny", "")
			}
		}
	}
}

// Typing reports whether a reason field is open and unsent: it holds the
// keys, so the window's composer leaves them to it.
func (t *Transcript) Typing() bool {
	for id := range t.reasons {
		if !t.sent[id] {
			return true
		}
	}
	return false
}

// reasonField is request id's open reason field, nil while it is shut.
func (t *Transcript) reasonField(id string) *widget.Editor { return t.reasons[id] }

// reasonBox draws a reason field as the composer's field in small: a
// control-radius box on the page's background, its border lit while it
// has the keys, hint in it while it is empty.
func (d *cdraw) reasonBox(gtx layout.Context, e *widget.Editor, hint string) layout.Dimensions {
	th := d.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(hit)
	border := th.BorderStrong
	if gtx.Focused(e) {
		border = theme.Mix(th.Surface, th.Primary, 0.6)
	}
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl), th.Bg, border)
	px := gtx.Dp(theme.SpaceM)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(max(0, w-2*px), 0), Max: image.Pt(max(0, w-2*px), h)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		dims := e.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), material(gtx, th.Fg), material(gtx, d.selColor()))
		if e.Len() == 0 {
			label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, hint, 1)
		}
		return dims
	})
	at(gtx, image.Pt(px, (h-dims.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return dims })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// reasonRow is a card's open reason field with its send button after it:
// Deny for an approval, Refine for a plan.
func (d *cdraw) reasonRow(gtx layout.Context, e *widget.Editor, c *widget.Clickable, hint, send string) layout.Dimensions {
	gap := gtx.Dp(theme.SpaceS)
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, c, false, send, "", true) })
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-bd.Size.X-gap), gtx.Constraints.Max.Y)}
	fd := d.reasonBox(g, e, hint)
	at(gtx, image.Pt(fd.Size.X+gap, (fd.Size.Y-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, max(fd.Size.Y, bd.Size.Y))}
}
