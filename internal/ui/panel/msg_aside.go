package panel

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/font"
	gioevent "gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// asides is the side-question card: the last question asked about the
// chat and its answer, over the transcript's bottom right, never in it.
type asides struct {
	id, q, answer, err string // the card's question, "" for no card
	waiting            bool
	seq                int
	shut, copy         widget.Clickable
	list               layout.List
	bg                 int // the card's own area
}

// AsideCommand is the composer command that asks a side question, as
// Claude Code's own.
const AsideCommand = "/btw"

// AsideQuestion is the question a composer message asks on the side:
// the text after AsideCommand; false for any other message.
func AsideQuestion(text string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), AsideCommand)
	if !ok || rest != "" && rest[0] != ' ' && rest[0] != '\n' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Ask shows side question q in the card, waiting for its answer, and
// returns the id to send it with.
func (t *Transcript) Ask(q string) string {
	a := &t.msg.aside
	a.seq++
	a.id, a.q, a.answer, a.err, a.waiting = "aside-"+strconv.Itoa(a.seq), q, "", "", true
	a.list.Position = layout.Position{}
	return a.id
}

// Answered fills side question id's answer, or err when there is none;
// one the card no longer shows is dropped.
func (t *Transcript) Answered(id, answer, err string) {
	a := &t.msg.aside
	if id != a.id {
		return
	}
	a.answer, a.err, a.waiting = answer, err, false
	if a.answer == "" && a.err == "" {
		a.err = "Claude gave no answer."
	}
}

// asideWidth is the card's width at most.
const asideWidth = 440

// drawAside draws the side-question card at the bottom right of the
// transcript's area of size: a quiet header with Copy and close, the
// question, then the answer as a reply's markdown, scrolling when long.
func (t *Transcript) drawAside(gtx layout.Context, d *cdraw, size image.Point) {
	a := &t.msg.aside
	if a.id == "" {
		return
	}
	for a.shut.Clicked(gtx) {
		a.id = ""
		return
	}
	for a.copy.Clicked(gtx) {
		t.copy(gtx, "aside:"+a.id, a.answer)
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &a.bg, Kinds: pointer.Press}); !ok {
			break
		}
	}
	th := d.th
	m, pad := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceL)
	w := min(gtx.Dp(asideWidth), size.X-2*m)
	inner := w - 2*pad
	if inner <= 0 {
		return
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(inner, 1<<20)}
	bs := gtx.Dp(hit)
	head := func(gtx layout.Context) layout.Dimensions {
		isz := gtx.Dp(theme.SpaceM + theme.Space2XS)
		at(gtx, image.Pt(0, (bs-isz)/2), func(gtx layout.Context) layout.Dimensions {
			return sidebar.IconPath(gtx, icAskSide, isz, th.Muted)
		})
		at(gtx, image.Pt(isz+gtx.Dp(theme.SpaceS), 0), func(gtx layout.Context) layout.Dimensions {
			call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Side question")
			})
			at(gtx, image.Pt(0, (bs-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
			return td
		})
		x := inner + gtx.Dp(theme.SpaceS) - bs
		at(gtx, image.Pt(x, 0), func(gtx layout.Context) layout.Dimensions {
			return d.iconButton(gtx, &a.shut, icClose, th.Muted, false)
		})
		if a.answer != "" {
			ic, col := icClip, th.Muted
			if t.isCopied("aside:" + a.id) {
				ic, col = icCheck, th.Readable(th.Green, th.Bg)
			}
			at(gtx, image.Pt(x-bs, 0), func(gtx layout.Context) layout.Dimensions {
				return d.iconButton(gtx, &a.copy, ic, col, false)
			})
		}
		return layout.Dimensions{Size: image.Pt(inner, bs)}
	}
	question := func(gtx layout.Context) layout.Dimensions {
		st := d.promptStyle()
		st.f.Weight = font.Medium
		return d.selectable(gtx, t.sel("aside:q:"+a.id), st, a.q)
	}
	answer := func(gtx layout.Context) layout.Dimensions {
		switch {
		case a.waiting:
			return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = inner
				t.live = true
				return kit.Loading(gtx, th, "Asking on the side…")
			})
		case a.err != "":
			st := d.small(th.Readable(th.Red, th.Bg))
			return widget.Label{}.Layout(gtx, th.Shaper, st.f, st.size, a.err, material(gtx, st.c))
		}
		it := &session.Item{ID: "aside:" + a.id, Kind: session.KindAssistantText, Text: a.answer}
		saved := d.proseW
		d.proseW = inner
		defer func() { d.proseW = saved }()
		return d.markdown(gtx, it.ID, t.reply(it, false))
	}
	body := func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS), question, answer)
	}
	hc, hd := measure(g, head)
	bc, bd := measure(g, body)
	maxH := size.Y*6/10 - hd.Size.Y - 2*pad
	bh := min(bd.Size.Y, max(gtx.Dp(theme.Space2XL), maxH))
	h := pad/2 + hd.Size.Y + gtx.Dp(theme.SpaceXS) + bh + pad
	rect := image.Rect(size.X-m-w, size.Y-m-h, size.X-m, size.Y-m)
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusCard), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	area := clip.Rect(rect).Push(gtx.Ops)
	pointer.CursorDefault.Add(gtx.Ops)
	gioevent.Op(gtx.Ops, &a.bg)
	area.Pop()
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	defer clip.UniformRRect(image.Rectangle{Max: rect.Size()}, gtx.Dp(theme.RadiusCard)).Push(gtx.Ops).Pop()
	at(gtx, image.Pt(pad, pad/2), func(gtx layout.Context) layout.Dimensions { hc.Add(gtx.Ops); return hd })
	at(gtx, image.Pt(pad, pad/2+hd.Size.Y+gtx.Dp(theme.SpaceXS)), func(gtx layout.Context) layout.Dimensions {
		if bd.Size.Y <= bh {
			bc.Add(gtx.Ops)
			return bd
		}
		gtx.Constraints = layout.Exact(image.Pt(inner, bh))
		a.list.Axis = layout.Vertical
		return a.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions { return body(gtx) })
	})
}
