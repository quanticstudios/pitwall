package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cdraw draws one frame of a Transcript.
type cdraw struct {
	t          *Transcript
	th         *theme.Theme
	s          *session.Snapshot
	sec        float64 // seconds since the transcript opened, for spinners
	col, prose int     // the column's width, and reply prose's
}

// avgChar is the UI font's average character width, measured once per
// theme and scale.
type avgChar struct {
	th *theme.Theme
	px float32
	w  int
}

const sample = "The quick brown fox jumps over the lazy dog, then naps."

func (d *cdraw) avgChar(gtx layout.Context) int {
	a := &d.t.avg
	if a.th != d.th || a.px != gtx.Metric.PxPerSp {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
		dims := widget.Label{MaxLines: 1}.Layout(g, d.th.Shaper, d.th.UIFont, d.th.Sp(theme.Body), sample, op.CallOp{})
		m.Stop()
		*a = avgChar{th: d.th, px: gtx.Metric.PxPerSp, w: dims.Size.X / utf8.RuneCountInString(sample)}
	}
	return a.w
}

// --- small pieces ---

func (d *cdraw) selColor() color.NRGBA { return theme.Mix(d.th.Bg, d.th.Primary, 0.42) }

// selectable draws txt selectable, wrapped to the width.
func (d *cdraw) selectable(gtx layout.Context, sl *widget.Selectable, f font.Font, size unit.Sp, c color.NRGBA, txt string) layout.Dimensions {
	sl.SetText(txt)
	sl.LineHeightScale = 1.45
	gtx.Constraints.Min = image.Point{}
	return sl.Layout(gtx, d.th.Shaper, f, size, material(gtx, c), material(gtx, d.selColor()))
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

// ghost is a quiet button: an icon, a label, hovered fill; a copy button
// shows a green check and Copied for a moment after its click.
func (d *cdraw) ghost(gtx layout.Context, c *widget.Clickable, ic, txt string, copied bool, h unit.Dp) layout.Dimensions {
	th := d.th
	col := th.Muted
	if copied {
		ic, col = "check", th.Readable(th.Green, th.Bg)
		if txt != "" {
			txt = "Copied"
		}
	}
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		hh, pad, isz := gtx.Dp(h), gtx.Dp(6), gtx.Dp(12)
		call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			if txt == "" {
				return layout.Dimensions{}
			}
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), col, txt)
		})
		w := pad*2 + td.Size.X
		if ic != "" {
			w += isz
			if txt != "" {
				w += gtx.Dp(5)
			}
		}
		if txt == "" {
			w = hh
		}
		if c.Hovered() {
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, w, hh), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		x := pad
		if txt == "" {
			x = (hh - isz) / 2
		}
		if ic != "" {
			at(gtx, image.Pt(x, (hh-isz)/2), func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, isz, col) })
			x += isz + gtx.Dp(5)
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
		fill, ring, fg = theme.Mix(th.Primary, th.Bg, 0.22), theme.Mix(th.Primary, th.Bg, 0.36), th.OnPrimary
	}
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.MonoFont), th.Sp(theme.Caption), fg, k)
	})
	w, h := max(td.Size.X+gtx.Dp(10), gtx.Dp(18)), gtx.Dp(18)
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(5), fill, ring)
	o := op.Offset(image.Pt((w-td.Size.X)/2, (h-td.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// keyButton is a card's button with its key on a keycap.
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
		h, pad := gtx.Dp(26), gtx.Dp(9)
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), fg, txt)
		})
		var kc op.CallOp
		var kd layout.Dimensions
		if k != "" {
			kc, kd = measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.keycap(gtx, k, primary) })
		}
		w := pad*2 + ld.Size.X
		if k != "" {
			w += gtx.Dp(7) + kd.Size.X
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		o := op.Offset(image.Pt(pad, (h-ld.Size.Y)/2)).Push(gtx.Ops)
		lc.Add(gtx.Ops)
		o.Pop()
		if k != "" {
			o := op.Offset(image.Pt(pad+ld.Size.X+gtx.Dp(7), (h-kd.Size.Y)/2)).Push(gtx.Ops)
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
	x := 0
	for i := range ws {
		if dims[i].Size.X == 0 {
			continue
		}
		if x > 0 {
			x += gap
		}
		o := op.Offset(image.Pt(x, (h-dims[i].Size.Y)/2)).Push(gtx.Ops)
		calls[i].Add(gtx.Ops)
		o.Pop()
		x += dims[i].Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}

// --- rows ---

// block draws one list row of transcript items.
func (d *cdraw) block(gtx layout.Context, b block) layout.Dimensions {
	switch b.kind {
	case blockPrompt:
		return d.prompt(gtx, &d.s.Content[b.lo])
	case blockSystem:
		it := &d.s.Content[b.lo]
		return d.selectable(gtx, d.t.sel(it.ID), d.th.UIFont, d.th.Sp(theme.Small), d.th.Muted, it.Text)
	}
	return d.turn(gtx, b)
}

// prompt is a user prompt's bubble on the right, its images under the
// text.
func (d *cdraw) prompt(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	maxW := d.col * 84 / 100
	px, py := gtx.Dp(12), gtx.Dp(9)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, maxW-2*px), gtx.Constraints.Max.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(8),
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

// promptText is a prompt's selectable text, its [Image #N] tokens drawn
// as chips over the words.
func (d *cdraw) promptText(gtx layout.Context, it *session.Item) layout.Dimensions {
	txt := strings.TrimRight(it.Text, "\n ")
	if txt == "" {
		return layout.Dimensions{}
	}
	sl := d.t.sel(it.ID)
	dims := d.selectable(gtx, sl, d.th.UIFont, d.th.Sp(theme.Body), d.th.Fg, txt)
	for _, ref := range imageRefs(txt) {
		a, b := utf8.RuneCountInString(txt[:ref.start]), utf8.RuneCountInString(txt[:ref.end])
		for _, rg := range sl.Regions(a, b, nil) {
			d.imageChip(gtx, rg.Bounds, ref.n)
		}
	}
	return dims
}

// imageChip draws an image token's chip over r: the image icon and
// "Image #N" on the blue chip colors.
func (d *cdraw) imageChip(gtx layout.Context, r image.Rectangle, n int) {
	th := d.th
	bg, fg := th.Chip(th.Blue, th.SurfaceSecondary)
	h := min(gtx.Dp(19), r.Dy())
	cy := (r.Min.Y + r.Max.Y) / 2
	box := image.Rect(r.Min.X-gtx.Dp(2), cy-h/2, r.Max.X+gtx.Dp(2), cy-h/2+h)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(box, gtx.Dp(5)).Op(gtx.Ops))
	defer clip.Rect(box).Push(gtx.Ops).Pop()
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Caption), fg, "Image #"+strconv.Itoa(n))
	})
	isz, gap := gtx.Dp(12), gtx.Dp(4)
	w := isz + gap + td.Size.X
	x := box.Min.X + (box.Dx()-w)/2
	if w > box.Dx() { // no room for the icon
		x, isz, gap = box.Min.X+(box.Dx()-td.Size.X)/2, 0, 0
	}
	if isz > 0 {
		at(gtx, image.Pt(x, cy-isz/2), func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, "image", isz, fg) })
	}
	o := op.Offset(image.Pt(x+isz+gap, cy-td.Size.Y/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}

// attachments are a prompt's images as thumbnails, numbered as the
// prompt's [Image #N] tokens count them, and its files as chips.
func (d *cdraw) attachments(gtx layout.Context, it *session.Item) layout.Dimensions {
	if len(it.Attachments) == 0 {
		return layout.Dimensions{}
	}
	var ws []layout.Widget
	n := 0
	for i := range it.Attachments {
		a := &it.Attachments[i]
		if a.Kind == "image" {
			n++
			no := n
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.thumbnail(gtx, it.ID, a, no) })
			continue
		}
		name := a.Name
		if a.Kind == "workspace-file" {
			name = a.Path
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.fileChip(gtx, "file", name) })
	}
	return hrow(gtx, gtx.Dp(8), ws...)
}

