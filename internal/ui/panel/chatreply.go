package panel

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// reply is an assistant reply parsed once per text.
type reply struct {
	text   string
	live   bool // parsed healed, as a reply still streaming
	blocks []mdview.Block
	paras  map[int]para                // text blocks' plain text and styled ranges
	doc    mdview.Doc                  // tables, flowcharts and images
	plain  string                      // the reply without its markdown, for Copy reply
	wide   map[string]*mdview.Sideways // code blocks' sideways scroll, by key
}

// para is a text block as one selectable string: its styled runs as rune
// ranges, and the code and link runs drawn over it.
type para struct {
	text        string
	runs        []srun // the runs that are bold, italic, code or struck
	code, links [][2]int
	hrefs       []string // what each link opens, "" for nothing
	ct          ctext    // the way back from text to what the reply says
	plain       string   // text without the code chips' padding
}

// srun is runes [lo, hi) of a para's text, drawn as span s.
type srun struct {
	lo, hi int
	s      mdview.Span
}

// reply is item it's text parsed; a live one, still streaming, is healed
// first so a half-written bold or code span draws styled.
func (t *Transcript) reply(it *session.Item, live bool) *reply {
	r := t.docs[it.ID]
	if r != nil && r.text == it.Text && r.live == live {
		return r
	}
	src := it.Text
	if live {
		src = mdview.Heal(src)
	}
	old := r
	r = &reply{text: it.Text, live: live, blocks: mdview.Parse([]byte(src)), paras: map[int]para{}, wide: map[string]*mdview.Sideways{}}
	if old != nil {
		r.wide = old.wide // a code block keeps its scroll as the reply grows
	}
	r.doc.Set(r.blocks)
	var plain []string
	for i, b := range r.blocks {
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			p := spansPara(b.Spans, t.href)
			r.paras[i] = p
			plain = append(plain, b.Marker+sp(b.Marker)+p.plain)
		case mdview.Code, mdview.Mermaid:
			plain = append(plain, strings.TrimRight(b.Text, "\n"))
		case mdview.Table:
			for _, row := range b.Rows {
				var cells []string
				for _, c := range row {
					cells = append(cells, spansPara(c, nil).plain)
				}
				plain = append(plain, strings.Join(cells, "\t"))
			}
		case mdview.Images:
			for _, im := range b.Imgs {
				plain = append(plain, im.Alt)
			}
		}
	}
	r.plain = strings.Join(plain, "\n\n")
	t.docs[it.ID] = r
	return r
}

func sp(marker string) string {
	if marker == "" {
		return ""
	}
	return " "
}

// pathLike is inline code that names a file: a path with a dot or a
// slash, a :line after it or not, and no spaces.
var pathLike = regexp.MustCompile(`^[\w.@~+-]*[/.][\w./@~+-]*\w(:\d+){0,2}$`)

// href is what a link to link opens: a URL as it is, a path as the file
// under the chat's folder when it is there, else "". code says link is
// inline code, which opens only when it is a path to a file.
func (t *Transcript) href(link string, code bool) string {
	if code && !pathLike.MatchString(link) {
		return ""
	}
	if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
		if code {
			return ""
		}
		return link
	}
	p, _, _ := strings.Cut(link, "#")
	p = lineSuffix.ReplaceAllString(p, "")
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if t.Dir == "" {
			return ""
		}
		p = filepath.Join(t.Dir, p)
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return mdview.FileURL(p)
}

var lineSuffix = regexp.MustCompile(`(:\d+)+$`)

