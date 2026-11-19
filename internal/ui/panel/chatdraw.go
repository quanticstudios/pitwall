package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cdraw draws one frame of a Transcript.
type cdraw struct {
	t           *Transcript
	th          *theme.Theme
	s           *session.Snapshot
	sec         float64 // seconds since the transcript opened, for spinners
	col, proseW int     // the column's width, and reply prose's
}

// avgChar is the width per character that puts reply prose at about 96
// characters: 680 dp at 14 sp, scaling with the UI size.
func (d *cdraw) avgChar(gtx layout.Context) int {
	return gtx.Sp(d.th.Sp(theme.Large)) * 680 / (14 * proseChars)
}

// --- small pieces ---

func (d *cdraw) selColor() color.NRGBA { return theme.Mix(d.th.Bg, d.th.Primary, 0.42) }

// selectable draws txt selectable in st, wrapped to the width.
func (d *cdraw) selectable(gtx layout.Context, sl *widget.Selectable, st tstyle, txt string) layout.Dimensions {
	sl.SetText(txt)
	sl.LineHeightScale = st.lh
	gtx.Constraints.Min = image.Point{}
	return sl.Layout(gtx, d.th.Shaper, st.f, st.size, material(gtx, st.c), material(gtx, d.selColor()))
}

// lbl draws one line of txt, cut with an ellipsis.
func (d *cdraw) lbl(gtx layout.Context, f font.Font, size unit.Sp, c color.NRGBA, txt string) layout.Dimensions {
	return label(gtx, d.th, f, size, c, txt, 1)
}

// at draws w at p and returns its size.
func at(gtx layout.Context, p image.Point, w layout.Widget) layout.Dimensions {
	o := op.Offset(p).Push(gtx.Ops)
	defer o.Pop()
	return w(gtx)
}

// measure records w and returns the call that draws it and its size.
func measure(gtx layout.Context, w layout.Widget) (op.CallOp, layout.Dimensions) {
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	return m.Stop(), dims
}

// vstack lays ws top to bottom, gap apart, and is as wide as the widest.
func vstack(gtx layout.Context, gap int, ws ...layout.Widget) layout.Dimensions {
	y, w := 0, 0
	for _, wd := range ws {
		if wd == nil {
			continue
		}
		g := gtx
		g.Constraints.Min = image.Point{}
		call, dims := measure(g, wd)
		if dims.Size.Y == 0 {
			continue
		}
		if y > 0 {
			y += gap
		}
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		y += dims.Size.Y
		w = max(w, dims.Size.X)
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// icon draws a sidebar icon size dp at p.
func icon(gtx layout.Context, p image.Point, name string, size unit.Dp, c color.NRGBA) {
	at(gtx, p, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, name, gtx.Dp(size), c) })
}