// thumbnail is image attachment a, 96 dp tall, with its number.
func (d *cdraw) thumbnail(gtx layout.Context, item string, a *session.Attachment, n int) layout.Dimensions {
	th := d.th
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
		gtx.Constraints.Max.X -= gtx.Dp(26)
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Caption), theme.Mix(th.Muted, th.Fg, 0.6), name)
	})
	h := gtx.Dp(24)
	w := gtx.Dp(26) + td.Size.X + gtx.Dp(8)
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	icon(gtx, image.Pt(gtx.Dp(8), (h-gtx.Dp(12))/2), ic, 12, th.Muted)
	o := op.Offset(image.Pt(gtx.Dp(26), (h-td.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// turn is a turn's work log, then its reply.
func (d *cdraw) turn(gtx layout.Context, b block) layout.Dimensions {
	start, end, running := turnTimes(d.s, b, b.hi == len(d.s.Content))
	logged := b.hi - b.lo
	if b.reply >= 0 {
		logged--
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	var log, rep layout.Widget
	if logged > 0 || running {
		log = func(gtx layout.Context) layout.Dimensions { return d.workLog(gtx, b, start, end, running) }
	}
	if b.reply >= 0 {
		rep = func(gtx layout.Context) layout.Dimensions { return d.reply(gtx, b.reply, running, start, end) }
	}
	return vstack(gtx, gtx.Dp(12), log, rep)
}