// spansPara joins spans into one string, noting its styled runs and where
// code and links run. href, when not nil, is what a link or a path in
// code opens.
func spansPara(spans []mdview.Span, href func(link string, code bool) string) para {
	var p para
	var b, plain strings.Builder
	n, at := 0, 0
	put := func(str string, typed bool) {
		for range str {
			p.ct.lo, p.ct.hi = append(p.ct.lo, at), append(p.ct.hi, at)
			if typed {
				p.ct.hi[n]++
				at++
			}
			n++
		}
		b.WriteString(str)
	}
	for i, s := range spans {
		t := mdview.Detab(s.Text)
		k := utf8.RuneCountInString(t)
		if s.Mono {
			put(nbsp, false) // room for the chip's padding, kept out of a copy
			p.code = append(p.code, [2]int{n, n + k})
		}
		if s.Bold || s.Italic || s.Mono || s.Strike {
			p.runs = append(p.runs, srun{n, n + k, s})
		}
		link := ""
		switch {
		case s.Link != "" && href != nil:
			link = href(s.Link, false)
		case s.Mono && href != nil:
			link = href(t, true)
		}
		if s.Link != "" || link != "" {
			p.links = append(p.links, [2]int{n, n + k})
			p.hrefs = append(p.hrefs, link)
		}
		put(t, true)
		if s.Mono && (i+1 == len(spans) || !strings.ContainsAny(spans[i+1].Text[:min(1, len(spans[i+1].Text))], ".,:;!?)")) {
			put(nbsp, false)
		}
		plain.WriteString(t)
	}
	p.text, p.plain = b.String(), plain.String()
	if len(p.code) > 0 {
		p.ct.text, p.ct.orig = p.text, []rune(p.plain)
	} else {
		p.ct = ctext{text: p.text}
	}
	return p
}

// reply draws a turn's reply as markdown in the prose width, code at the
// column's; a finished one ends with its copy buttons and times.
func (d *cdraw) reply(gtx layout.Context, i int, running bool, start, end time.Time) layout.Dimensions {
	it := &d.s.Content[i]
	r := d.t.reply(it, running)
	ws := []layout.Widget{func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, it.ID, r) }}
	if !running {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.replyFoot(gtx, it, r, start, end) })
	}
	return vstack(gtx, gtx.Dp(gapFoot)-gtx.Dp((hit-20)/2), ws...)
}

// inlineMono is inline code's size over its text's: mono 12.5 in 14 sp
// prose.
const inlineMono = 12.5 / 14

// quoteIndent is how far each block quote level moves its text right.
const quoteIndent = theme.SpaceM + theme.Space2XS

// gapBefore is the space above block i of blocks: 10 between paragraphs,
// 4 between items of one list, 20 above a heading and 6 below it, 12
// around code, tables and rules.
func gapBefore(gtx layout.Context, blocks []mdview.Block, i int) int {
	b, prev := &blocks[i], &blocks[i-1]
	switch {
	case b.Kind == mdview.Heading:
		return gtx.Dp(gapHeadTop)
	case prev.Kind == mdview.Heading:
		return gtx.Dp(gapHeadEnd)
	case b.Kind == mdview.Rule || prev.Kind == mdview.Rule:
		return gtx.Dp(gapHeadTop)
	case !isText(b) || !isText(prev):
		return gtx.Dp(gapCode)
	case b.Indent > 0 && prev.Indent > 0 && !(b.NewList && b.Indent == 1):
		return gtx.Dp(gapItem)
	}
	return gtx.Dp(gapPara)
}

func isText(b *mdview.Block) bool { return b.Kind == mdview.Para || b.Kind == mdview.Heading }