// ghost is a quiet button at least 28 dp each way: an icon, a label, the
// Hover fill under the pointer and Pressed while held. A copy button
// shows a green check and Copied for a moment after its click.
func (d *cdraw) ghost(gtx layout.Context, c *widget.Clickable, ic, txt string, copied bool) layout.Dimensions {
	th := d.th
	col := th.Muted
	if copied {
		ic, col = "check", th.Readable(th.Green, th.Bg)
		if txt != "" {
			txt = "Copied"
		}
	}
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		hh, pad, isz := gtx.Dp(hit), gtx.Dp(theme.SpaceS), gtx.Sp(th.Sp(theme.Small))
		call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			if txt == "" {
				return layout.Dimensions{}
			}
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), col, txt)
		})
		w := pad*2 + td.Size.X
		if ic != "" {
			w += isz
			if txt != "" {
				w += gtx.Dp(6)
			}
		}
		if txt == "" {
			w = hh
		}
		switch {
		case c.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rect(0, 0, w, hh), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case c.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, w, hh), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		x := pad
		if txt == "" {
			x = (hh - isz) / 2
		}
		if ic != "" {
			at(gtx, image.Pt(x, (hh-isz)/2), func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, isz, col) })
			x += isz + gtx.Dp(6)
		}
		o := op.Offset(image.Pt(x, (hh-td.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
}

// keycap is a key's label in a small raised box, as on approval buttons.
func (d *cdraw) keycap(gtx layout.Context, k string, onPrimary bool) layout.Dimensions {
	th := d.th
	fill, ring, fg := th.SurfaceSecondary, th.BorderStrong, th.Fg
	if onPrimary {
		fill, ring, fg = theme.Mix(th.Primary, th.Bg, 0.3), theme.Mix(th.Primary, th.Bg, 0.42), th.Fg
	}
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), fg, k)
	})
	w, h := max(td.Size.X+gtx.Dp(10), gtx.Dp(18)), gtx.Dp(18)
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(5), fill, ring)
	o := op.Offset(image.Pt((w-td.Size.X)/2, (h-td.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// keyButton is a card's 28 dp button with its key on a keycap.
func (d *cdraw) keyButton(gtx layout.Context, c *widget.Clickable, primary bool, txt, k string, on bool) layout.Dimensions {
	th := d.th
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		fill, fg := th.SurfaceElevated, th.Fg
		if primary {
			fill, fg = th.Primary, th.OnPrimary
		}
		if !on {
			defer paint.PushOpacity(gtx.Ops, 0.5).Pop()
		} else if c.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.16)
		} else if c.Hovered() {
			fill = theme.Mix(fill, th.Fg, 0.08)
		}
		h, pad := gtx.Dp(hit), gtx.Dp(theme.SpaceM)
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), fg, txt)
		})
		var kc op.CallOp
		var kd layout.Dimensions
		if k != "" {
			kc, kd = measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.keycap(gtx, k, primary) })
		}
		w := pad*2 + ld.Size.X
		if k != "" {
			w += gtx.Dp(theme.SpaceS) + kd.Size.X
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		o := op.Offset(image.Pt(pad, (h-ld.Size.Y)/2)).Push(gtx.Ops)
		lc.Add(gtx.Ops)
		o.Pop()
		if k != "" {
			o := op.Offset(image.Pt(pad+ld.Size.X+gtx.Dp(theme.SpaceS), (h-kd.Size.Y)/2)).Push(gtx.Ops)
			kc.Add(gtx.Ops)
			o.Pop()
		}
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

// hrow lays ws left to right, gap apart, centered on the tallest.
func hrow(gtx layout.Context, gap int, ws ...layout.Widget) layout.Dimensions {
	calls := make([]op.CallOp, len(ws))
	dims := make([]layout.Dimensions, len(ws))
	h := 0
	for i, w := range ws {
		g := gtx
		g.Constraints.Min = image.Point{}
		calls[i], dims[i] = measure(g, w)
		h = max(h, dims[i].Size.Y)
	}
	x, base := 0, 0
	for i := range ws {
		if dims[i].Size.X == 0 {
			continue
		}
		if x > 0 {
			x += gap
		}
		y := (h - dims[i].Size.Y) / 2
		if base == 0 {
			base = h - (y + dims[i].Size.Y - dims[i].Baseline)
		}
		o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		calls[i].Add(gtx.Ops)
		o.Pop()
		x += dims[i].Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h), Baseline: base}
}

// centerBase is the baseline, from the top of a row h tall, that centers
// text dims in it: the one baseline the row's other texts sit on.
func centerBase(h int, dims layout.Dimensions) int {
	return (h-dims.Size.Y)/2 + dims.Size.Y - dims.Baseline
}

// onBase is the top at which text dims sits on baseline base.
func onBase(base int, dims layout.Dimensions) int {
	return base - (dims.Size.Y - dims.Baseline)
}

// --- rows ---

// block draws one list row of transcript items.
func (d *cdraw) block(gtx layout.Context, b block) layout.Dimensions {
	switch b.kind {
	case blockPrompt:
		return d.prompt(gtx, &d.s.Content[b.lo])
	case blockSystem:
		it := &d.s.Content[b.lo]
		return d.selectable(gtx, d.t.sel(it.ID), d.small(d.th.Muted), it.Text)
	}
	return layout.Dimensions{} // a turn draws as its rows, see buildRows
}

// prompt is a user prompt's bubble on the right, its images under the
// text.
func (d *cdraw) prompt(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	maxW := d.col * 84 / 100
	px, py := gtx.Dp(14), gtx.Dp(10)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, maxW-2*px), gtx.Constraints.Max.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions { return d.promptText(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.attachments(gtx, it) },
		)
	})
	bw, bh := dims.Size.X+2*px, dims.Size.Y+2*py
	x := d.col - bw
	r, small := gtx.Dp(theme.RadiusCard), gtx.Dp(theme.SpaceXS)
	rr := clip.RRect{Rect: image.Rect(x, 0, x+bw, bh), NW: r, NE: r, SE: small, SW: r}
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
	o := op.Offset(image.Pt(x+px, py)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(d.col, bh)}
}

// promptText is a prompt's selectable text. Each [Image #N] token draws
// as a chip: the text widens the token to make room for the chip's icon
// and padding, and a copy gives back the token as typed.
func (d *cdraw) promptText(gtx layout.Context, it *session.Item) layout.Dimensions {
	txt := mdview.Detab(strings.TrimRight(it.Text, "\n "))
	if txt == "" {
		return layout.Dimensions{}
	}
	st := d.promptStyle()
	lead, trail := d.chipPad(gtx, st)
	ct := chipText(txt, lead, trail)
	sl := d.t.sel(it.ID)
	d.copyAs(gtx, sl, ct)
	dims := d.selectable(gtx, sl, st, ct.text)
	var rg []widget.Region
	for _, c := range ct.chips {
		rg = sl.Regions(c.label[0], c.label[1], rg[:0])
		if len(rg) == 0 {
			continue
		}
		d.imageChip(gtx, rg[0], c, st)
	}
	return dims
}

