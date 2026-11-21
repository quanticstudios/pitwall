package chatin

import (
	"image"
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/gesture"
	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Lucide's quote and file-text, for the quote and pasted text chips.
const (
	icQuote    = "M16 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2zM5 3a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2 1 1 0 0 1 1 1v1a2 2 0 0 1-2 2 1 1 0 0 0-1 1v2a1 1 0 0 0 1 1 6 6 0 0 0 6-6V5a2 2 0 0 0-2-2z"
	icFileText = "M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7ZM14 2v4a2 2 0 0 0 2 2h4M10 9H8M16 13H8M16 17H8"
)

// partUI is a part's card on the shelf: the card itself, which goes to a
// quote's source, its comment pencil and its remove x.
type partUI struct {
	body, pen, rm widget.Clickable
	hov           gesture.Hover
}

// partIcon draws kind k's icon, size big, in col.
func partIcon(gtx layout.Context, k PartKind, size int, col color.NRGBA) layout.Dimensions {
	switch k {
	case PartQuote:
		return sidebar.IconPath(gtx, icQuote, size, col)
	case PartTerminal:
		return sidebar.Icon(gtx, "terminal", size, col)
	}
	return sidebar.IconPath(gtx, icFileText, size, col)
}

// partColor is the accent of kind k's chip in the text.
func partColor(th *theme.Theme, k PartKind) color.NRGBA {
	switch k {
	case PartQuote:
		return th.Purple
	case PartTerminal:
		return th.Green
	}
	return th.Blue
}

// AddQuote puts a quote of text, selected in a reply at src, at the
// caret, as T3 Code's Cite in composer does; false, with a note, when it
// is blank or too long.
func (c *Composer) AddQuote(text string, src Source) bool {
	text = strings.Trim(text, "\n")
	switch {
	case strings.TrimSpace(text) == "":
		return false
	case utf8.RuneCountInString(text) > MaxQuote:
		c.note = "The selection is too long to quote: select less, or copy and paste it"
		return false
	}
	c.insertToken(c.draft.AddPart(Part{Kind: PartQuote, Text: text, Src: src}))
	return true
}

// AddTerminal puts lines a terminal pane selected at the caret, label the
// command that printed them or "", and returns the excerpt's number for
// LabelTerminal; 0, with a note, when nothing is left of them. Lines too
// long for a message go as a pasted text file where the daemon takes one.
func (c *Composer) AddTerminal(text, label string) int {
	text = strings.Trim(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		c.note = "The selection has no text to add"
		return 0
	}
	if c.files && Fold(text, utf8.RuneCountInString(c.draft.Expand(c.Text()))) {
		c.foldPaste(text)
		return 0
	}
	c.insertToken(c.draft.AddPart(Part{Kind: PartTerminal, Text: text, Label: label}))
	return c.draft.seq[PartTerminal]
}

// LabelTerminal names the command that printed terminal excerpt n, once
// the daemon said; an excerpt already sent is gone and keeps its label.
func (c *Composer) LabelTerminal(n int, label string) {
	if p := c.draft.Part(PartTerminal, n); p != nil && strings.TrimSpace(label) != "" {
		p.Label = strings.TrimSpace(label)
	}
}

// insertToken puts tok at the caret, a word of its own, with a space
// after it.
func (c *Composer) insertToken(tok string) {
	r := []rune(c.Text())
	s, e := c.ed.Selection()
	if lo := min(s, e); lo > 0 && lo <= len(r) && !unicode.IsSpace(r[lo-1]) {
		tok = " " + tok
	}
	c.ed.Insert(glue(tok) + " ")
	c.note, c.noteAt = "", ""
}

// attached counts the draft's attachments: its images and pasted texts.
func (c *Composer) attached() int {
	n := len(c.draft.Images)
	for _, p := range c.draft.Parts {
		if p.Kind == PartPasted {
			n++
		}
	}
	return n
}

// foldPaste puts paste s in the draft as a text file and reports whether
// it took it: false leaves it to go in inline, when the message has no
// room for another attachment but has room for the text.
func (c *Composer) foldPaste(s string) bool {
	over := utf8.RuneCountInString(c.draft.Expand(c.Text()))+utf8.RuneCountInString(s) > MessageLimit
	switch {
	case len(s) > MaxPasted:
		c.note = "The paste is over 8 MiB: save it to a file in the project and @ mention it"
		return true
	case c.attached() >= maxImages && over:
		c.note = "The paste is too long for this message: remove an attachment, then paste again"
		return true
	case c.attached() >= maxImages:
		return false
	}
	c.insertToken(c.draft.AddPart(Part{Kind: PartPasted, Text: s, Label: PastedName(c.draft.seq[PartPasted] + 1)}))
	return true
}

// paste takes text pasted into the box: a big paste goes as a text file
// where the daemon takes one (env.TextFiles), unless Ctrl+Shift+V asked
// for it inline.
func (c *Composer) paste(env Env, s string) {
	inline := c.inline
	c.inline = false
	if !inline && env.TextFiles && Fold(s, utf8.RuneCountInString(c.draft.Expand(c.Text()))) && c.foldPaste(s) {
		return
	}
	c.ed.Insert(s)
}

// pasteInline asks for the clipboard's text to go in inline, as
// Ctrl+Shift+V does.
func (c *Composer) pasteInline(gtx layout.Context) {
	c.inline = true
	gtx.Execute(clipboard.ReadCmd{Tag: &c.ed})
}

// askRemove takes image n out, or asks first when the text still names
// it: removing it then takes its token too.
func (c *Composer) askRemove(n int) {
	if Mentioned(c.Text(), n) {
		c.confirm = n
		return
	}
	c.RemoveImage(n)
}

// RemovePart takes the part with token tok out of the draft, with every
// copy of its token.
func (c *Composer) RemovePart(tok string) {
	for {
		s, e := tokenSpan(c.Text(), tok)
		if s < 0 {
			break
		}
		c.ed.SetCaret(s, e)
		c.ed.Insert("")
	}
	if c.editing == tok {
		c.editing = ""
	}
	c.draft.Sync(c.Text())
}

// partOf is the draft's part with token tok, nil when it went.
func (c *Composer) partOf(tok string) *Part {
	for i := range c.draft.Parts {
		if c.draft.Parts[i].Token() == tok {
			return &c.draft.Parts[i]
		}
	}
	return nil
}

// editComment opens quote tok's comment field.
func (c *Composer) editComment(tok string) {
	p := c.partOf(tok)
	if p == nil {
		return
	}
	c.editing, c.editHad = tok, false
	c.comment.SingleLine, c.comment.Submit = true, true
	c.comment.SetText(p.Comment)
	c.comment.SetCaret(c.comment.Len(), c.comment.Len())
}

// endComment closes the comment field, keeping what it says when save.
func (c *Composer) endComment(gtx layout.Context, save bool) {
	if p := c.partOf(c.editing); p != nil && save {
		p.Comment = strings.TrimSpace(c.comment.Text())
	}
	c.editing, c.editHad = "", false
	gtx.Execute(key.FocusCmd{Tag: &c.ed})
}

// takeFocus gives the keys to the comment field while it is open, else
// to the draft.
func (c *Composer) takeFocus(gtx layout.Context) {
	switch {
	case c.editing != "":
		if !c.editHad && !gtx.Focused(&c.comment) {
			gtx.Execute(key.FocusCmd{Tag: &c.comment})
		}
	case !gtx.Focused(&c.ed):
		gtx.Execute(key.FocusCmd{Tag: &c.ed})
	}
}

// partEvents takes the shelf's clicks and the comment field's keys.
func (c *Composer) partEvents(gtx layout.Context, env Env) {
	if c.confirm != 0 {
		if !hasImage(c.draft.Images, c.confirm) {
			c.confirm = 0
		}
		if c.keepB.Clicked(gtx) {
			c.confirm = 0
			gtx.Execute(key.FocusCmd{Tag: &c.ed})
		}
		if c.dropB.Clicked(gtx) {
			c.RemoveImage(c.confirm)
			c.confirm = 0
			gtx.Execute(key.FocusCmd{Tag: &c.ed})
		}
	}
	for tok, u := range c.parts {
		p := c.partOf(tok)
		if p == nil {
			delete(c.parts, tok)
			continue
		}
		if u.rm.Clicked(gtx) {
			c.RemovePart(tok)
			gtx.Execute(key.FocusCmd{Tag: &c.ed})
			continue
		}
		if u.pen.Clicked(gtx) {
			c.editComment(tok)
		}
		if u.body.Clicked(gtx) && p.Kind == PartQuote {
			if env.Reveal == nil || !env.Reveal(p.Src) {
				c.note = "The quoted reply is no longer in this chat"
			}
		}
	}
	if c.editing == "" {
		return
	}
	if c.partOf(c.editing) == nil {
		c.editing = ""
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &c.comment, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			c.endComment(gtx, false)
			return
		}
	}
	for {
		ev, ok := c.comment.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			c.endComment(gtx, true)
			return
		}
	}
	// A click elsewhere, as on the draft, keeps the comment.
	if gtx.Focused(&c.comment) {
		c.editHad = true
	} else if c.editHad {
		c.endComment(gtx, true)
	}
}