// markdown draws r's blocks top to bottom, a block quote's bar beside
// each block in it and across the gaps between them.
func (d *cdraw) markdown(gtx layout.Context, id string, r *reply) layout.Dimensions {
	th := d.th
	r.doc.Size = th.Sp(theme.Large)
	r.doc.Code = th.Fg
	y, w := 0, gtx.Constraints.Max.X
	for i := range r.blocks {
		b := &r.blocks[i]
		if i > 0 {
			g := gapBefore(gtx, r.blocks, i)
			for q := range min(b.Quote, r.blocks[i-1].Quote) {
				d.quoteBar(gtx, q, y, y+g)
			}
			y += g
		}
		key := id + "/" + strconv.Itoa(i)
		x := b.Quote*gtx.Dp(quoteIndent) + b.Indent*gtx.Dp(listIndent)
		var dims layout.Dimensions
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions { return d.para(gtx, key, b, r.paras[i]) })
		case mdview.Code:
			dims = at(gtx, image.Pt(x, y), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = w - x
				return d.codeBlock(gtx, r, key, b.Lang, b.Text)
			})
		case mdview.Rule:
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(x, y), Max: image.Pt(w, y+max(1, gtx.Dp(1)))}.Op())
			dims.Size.Y = max(1, gtx.Dp(1))
		case mdview.Mermaid:
			if b.Graph == nil {
				dims = at(gtx, image.Pt(x, y), func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = w - x
					return d.codeBlock(gtx, r, key, "mermaid", b.Text)
				})
				break
			}
			fallthrough
		default:
			dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
				return r.doc.Block(gtx, th, i)
			})
		}
		for q := range b.Quote {
			d.quoteBar(gtx, q, y, y+dims.Size.Y)
		}
		y += dims.Size.Y
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// quoteBar is block quote level q's bar from y0 to y1.
func (d *cdraw) quoteBar(gtx layout.Context, q, y0, y1 int) {
	x := q * gtx.Dp(quoteIndent)
	paint.FillShape(gtx.Ops, d.th.BorderStrong, clip.Rect{Min: image.Pt(x, y0), Max: image.Pt(x+gtx.Dp(2), y1)}.Op())
}

// linkColor is a link's text: the accent, readable on the page.
func (d *cdraw) linkColor() color.NRGBA { return d.th.Readable(d.th.Blue, d.th.Bg) }

// para is a paragraph, heading or list item: selectable text in the
// prose width, bold, italic and code in their own fonts, inline code on
// a chip that sits on the baseline, links in the accent and clickable.
func (d *cdraw) para(gtx layout.Context, key string, b *mdview.Block, p para) layout.Dimensions {
	th := d.th
	x := b.Quote*gtx.Dp(quoteIndent) + b.Indent*gtx.Dp(listIndent)
	w := min(d.proseW, gtx.Constraints.Max.X) - x
	if w <= 0 || p.text == "" {
		return layout.Dimensions{}
	}
	st := d.prose()
	if b.Kind == mdview.Heading {
		st.f.Weight = font.SemiBold
		st.c, st.lh = th.Fg, 1.4
		if b.Level <= 2 {
			st.size, st.lh = th.Sp(theme.Title), 1.35
		}
	}
	if b.Quote > 0 {
		st.c = th.Muted
	}
	sl := d.t.sel(key)
	sl.Styles, sl.Colors = sl.Styles[:0], sl.Colors[:0]
	for _, r := range p.runs {
		if f, scale, ok := mdview.SpanFont(th, st.f, r.s, inlineMono); ok {
			sl.Styles = append(sl.Styles, text.Style{Start: r.lo, End: r.hi, Font: f, Scale: scale})
		}
	}
	link := d.linkColor()
	for _, r := range p.links {
		sl.Colors = append(sl.Colors, widget.TextColor{Start: r[0], End: r[1], Color: link})
	}
	for _, r := range p.code {
		sl.Colors = append(sl.Colors, widget.TextColor{Start: r[0], End: r[1], Color: th.Fg})
	}
	d.copyAs(gtx, sl, p.ct)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, sl, st, p.text) })
	o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
	em := gtx.Sp(st.size)
	var rg []widget.Region
	for _, r := range p.code {
		rg = sl.Regions(r[0], r[1], rg[:0])
		for _, g := range rg {
			base := g.Bounds.Max.Y - g.Baseline
			box := image.Rect(g.Bounds.Min.X-gtx.Dp(4), base-em*78/100-gtx.Dp(2), g.Bounds.Max.X+gtx.Dp(4), base+em*22/100+gtx.Dp(2))
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(box, gtx.Dp(4)).Op(gtx.Ops))
		}
	}
	call.Add(gtx.Ops)
	thin := max(1, gtx.Dp(1))
	for _, r := range p.runs {
		if !r.s.Strike {
			continue
		}
		rg = sl.Regions(r.lo, r.hi, rg[:0])
		for _, g := range rg {
			y := g.Bounds.Max.Y - g.Baseline - em*27/100
			paint.FillShape(gtx.Ops, st.c, clip.Rect{Min: image.Pt(g.Bounds.Min.X, y), Max: image.Pt(g.Bounds.Max.X, y+thin)}.Op())
		}
	}
	for i, r := range p.links {
		rg = sl.Regions(r[0], r[1], rg[:0])
		for j, g := range rg {
			line := theme.Mix(link, th.Bg, 0.5)
			y := g.Bounds.Max.Y - g.Baseline + gtx.Dp(2)
			under := clip.Rect{Min: image.Pt(g.Bounds.Min.X, y), Max: image.Pt(g.Bounds.Max.X, y+thin)}
			if p.hrefs[i] == "" {
				paint.FillShape(gtx.Ops, line, under.Op())
				continue
			}
			c := d.t.click(key + "/a" + strconv.Itoa(i) + "/" + strconv.Itoa(j))
			if c.Clicked(gtx) && d.t.Open != nil {
				d.t.Open(p.hrefs[i])
			}
			if c.Hovered() {
				line = link
			}
			paint.FillShape(gtx.Ops, line, under.Op())
			at(gtx, g.Bounds.Min, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Exact(g.Bounds.Size())
				return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} })
			})
		}
	}
	o.Pop()
	if b.Marker != "" || b.Task != 0 {
		d.marker(gtx, b, x, st, td)
	}
	return layout.Dimensions{Size: image.Pt(x+td.Size.X, td.Size.Y)}
}