// imageChip draws an image token's chip around its label's region r: the
// blue chip colors, a 1 em image icon in the room the padding left, and
// the label redrawn in blue on the line's baseline.
func (d *cdraw) imageChip(gtx layout.Context, r widget.Region, c chip, st tstyle) {
	th := d.th
	bg, fg := th.Chip(th.Blue, th.SurfaceSecondary)
	em := gtx.Sp(st.size)
	base := r.Bounds.Max.Y - r.Baseline
	lead := gtx.Dp(6) + em + gtx.Dp(4)
	box := image.Rect(r.Bounds.Min.X-lead, base-em*78/100-gtx.Dp(2), r.Bounds.Max.X+gtx.Dp(6), base+em*22/100+gtx.Dp(2))
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(box, gtx.Dp(5)).Op(gtx.Ops))
	xh := em / 2
	at(gtx, image.Pt(box.Min.X+gtx.Dp(6), base-xh/2-em/2), func(gtx layout.Context) layout.Dimensions {
		return sidebar.Icon(gtx, "image", em, fg)
	})
	call, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, st.f, st.size, c.name, material(gtx, fg))
	})
	at(gtx, image.Pt(r.Bounds.Min.X, base-(ld.Size.Y-ld.Baseline)), func(gtx layout.Context) layout.Dimensions {
		call.Add(gtx.Ops)
		return ld
	})
}

// attachments are a prompt's images as thumbnails, numbered as the
// prompt's [Image #N] tokens count them, and its files as chips.
func (d *cdraw) attachments(gtx layout.Context, it *session.Item) layout.Dimensions {
	if len(it.Attachments) == 0 {
		return layout.Dimensions{}
	}
	var ws []layout.Widget
	refs := imageRefs(it.Text)
	n := 0
	for i := range it.Attachments {
		a := &it.Attachments[i]
		if a.Kind == "image" {
			n++
			no := imageNo(refs, n)
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.thumbnail(gtx, it.ID, a, no) })
			continue
		}
		name := a.Name
		if a.Kind == "workspace-file" {
			name = a.Path
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.fileChip(gtx, "file", name) })
	}
	return hrow(gtx, gtx.Dp(theme.SpaceS), ws...)
}

// thumbnail is image attachment a, 96 dp tall, with its number.
func (d *cdraw) thumbnail(gtx layout.Context, item string, a *session.Attachment, n int) layout.Dimensions {
	th := d.th
	if a.Data == "" { // a snapshot's copy, until the item comes whole (Transcript.Full)
		return d.fileChip(gtx, "image", a.Name)
	}
	tb := d.t.thumbs.snapshot(d.t.thumbs.get(item+"/"+a.ID, a, nil))
	if tb.ready && tb.size == (image.Point{}) {
		return d.fileChip(gtx, "image", a.Name)
	}
	h := gtx.Dp(96)
	w := gtx.Dp(72)
	if tb.ready {
		w = min(max(h*tb.size.X/tb.size.Y, gtx.Dp(48)), gtx.Dp(180))
	} else {
		d.t.live = true
	}
	r := image.Rect(0, 0, w, h)
	rad := gtx.Dp(theme.RadiusPopover)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(r, rad).Op(gtx.Ops))
	if tb.ready {
		cl := clip.UniformRRect(r, rad).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(r.Size())
		widget.Image{Src: tb.img, Fit: widget.Cover, Position: layout.Center}.Layout(g)
		cl.Pop()
	}
	paint.FillShape(gtx.Ops, th.BorderStrong, clip.Stroke{Path: clip.UniformRRect(r, rad).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), th.Fg, "#"+strconv.Itoa(n))
	})
	badge := image.Rect(gtx.Dp(4), h-gtx.Dp(4)-td.Size.Y-gtx.Dp(2), gtx.Dp(4)+td.Size.X+gtx.Dp(8), h-gtx.Dp(4))
	bg := th.Bg
	bg.A = 0xd9
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(badge, gtx.Dp(4)).Op(gtx.Ops))
	o := op.Offset(badge.Min.Add(image.Pt(gtx.Dp(4), gtx.Dp(1)))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: r.Size()}
}

// fileChip is an attachment that is not an image: an icon and its name.
func (d *cdraw) fileChip(gtx layout.Context, ic, name string) layout.Dimensions {
	th := d.th
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(260))
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X -= gtx.Dp(30)
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), theme.Mix(th.Fg, th.Bg, 0.2), name)
	})
	h := gtx.Dp(hit)
	w := gtx.Dp(30) + td.Size.X + gtx.Dp(10)
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	icon(gtx, image.Pt(gtx.Dp(10), (h-gtx.Dp(14))/2), ic, 14, th.Muted)
	o := op.Offset(image.Pt(gtx.Dp(30), (h-td.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}