func hasImage(ims []Image, n int) bool {
	for _, im := range ims {
		if im.N == n {
			return true
		}
	}
	return false
}

// shelf is the draft's images and parts over the text, wrapping, then
// the question that removing an image the text names asks, or a quote's
// comment field.
func (c *Composer) shelf(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	c.side = gtx.Dp(thumbDp)
	if len(c.draft.Images) == 0 && len(c.draft.Parts) == 0 {
		return layout.Dimensions{}
	}
	if c.rm == nil {
		c.rm, c.hov = map[int]*widget.Clickable{}, map[int]*gesture.Hover{}
	}
	if c.parts == nil {
		c.parts = map[string]*partUI{}
	}
	var ws []layout.Widget
	for i := range c.draft.Images {
		im := &c.draft.Images[i]
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return c.chip(gtx, th, im) })
	}
	for _, p := range c.draft.ordered(c.Text()) {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return c.partCard(gtx, th, p) })
	}
	return layout.Inset{Top: 10, Left: theme.SpaceL, Right: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return wrap(gtx, gtx.Dp(theme.SpaceS), ws...) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.ask(gtx, th) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.commentRow(gtx, th) }),
		)
	})
}

// wrap lays ws out left to right, gap apart, starting a new row, gap
// below, where the next would pass the width.
func wrap(gtx layout.Context, gap int, ws ...layout.Widget) layout.Dimensions {
	x, y, rowH, w := 0, 0, 0, 0
	for _, wd := range ws {
		g := gtx
		g.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := wd(g)
		call := m.Stop()
		if x > 0 && x+d.Size.X > gtx.Constraints.Max.X {
			x, y, rowH = 0, y+rowH+gap, 0
		}
		o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x += d.Size.X + gap
		rowH = max(rowH, d.Size.Y)
		w = max(w, x-gap)
	}
	return layout.Dimensions{Size: image.Pt(w, y+rowH)}
}