// marker draws a list item's marker in the gutter left of x, on the
// item's first line: a number right-aligned on its baseline, a bullet
// (a disc, then a ring, then a square, by depth) on its x-height, or a
// task box.
func (d *cdraw) marker(gtx layout.Context, b *mdview.Block, x int, st tstyle, td layout.Dimensions) {
	th := d.th
	c := theme.Mix(st.c, th.Bg, 0.3)
	base := td.Size.Y - td.Baseline // the first line's baseline
	em := gtx.Sp(st.size)
	if b.Task != 0 {
		s := gtx.Dp(14)
		top := base - em*36/100 - s/2
		r := image.Rect(x-gtx.Dp(20), top, x-gtx.Dp(20)+s, top+s)
		rr := clip.UniformRRect(r, gtx.Dp(3))
		if b.Task == 2 {
			paint.FillShape(gtx.Ops, th.Primary, rr.Op(gtx.Ops))
			at(gtx, r.Min.Add(image.Pt(gtx.Dp(1), gtx.Dp(1))), func(gtx layout.Context) layout.Dimensions {
				return sidebar.Icon(gtx, "check", s-gtx.Dp(2), th.OnPrimary)
			})
		} else {
			paint.FillShape(gtx.Ops, th.Muted, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
		}
		return
	}
	if b.Marker == "•" {
		r := gtx.Dp(2.5)
		cx, cy := x-gtx.Dp(10), base-em*27/100
		box := image.Rect(cx-r, cy-r, cx+r, cy+r)
		switch {
		case b.Indent <= 1:
			paint.FillShape(gtx.Ops, c, clip.Ellipse(box).Op(gtx.Ops))
		case b.Indent == 2:
			paint.FillShape(gtx.Ops, c, clip.Stroke{Path: clip.Ellipse(box.Inset(gtx.Dp(0.5))).Path(gtx.Ops), Width: float32(gtx.Dp(1.2))}.Op())
		default:
			paint.FillShape(gtx.Ops, c, clip.Rect(box.Inset(gtx.Dp(0.25))).Op())
		}
		return
	}
	mw := gtx.Dp(listIndent)
	call, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(mw, 0), Max: image.Pt(mw, 1<<20)}
		return widget.Label{Alignment: text.End, MaxLines: 1}.Layout(gtx, th.Shaper, st.f, st.size, b.Marker, material(gtx, c))
	})
	at(gtx, image.Pt(x-mw-gtx.Dp(6), base-(ld.Size.Y-ld.Baseline)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return ld })
}

