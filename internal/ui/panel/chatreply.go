package panel

import (
	"image"
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
	blocks []mdview.Block
	paras  map[int]para // text blocks' plain text and styled ranges
	doc    mdview.Doc   // tables, flowcharts, images and rules
	plain  string       // the reply without its markdown, for Copy reply
}

// para is a text block as one selectable string: code and link runs as
// rune ranges, drawn over it.
type para struct {
	text        string
	code, links [][2]int
	ct          ctext  // the way back from text to what the reply says
	plain       string // text without the code chips' padding
}

func (t *Transcript) reply(it *session.Item) *reply {
	r := t.docs[it.ID]
	if r != nil && r.text == it.Text {
		return r
	}
	r = &reply{text: it.Text, blocks: mdview.Parse([]byte(it.Text)), paras: map[int]para{}}
	r.doc.Set(r.blocks)
	var plain []string
	for i, b := range r.blocks {
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			p := spansPara(b.Spans)
			r.paras[i] = p
			plain = append(plain, b.Marker+sp(b.Marker)+p.plain)
		case mdview.Code, mdview.Mermaid:
			plain = append(plain, strings.TrimRight(b.Text, "\n"))
		case mdview.Table:
			for _, row := range b.Rows {
				var cells []string
				for _, c := range row {
					cells = append(cells, spansPara(c).plain)
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

// spansPara joins spans into one string, noting where code and links
// run.
func spansPara(spans []mdview.Span) para {
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
		switch {
		case s.Mono:
			put(nbsp, false) // room for the chip's padding, kept out of a copy
			p.code = append(p.code, [2]int{n, n + k})
			put(t, true)
			if i+1 == len(spans) || !strings.ContainsAny(spans[i+1].Text[:min(1, len(spans[i+1].Text))], ".,:;!?)") {
				put(nbsp, false)
			}
		case s.Link != "":
			p.links = append(p.links, [2]int{n, n + k})
			put(t, true)
		default:
			put(t, true)
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
	r := d.t.reply(it)
	ws := []layout.Widget{func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, it.ID, r) }}
	if !running {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.replyFoot(gtx, it, r, start, end) })
	}
	return vstack(gtx, gtx.Dp(gapFoot)-gtx.Dp((hit-20)/2), ws...)
}

// markdown draws r's blocks top to bottom: 10 between paragraphs, 4
// between list items, 20 above a heading and 6 below, 12 around code.
func (d *cdraw) markdown(gtx layout.Context, id string, r *reply) layout.Dimensions {
	th := d.th
	r.doc.Size = th.Sp(theme.Large)
	y, w := 0, gtx.Constraints.Max.X
	for i := range r.blocks {
		b := &r.blocks[i]
		if i > 0 {
			prev := &r.blocks[i-1]
			g := gtx.Dp(gapPara)
			switch {
			case b.Kind == mdview.Heading:
				g = gtx.Dp(gapHeadTop)
			case prev.Kind == mdview.Heading:
				g = gtx.Dp(gapHeadEnd)
			case b.Kind == mdview.Code || prev.Kind == mdview.Code:
				g = gtx.Dp(gapCode)
			case b.Indent > 0 && prev.Indent > 0:
				g = gtx.Dp(gapItem)
			}
			y += g
		}
		key := id + "/" + strconv.Itoa(i)
		var dims layout.Dimensions
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions { return d.para(gtx, key, b, r.paras[i]) })
		case mdview.Code:
			dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions { return d.codeBlock(gtx, key, b.Lang, b.Text) })
		case mdview.Mermaid:
			if b.Graph == nil {
				dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions { return d.codeBlock(gtx, key, "mermaid", b.Text) })
				break
			}
			fallthrough
		default:
			dims = at(gtx, image.Pt(0, y), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
				return r.doc.Block(gtx, th, i)
			})
		}
		y += dims.Size.Y
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// para is a paragraph, heading or list item: selectable text in the
// prose width, inline code on a chip that sits on the baseline, links
// underlined.
func (d *cdraw) para(gtx layout.Context, key string, b *mdview.Block, p para) layout.Dimensions {
	th := d.th
	x := b.Quote*gtx.Dp(14) + b.Indent*gtx.Dp(listIndent)
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
	link := th.Readable(th.Blue, th.Bg)
	for _, r := range p.links {
		rg = sl.Regions(r[0], r[1], rg[:0])
		for _, g := range rg {
			y := g.Bounds.Max.Y - g.Baseline + gtx.Dp(2)
			paint.FillShape(gtx.Ops, link, clip.Rect{Min: image.Pt(g.Bounds.Min.X, y), Max: image.Pt(g.Bounds.Max.X, y+max(1, gtx.Dp(1)))}.Op())
		}
	}
	o.Pop()
	for q := range b.Quote {
		bx := q * gtx.Dp(14)
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(bx, 0), Max: image.Pt(bx+gtx.Dp(2), td.Size.Y)}.Op())
	}
	if b.Marker != "" || b.Task != 0 {
		d.marker(gtx, b, x, st)
	}
	return layout.Dimensions{Size: image.Pt(x+td.Size.X, td.Size.Y)}
}

// marker draws a list item's bullet, number or task box left of x.
func (d *cdraw) marker(gtx layout.Context, b *mdview.Block, x int, st tstyle) {
	th := d.th
	line := int(float32(gtx.Sp(st.size)) * st.lh)
	if b.Task != 0 {
		s := gtx.Dp(14)
		r := image.Rect(x-gtx.Dp(20), (line-s)/2, x-gtx.Dp(20)+s, (line-s)/2+s)
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
	mw := gtx.Dp(16)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(mw, 0), Max: image.Pt(mw, line)}
	at(g, image.Pt(x-mw-gtx.Dp(4), 0), func(gtx layout.Context) layout.Dimensions {
		return widget.Label{Alignment: text.End, MaxLines: 1, LineHeightScale: st.lh}.Layout(gtx, th.Shaper, st.f, st.size, b.Marker, material(gtx, th.Muted))
	})
}

// codeBlock is a fenced block: a 30 dp header bar with its language and
// Copy, the code in mono under it, selectable.
func (d *cdraw) codeBlock(gtx layout.Context, key, lang, code string) layout.Dimensions {
	th := d.th
	code = strings.TrimRight(code, "\n")
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	px, py := gtx.Dp(14), gtx.Dp(12)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(key), d.mono(), mdview.Detab(code))
	})
	h := hh + td.Size.Y + 2*py
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
	at(gtx, image.Pt(px, hh+py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// replyFoot is a finished reply's row: Copy reply, Copy as Markdown, the
// time it finished and how long the turn took. Its buttons sit flush
// with the reply's left edge.
func (d *cdraw) replyFoot(gtx layout.Context, it *session.Item, r *reply, start, end time.Time) layout.Dimensions {
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
				return layout.Inset{Left: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return d.footMeta(gtx, it, when)
				})
			},
		)
	})
}