// cardDp is a part card's widest.
const cardDp unit.Dp = 232

// partCard is part p on the shelf, as tall as an image's thumbnail: its icon
// and name, then two lines of what it holds. A quote's card goes to its
// source when clicked and has a pencil for its comment; each has an x
// while the pointer is on it.
func (c *Composer) partCard(gtx layout.Context, th *theme.Theme, p Part) layout.Dimensions {
	tok := p.Token()
	u := c.parts[tok]
	if u == nil {
		u = new(partUI)
		c.parts[tok] = u
	}
	side := c.side
	w := min(gtx.Dp(cardDp), gtx.Constraints.Max.X)
	size := image.Pt(w, side)
	rect := image.Rectangle{Max: size}
	r := gtx.Dp(theme.RadiusPopover)
	shape := clip.UniformRRect(rect, r)
	fill := th.SurfaceSecondary
	if p.Kind == PartQuote && (u.body.Hovered() || u.pen.Hovered()) {
		fill = theme.Mix(th.SurfaceSecondary, th.Fg, 0.04)
	}
	paint.FillShape(gtx.Ops, fill, shape.Op(gtx.Ops))
	ring := th.BorderSubtle
	if c.editing == tok {
		ring = theme.Mix(th.Surface, th.Primary, 0.5)
	}
	paint.FillShape(gtx.Ops, ring, clip.Stroke{Path: shape.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	cl := shape.Push(gtx.Ops)
	u.hov.Add(gtx.Ops)
	cl.Pop()
	hovered := u.hov.Update(gtx.Source) || u.rm.Hovered() || u.pen.Hovered() || gtx.Focused(&u.rm) || gtx.Focused(&u.pen)
	if p.Kind == PartQuote {
		bg := gtx
		bg.Constraints = layout.Exact(size)
		u.body.Layout(bg, func(gtx layout.Context) layout.Dimensions {
			semantic.LabelOp("Go to " + p.Name() + "'s source").Add(gtx.Ops)
			pointer.CursorPointer.Add(gtx.Ops)
			return layout.Dimensions{Size: size}
		})
	}
	pad := gtx.Dp(theme.SpaceS)
	btns := 0
	if hovered {
		btns = gtx.Dp(28)
		if p.Kind == PartQuote {
			btns *= 2
		}
	}
	// The texts: the name over two lines of what it holds.
	title, l1, l2 := p.Name(), "", ""
	f1, f2 := th.UIFont, th.UIFont
	c1 := th.Muted
	switch p.Kind {
	case PartQuote:
		l1, l2 = Flat(p.Text), ""
		if p.Comment != "" {
			l1, l2, c1 = p.Comment, Flat(p.Text), th.Fg
		}
	case PartTerminal:
		l1 = itoa(Lines(p.Text)) + " lines"
		if Lines(p.Text) == 1 {
			l1 = "1 line"
		}
		if p.Label != "" {
			l1, f1 = "$ "+p.Label, th.MonoFont
		}
		l2, f2 = FirstLine(p.Text), th.MonoFont
	case PartPasted:
		title = p.Label
		l1 = Size(len(p.Text)) + " · " + itoa(Lines(p.Text)) + " lines"
		l2, f2 = FirstLine(p.Text), th.MonoFont
	}
	tw := w - 2*pad
	lh := (side - 2*pad) / 3
	at := func(y, wd int, f func(gtx layout.Context) layout.Dimensions) {
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, wd), lh)}
		m := op.Record(gtx.Ops)
		d := f(g)
		call := m.Stop()
		o := op.Offset(image.Pt(pad, y+(lh-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
	}
	small := th.Sp(theme.Small)
	at(pad, tw-btns, func(gtx layout.Context) layout.Dimensions {
		is := gtx.Dp(12)
		return row(gtx, gtx.Dp(6), lh,
			func(gtx layout.Context) layout.Dimensions {
				return partIcon(gtx, p.Kind, is, th.Readable(partColor(th, p.Kind), th.SurfaceSecondary))
			},
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, medium(th.UIFont), small, th.Fg, title)
			})
	})
	if l2 == "" && p.Kind == PartQuote {
		at(pad+lh, tw, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.Y = 2 * lh
			return widget.Label{MaxLines: 2}.Layout(gtx, th.Shaper, f1, small, l1, colorOp(gtx, c1))
		})
	} else {
		at(pad+lh, tw, func(gtx layout.Context) layout.Dimensions { return text(gtx, th, f1, small, c1, l1) })
		at(pad+2*lh, tw, func(gtx layout.Context) layout.Dimensions { return text(gtx, th, f2, small, th.Muted, l2) })
	}
	if hovered {
		x := w - gtx.Dp(2) - gtx.Dp(28)
		cardButton(gtx, th, &u.rm, image.Pt(x, gtx.Dp(2)), "x", "Remove "+p.Name())
		if p.Kind == PartQuote {
			cardButton(gtx, th, &u.pen, image.Pt(x-gtx.Dp(28), gtx.Dp(2)), "pencil", "Comment on "+p.Name())
		}
	}
	return layout.Dimensions{Size: size}
}