// codeBlock is a fenced block: a 30 dp header bar with its language and
// Copy, the code in mono under it, selectable and never wrapped; a line
// wider than the block scrolls it sideways. A diff colors its added and
// removed lines.
func (d *cdraw) codeBlock(gtx layout.Context, r *reply, key, lang, code string) layout.Dimensions {
	th := d.th
	code = mdview.Detab(strings.TrimRight(code, "\n"))
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	px, py := gtx.Dp(14), gtx.Dp(12)
	sl := d.t.sel(key)
	sl.Colors = sl.Colors[:0]
	if lang == "diff" || lang == "patch" {
		sl.Colors = d.diffColors(sl.Colors, code)
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, sl, d.mono(), code) })
	sw := r.wide[key]
	if sw == nil {
		sw = new(mdview.Sideways)
		r.wide[key] = sw
	}
	body, bar := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		inner := op.Record(gtx.Ops)
		at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
		content := inner.Stop()
		h := td.Size.Y + 2*py
		extra := sw.Layout(gtx, th, w-2, h, td.Size.X+2*px, content)
		return layout.Dimensions{Size: image.Pt(w, h+extra)}
	})
	h := hh + bar.Size.Y
	rad := gtx.Dp(theme.RadiusPopover)
	rrect(gtx, image.Rect(0, 0, w, h), rad, th.Surface, th.BorderSubtle)
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(1, hh-1), Max: image.Pt(w-1, hh)}.Op())
	if lang != "" {
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, lang)
		})
		at(gtx, image.Pt(px, (hh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	}
	ck := d.t.click("copy:" + key)
	if ck.Clicked(gtx) {
		d.t.copy(gtx, key, code)
	}
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "Copy", d.t.isCopied(key))
	})
	at(gtx, image.Pt(w-gtx.Dp(2)-bd.Size.X, (hh-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	at(gtx, image.Pt(1, hh), func(gtx layout.Context) layout.Dimensions { body.Add(gtx.Ops); return bar })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// diffColors appends a diff's line colors to out: added lines green,
// removed red, hunk heads blue, file heads muted.
func (d *cdraw) diffColors(out []widget.TextColor, code string) []widget.TextColor {
	th := d.th
	n := 0
	for line := range strings.SplitSeq(code, "\n") {
		k := utf8.RuneCountInString(line)
		var c color.NRGBA
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			c = th.Muted
		case strings.HasPrefix(line, "+"):
			c = th.Readable(th.Green, th.Surface)
		case strings.HasPrefix(line, "-"):
			c = th.Readable(th.Red, th.Surface)
		case strings.HasPrefix(line, "@@"):
			c = th.Readable(th.Blue, th.Surface)
		}
		if c != (color.NRGBA{}) {
			out = append(out, widget.TextColor{Start: n, End: n + k, Color: c})
		}
		n += k + 1
	}
	return out
}

// replyFoot is a finished reply's row: Copy reply, Copy as Markdown, the
// time it finished and how long the turn took. Its buttons sit flush
// with the reply's left edge.
func (d *cdraw) replyFoot(gtx layout.Context, it *session.Item, r *reply, start, end time.Time) layout.Dimensions {
	th := d.th
	plain, md := d.t.click("copy:plain:"+it.ID), d.t.click("copy:md:"+it.ID)
	if plain.Clicked(gtx) {
		d.t.copy(gtx, "plain:"+it.ID, r.plain)
	}
	if md.Clicked(gtx) {
		d.t.copy(gtx, "md:"+it.ID, strings.TrimSpace(it.Text))
	}
	when := ""
	if !end.IsZero() {
		when = end.Local().Format("15:04")
		if !start.IsZero() && end.Sub(start) >= time.Second {
			when += " · " + duration(end.Sub(start))
		}
	}
	return at(gtx, image.Pt(-gtx.Dp(theme.SpaceS), 0), func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, 0,
			func(gtx layout.Context) layout.Dimensions {
				return d.ghost(gtx, plain, "clipboard", "Copy reply", d.t.isCopied("plain:"+it.ID))
			},
			func(gtx layout.Context) layout.Dimensions {
				return d.ghost(gtx, md, "", "Copy as Markdown", d.t.isCopied("md:"+it.ID))
			},
			func(gtx layout.Context) layout.Dimensions {
				if when == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, when)
				})
			},
		)
	})
}
