package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/syntax"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cdraw draws one frame of a Transcript.
type cdraw struct {
	t           *Transcript
	th          *theme.Theme
	s           *session.Snapshot
	sec         float64                         // seconds since the transcript opened, for spinners
	col, proseW int                             // the column's width, and reply prose's
	ledger      bool                            // drawing Ledger layout
	own         bool                            // drawing the user's own message as markdown
	geom        ledgerGeom                      // Ledger column widths
	syn         *[syntax.Header + 1]color.NRGBA // code's token colors, for synFor
	synFor      *theme.Theme
}

// avgChar is the width per character that puts reply prose at about 96
// characters: 680 dp at 14 sp, scaling with the UI size.
func (d *cdraw) avgChar(gtx layout.Context) int {
	return gtx.Sp(d.th.Sp(theme.Large)) * 680 / (14 * proseChars)
}

// --- small pieces ---

func (d *cdraw) selColor() color.NRGBA { return theme.Mix(d.th.Bg, d.th.Primary, 0.42) }

// selectable draws txt selectable in st, wrapped to the width.
// The transcript selects over all its text (selection.go): sl only paints
// its part, and takes no events of its own.
func (d *cdraw) selectable(gtx layout.Context, sl *widget.Selectable, st tstyle, txt string) layout.Dimensions {
	sl.SetText(txt)
	sl.LineHeightScale = st.lh
	sl.PaintSelectionWhenUnfocused = true
	d.t.span.press(gtx, sl)
	d.t.span.paint(d.t.msg.keys[sl], sl, txt)
	gtx.Constraints.Min = image.Point{}
	g := gtx
	g.Source = gtx.Source.Disabled()
	m := op.Record(gtx.Ops)
	dims := sl.Layout(g, d.th.Shaper, st.f, st.size, material(gtx, st.c), material(gtx, d.selColor()))
	call := m.Stop()
	d.t.span.size[sl] = dims.Size
	d.t.marks(gtx, d, sl, txt) // under the text
	d.t.flashMark(gtx, d, sl, txt)
	call.Add(gtx.Ops)
	return dims
}

// selLine draws one line of txt as lbl does, but selectable as the
// transcript's other text is, by key; a cut line copies whole.
func (d *cdraw) selLine(gtx layout.Context, key string, f font.Font, size unit.Sp, c color.NRGBA, txt string) layout.Dimensions {
	sl := d.t.sel(key)
	sl.MaxLines, sl.WrapPolicy = 1, text.WrapGraphemes
	return d.selectable(gtx, sl, tstyle{f, size, c, 0}, txt)
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

// wrapRow lays ws out as hrow does, gap apart, starting a new line, gap
// under the last, for one that would pass the right edge: buttons that
// do not fit a narrow or large-text chat.
func wrapRow(gtx layout.Context, gap int, ws ...layout.Widget) layout.Dimensions {
	x, y, h, w := 0, 0, 0, 0
	for _, wd := range ws {
		g := gtx
		g.Constraints.Min = image.Point{}
		call, d := measure(g, wd)
		if d.Size.X == 0 {
			continue
		}
		if x > 0 && x+gap+d.Size.X > gtx.Constraints.Max.X {
			x, y, h = 0, y+h+gap, 0
		} else if x > 0 {
			x += gap
		}
		o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x += d.Size.X
		h, w = max(h, d.Size.Y), max(w, x)
	}
	return layout.Dimensions{Size: image.Pt(w, y+h)}
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
		return d.sysItem(gtx, &d.s.Content[b.lo], false)
	}
	return layout.Dimensions{} // a turn draws as its rows, see buildRows
}

// prompt is a user prompt's bubble on the right, its images above the
// text.
func (d *cdraw) prompt(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	if slashCommand(it.Text) && len(it.Attachments) == 0 {
		return d.commandChip(gtx, strings.TrimSpace(it.Text), true)
	}
	maxW := d.col * 84 / 100
	px, py := gtx.Dp(14), gtx.Dp(10)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, maxW-2*px), gtx.Constraints.Max.Y)}
	call, dims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions { return d.senderLine(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.attachments(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.promptText(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.pastes(gtx, it) },
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
	d.promptTools(gtx, it, x, bh)
	return layout.Dimensions{Size: image.Pt(d.col, bh)}
}

// senderLine names who sent a message pitwall typed, above its text:
// the agent's mark and name and its tab, quiet, without the header's
// reply command.
func (d *cdraw) senderLine(gtx layout.Context, it *session.Item) layout.Dimensions {
	shown, _ := promptParts(it.Text)
	s, _ := promptSender(shown)
	if s.label == "" {
		return layout.Dimensions{}
	}
	st := d.small(d.th.Muted)
	label := func(gtx layout.Context) layout.Dimensions {
		return widget.Label{MaxLines: 1}.Layout(gtx, d.th.Shaper, st.f, st.size, s.label, material(gtx, st.c))
	}
	if s.prov == "" {
		return label(gtx)
	}
	return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceEnd}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return sidebar.AgentMark(gtx, s.prov, gtx.Sp(st.size), st.c)
		}),
		layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
		layout.Rigid(label),
	)
}