// cardButton is a 28dp ghost button with a 12dp icon at p.
func cardButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, p image.Point, icon, label string) {
	o := op.Offset(p).Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(gtx.Dp(28), gtx.Dp(28)))
	b.Layout(g, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		s, is := gtx.Dp(28), gtx.Dp(12)
		ghostFill(gtx, th, b, image.Pt(s, s))
		io := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, icon, is, th.Fg)
		io.Pop()
		return layout.Dimensions{Size: image.Pt(s, s)}
	})
	o.Pop()
}

// ask is the question removing an image the text still names asks, with
// Keep and Remove, T3 Code's confirm, inline under the shelf.
func (c *Composer) ask(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if c.confirm == 0 {
		return layout.Dimensions{}
	}
	return layout.Inset{Top: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, "Image #"+itoa(c.confirm)+" is named in your text. Remove it and its mention?")
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return textButton(gtx, th, &c.keepB, "Keep") }),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return dangerButton(gtx, th, &c.dropB, "Remove") }),
		)
	})
}

// dangerButton is textButton in red, for an action that takes something
// out.
func dangerButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(10)
		col := th.Readable(th.Red, th.Surface)
		m := op.Record(gtx.Ops)
		d := text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, label)
		call := m.Stop()
		size := image.Pt(d.Size.X+2*pad, h)
		fill := theme.Mix(th.Surface, th.Red, 0.12)
		switch {
		case b.Pressed():
			fill = theme.Mix(th.Surface, th.Red, 0.24)
		case b.Hovered():
			fill = theme.Mix(th.Surface, th.Red, 0.18)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		o := op.Offset(image.Pt(pad, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
}

// commentRow is the open comment field under the shelf: the quote's
// name, the field, and what Enter and Esc do.
func (c *Composer) commentRow(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	p := c.partOf(c.editing)
	if p == nil {
		return layout.Dimensions{}
	}
	return layout.Inset{Top: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(32)
		size := image.Pt(gtx.Constraints.Max.X, h)
		rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl))
		paint.FillShape(gtx.Ops, th.Bg, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Primary, 0.5), clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		pad := gtx.Dp(theme.SpaceS)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		defer o.Pop()
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(size.X-2*pad, h)}
		return layout.Flex{Alignment: layout.Middle}.Layout(g,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return row(gtx, gtx.Dp(6), h,
					func(gtx layout.Context) layout.Dimensions {
						return partIcon(gtx, PartQuote, gtx.Dp(12), th.Readable(th.Purple, th.Bg))
					},
					func(gtx layout.Context) layout.Dimensions {
						return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, p.Name())
					})
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceS}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				size := th.Sp(theme.Body)
				if c.comment.Len() == 0 {
					lg := gtx
					lg.Constraints.Min = image.Point{}
					text(lg, th, th.UIFont, size, th.Muted, "Add a comment on the quote")
				}
				return c.comment.Layout(gtx, th.Shaper, th.UIFont, size, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Surface, th.Primary, 0.35)))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, "Enter saves · Esc cancels")
			}),
		)
	})
}

