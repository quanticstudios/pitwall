package panel

import (
	"image"

	gioevent "gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// confirmAnchor notes where prompt id's Rewind button is, r in the
// element being laid out, while its confirm is open.
func (m *msgState) confirmAnchor(id string, r image.Rectangle) {
	if m.confirm == id {
		m.confirmAt = anchor{el: m.el, r: r.Add(image.Pt(0, m.dy))}
	}
}

// confirmWidth is the rewind confirm's width.
const confirmWidth = 280

// drawConfirm draws the open rewind confirm under its button, over
// everything in the transcript's area of size, x0 the column's left edge:
// what a rewind does, Cancel and Rewind. Esc, Cancel or a press outside
// close it; Rewind sends ActRewind.
func (t *Transcript) drawConfirm(gtx layout.Context, d *cdraw, x0 int, size image.Point) {
	m := &t.msg
	if m.confirm == "" {
		return
	}
	for m.no.Clicked(gtx) {
		m.confirm = ""
	}
	for m.ok.Clicked(gtx) {
		m.acts = append(m.acts, MessageAction{Kind: ActRewind, Turn: m.confirmTurn, Text: m.confirmText})
		m.confirm = ""
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape}, pointer.Filter{Target: &m.confirmBg, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			m.confirm = ""
		}
	}
	r, ok := m.at(m.confirmAt, x0)
	if m.confirm == "" || !ok {
		return
	}
	th := d.th
	pad := gtx.Dp(theme.SpaceM)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(confirmWidth)-2*pad, size.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Rewind to here?")
			},
			func(gtx layout.Context) layout.Dimensions {
				st := d.small(th.Muted)
				return widget.Label{}.Layout(gtx, th.Shaper, st.f, st.size,
					"The chat goes back to before this message, which comes back in the composer to edit and send. The files stay as they are.",
					material(gtx, st.c))
			},
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return kit.Row(gtx, theme.SpaceS,
						func(gtx layout.Context) layout.Dimensions {
							return kit.Button(gtx, th, &m.no, kit.Secondary, kit.Medium, "Cancel")
						},
						func(gtx layout.Context) layout.Dimensions {
							return kit.Button(gtx, th, &m.ok, kit.Primary, kit.Medium, "Rewind")
						})
				})
			})
	})
	box := image.Pt(gtx.Dp(confirmWidth), dims.Size.Y+2*pad)
	p := kit.Place(r, box, image.Rectangle{Max: size}, kit.BelowStart, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceS))
	rect := image.Rectangle{Min: p, Max: p.Add(box)}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	// It takes the presses on it, so they do not reach the transcript.
	area := clip.Rect(rect).Push(gtx.Ops)
	pointer.CursorDefault.Add(gtx.Ops)
	gioevent.Op(gtx.Ops, &m.confirmBg)
	area.Pop()
	at(gtx, p.Add(image.Pt(pad, pad)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return dims })
}