// promptText is a prompt's selectable text, as markdown as a reply's
// unless it has none, in the prompt's color. Each [Image #N] token draws
// as a chip: the text widens the token to make room for the chip's icon
// and padding, and a copy gives back the token as typed.
func (d *cdraw) promptText(gtx layout.Context, it *session.Item) layout.Dimensions {
	shown, _ := promptParts(it.Text)
	s, rest := promptSender(shown)
	txt := mdview.Detab(rest)
	if txt == "" {
		return layout.Dimensions{}
	}
	st := d.promptStyle()
	lead, trail := d.chipPad(gtx, st)
	if r := d.t.promptDoc(it.ID, rest, [2]int{lead, trail}); !r.flat {
		r.typed = strings.TrimPrefix(s.line+"\n", "\n") + rest
		d.own = true
		defer func() { d.own = false }()
		return d.markdown(gtx, it.ID, r)
	}
	ct := chipText(txt, lead, trail)
	ct.head = s.line
	sl := d.t.sel(it.ID)
	d.copyAs(sl, ct)
	dims := d.selectable(gtx, sl, st, ct.text)
	d.imageChips(gtx, sl, ct.chips, st)
	return dims
}

// imageChips draws each chip over its slot in sl's text, so a chip takes
// the room of a word: no gap but the typed spaces around it.
func (d *cdraw) imageChips(gtx layout.Context, sl *widget.Selectable, chips []chip, st tstyle) {
	var rg []widget.Region
	for _, c := range chips {
		if rg = sl.Regions(c.slot[0], c.slot[1], rg[:0]); len(rg) != 1 {
			continue // shortcut: a slot is no-break text, so it splits only if one word overflows the line
		}
		slot := rg[0].Bounds
		if rg = sl.Regions(c.label[0], c.label[1], rg[:0]); len(rg) > 0 {
			d.imageChip(gtx, slot, rg[0], c, st)
		}
	}
}

// imageChip draws an image token's chip over its slot: the blue chip
// colors, a 1 em image icon in the room left of its label's region r, and
// the label redrawn in blue on the line's baseline.
func (d *cdraw) imageChip(gtx layout.Context, slot image.Rectangle, r widget.Region, c chip, st tstyle) {
	th := d.th
	bg, fg := th.Chip(th.Blue, th.SurfaceSecondary)
	em := gtx.Sp(st.size)
	base := r.Bounds.Max.Y - r.Baseline
	box := image.Rect(slot.Min.X, base-em*78/100-gtx.Dp(2), slot.Max.X, base+em*22/100+gtx.Dp(2))
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(box, gtx.Dp(5)).Op(gtx.Ops))
	xh := em / 2
	ix := box.Min.X + (r.Bounds.Min.X-box.Min.X-em)*6/10 // the slot's slack split 6:4 like its 6 dp and 4 dp pads
	at(gtx, image.Pt(ix, base-xh/2-em/2), func(gtx layout.Context) layout.Dimensions {
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
	shown, _ := promptParts(it.Text)
	refs := imageRefs(shown)
	n := 0
	for i := range it.Attachments {
		a := &it.Attachments[i]
		if a.Kind == "image" {
			n++
			no := imageNo(refs, n)
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.thumbnail(gtx, it, a, no) })
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

// thumbnail is image attachment a of prompt it, 96 dp tall, with its
// number; a click shows it large. Its face waits empty while the data is
// on its way (it.Cut: the item read whole brings it), and a chip says why
// when the picture is gone.
func (d *cdraw) thumbnail(gtx layout.Context, it *session.Item, a *session.Attachment, n int) layout.Dimensions {
	h, w := gtx.Dp(96), gtx.Dp(72)
	if a.Data == "" {
		if it.Cut {
			return d.thumbFace(gtx, thumb{}, w, h, n, false)
		}
		return d.goneChip(gtx, it.ID+"/"+a.ID, a.Name, "The agent's transcript no longer holds this picture")
	}
	k := it.ID + "/" + a.ID
	tb := d.t.thumbs.snapshot(d.t.thumbs.get(k, a, d.t.Invalidate))
	if tb.ready && tb.size == (image.Point{}) {
		return d.goneChip(gtx, k, a.Name, "Pitwall could not read this picture")
	}
	if tb.ready {
		w = min(max(h*tb.size.X/tb.size.Y, gtx.Dp(48)), gtx.Dp(180))
	} else if d.t.Invalidate == nil {
		d.t.live = true
	}
	ck := d.t.click("img:" + k)
	for ck.Clicked(gtx) {
		d.t.tool.zoom = zoomed{key: k, a: *a}
		gtx.Execute(key.FocusCmd{Tag: &d.t.focus})
	}
	return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		return d.thumbFace(gtx, tb, w, h, n, ck.Hovered())
	})
}

// goneChip is a picture that cannot show as its file chip, with why on
// hover; it opens nothing, so it keeps the arrow.
func (d *cdraw) goneChip(gtx layout.Context, key, name, why string) layout.Dimensions {
	ck := d.t.click("gone:" + key)
	dims := ck.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return d.fileChip(gtx, "image", name) })
	d.tip(gtx, ck, dims.Size, why)
	return dims
}

// thumbFace draws a prompt's thumbnail tb, w by h, with its number n;
// hover lightens it.
func (d *cdraw) thumbFace(gtx layout.Context, tb thumb, w, h, n int, hover bool) layout.Dimensions {
	th := d.th
	r := image.Rect(0, 0, w, h)
	rad := gtx.Dp(theme.RadiusPopover)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(r, rad).Op(gtx.Ops))
	cl := clip.UniformRRect(r, rad).Push(gtx.Ops)
	if tb.ready {
		g := gtx
		g.Constraints = layout.Exact(r.Size())
		widget.Image{Src: tb.img, Fit: widget.Cover, Position: layout.Center}.Layout(g)
	}
	if hover {
		veil := th.Fg
		veil.A = 0x14
		paint.FillShape(gtx.Ops, veil, clip.Rect(r).Op())
	}
	cl.Pop()
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