// partTokens covers each part's token in the editor with a chip, as
// tokens covers an image's: its icon and name, in its kind's accent.
func (c *Composer) partTokens(gtx layout.Context, th *theme.Theme) {
	txt := c.Text()
	size := th.Sp(theme.Large)
	var regions []widget.Region
	for _, p := range c.draft.Parts {
		s, _ := tokenSpan(txt, p.Token())
		if s < 0 {
			continue
		}
		bg, fg := th.Chip(partColor(th, p.Kind), th.Surface)
		regions = c.ed.Regions(s, s+utf8.RuneCountInString(p.Token()), regions[:0])
		for _, rg := range regions[:min(1, len(regions))] {
			lm := op.Record(gtx.Ops)
			ld := text(gtx, th, th.UIFont, size, fg, p.Name())
			label := lm.Stop()
			base := rg.Bounds.Max.Y - rg.Baseline
			ly := base - (ld.Size.Y - ld.Baseline)
			em, gap, padX, padY := gtx.Sp(size), gtx.Dp(theme.SpaceXS), gtx.Dp(6), gtx.Dp(theme.Space2XS)
			w := ld.Size.X + 2*padX
			withIcon := w+em+gap <= rg.Bounds.Dx()+2*padX
			if withIcon {
				w += em + gap
			}
			x := rg.Bounds.Min.X + (rg.Bounds.Dx()-w)/2
			r := image.Rect(x, ly-padY, x+w, ly+ld.Size.Y+padY)
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(5)).Op(gtx.Ops))
			lx := x + padX
			if withIcon {
				o := op.Offset(image.Pt(lx, base-em/4-em/2)).Push(gtx.Ops)
				partIcon(gtx, p.Kind, em, fg)
				o.Pop()
				lx += em + gap
			}
			o := op.Offset(image.Pt(lx, ly)).Push(gtx.Ops)
			label.Add(gtx.Ops)
			o.Pop()
		}
	}
}
