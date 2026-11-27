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

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// reply is an assistant reply parsed once per text.
type reply struct {
	text   string
	live   bool // parsed healed, as a reply still streaming
	blocks []mdview.Block
	paras  map[int]para                // text blocks' plain text and styled ranges
	tables map[int]string              // precomputed tablePlain by block index
	doc    mdview.Doc                  // tables, flowcharts and images
	plain  string                      // the reply without its markdown, for Copy reply
	wide   map[string]*mdview.Sideways // code blocks' sideways scroll, by key
	code   map[string]*hlBlock         // code blocks' highlighting, by key
	// A prompt's: pad is the no-break spaces around each image chip's
	// name, {-1, -1} for none; flat says its text draws as typed, not as
	// markdown; typed is what a selection of all of it copies.
	pad   [2]int
	flat  bool
	typed string
}

// noChips is a reply's pad: its [Image #N] stay text.
var noChips = [2]int{-1, -1}

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
	if r != nil && r.text == it.Text && r.live == live && r.pad == noChips {
		return r
	}
	src := it.Text
	if live {
		src = mdview.Heal(src)
	}
	r = t.parse(it.Text, src, r, noChips)
	r.live = live
	t.docs[it.ID] = r
	return r
}

// promptDoc is prompt id's text src parsed, each [Image #N] in it widened
// by pad for its chip.
func (t *Transcript) promptDoc(id, src string, pad [2]int) *reply {
	r := t.docs[id]
	if r != nil && r.text == src && r.pad == pad {
		return r
	}
	r = t.parse(src, src, r, pad)
	r.flat = mdview.Plain(src, r.blocks)
	t.docs[id] = r
	return r
}

// parse is text's doc, parsed from src; old's code blocks keep their
// scroll and tokens.
func (t *Transcript) parse(text, src string, old *reply, pad [2]int) *reply {
	r := &reply{text: text, pad: pad, blocks: mdview.ParseChat([]byte(src)), paras: map[int]para{}, tables: map[int]string{}, wide: map[string]*mdview.Sideways{}, code: map[string]*hlBlock{}}
	if old != nil {
		r.wide, r.code = old.wide, old.code // a code block keeps its scroll and tokens as the reply grows
	}
	r.doc.Set(r.blocks)
	var plain []string
	for i, b := range r.blocks {
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			p := spansChips(b.Spans, t.href, pad)
			r.paras[i] = p
			plain = append(plain, b.Marker+sp(b.Marker)+p.plain)
		case mdview.Code, mdview.Mermaid:
			plain = append(plain, strings.TrimRight(b.Text, "\n"))
		case mdview.Table:
			tp := tablePlain(&b)
			r.tables[i] = tp
			plain = append(plain, tp)
		case mdview.Images:
			for _, im := range b.Imgs {
				plain = append(plain, im.Alt)
			}
		}
	}
	r.plain = strings.Join(plain, "\n\n")
	return r
}

// tablePlain is a table as a sheet takes it pasted: a line per row, its
// cells a tab apart.
func tablePlain(b *mdview.Block) string {
	rows := make([]string, len(b.Rows))
	for i, row := range b.Rows {
		var cells []string
		for _, c := range row {
			cells = append(cells, spansPara(c, nil).plain)
		}
		rows[i] = strings.Join(cells, "\t")
	}
	return strings.Join(rows, "\n")
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
	key := t.Dir + "\x00" + p
	if got, ok := t.hrefs[key]; ok {
		return got
	}
	res := ""
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		res = mdview.FileURL(p)
	}
	if t.hrefs == nil {
		t.hrefs = map[string]string{}
	} else if len(t.hrefs) >= 4096 {
		clear(t.hrefs)
	}
	t.hrefs[key] = res
	return res
}

var lineSuffix = regexp.MustCompile(`(:\d+)+$`)

// spansPara joins spans into one string, noting its styled runs and where
// code and links run. href, when not nil, is what a link or a path in
// code opens.
func spansPara(spans []mdview.Span, href func(link string, code bool) string) para {
	return spansChips(spans, href, noChips)
}

// spansChips is spansPara with each [Image #N] outside code widened to
// pad[0] no-break spaces, the name "Image #N" and pad[1] spaces, the room
// its chip takes; a copy gives back the token. noChips leaves them text.
func spansChips(spans []mdview.Span, href func(link string, code bool) string, pad [2]int) para {
	var p para
	var b, plain strings.Builder
	n, at := 0, 0
	// put writes str, each rune standing for typed runes [lo, hi).
	put := func(str string, lo, hi int) {
		for range str {
			p.ct.lo, p.ct.hi = append(p.ct.lo, lo), append(p.ct.hi, hi)
			n++
		}
		b.WriteString(str)
	}
	text := func(str string) { // str as typed, rune for rune
		for _, c := range str {
			put(string(c), at, at+1)
			at++
		}
	}
	for i, s := range spans {
		t := mdview.Detab(s.Text)
		if s.Mono {
			put(nbsp, at, at) // room for the chip's padding, kept out of a copy
		}
		lo := n
		var refs []imageRef
		if pad != noChips && !s.Mono {
			refs = imageRefs(t)
		}
		last := 0
		for _, r := range refs {
			text(t[last:r.start])
			tok := utf8.RuneCountInString(t[r.start:r.end])
			name := "Image" + nbsp + "#" + strconv.Itoa(r.n)
			slot := n
			put(strings.Repeat(nbsp, pad[0]), at, at+tok)
			start := n
			put(name, at, at+tok)
			label := [2]int{start, n}
			put(strings.Repeat(nbsp, pad[1]), at, at+tok)
			p.ct.chips = append(p.ct.chips, chip{label: label, slot: [2]int{slot, n}, name: strings.ReplaceAll(name, nbsp, " ")})
			at += tok
			last = r.end
		}
		text(t[last:])
		hi := n
		if s.Mono {
			p.code = append(p.code, [2]int{lo, hi})
		}
		if s.Bold || s.Italic || s.Mono || s.Strike {
			p.runs = append(p.runs, srun{lo, hi, s})
		}
		link := ""
		switch {
		case s.Link != "" && href != nil:
			link = href(s.Link, false)
		case s.Mono && href != nil:
			link = href(t, true)
		}
		if s.Link != "" || link != "" {
			p.links = append(p.links, [2]int{lo, hi})
			p.hrefs = append(p.hrefs, link)
		}
		if s.Mono && (i+1 == len(spans) || !strings.ContainsAny(spans[i+1].Text[:min(1, len(spans[i+1].Text))], ".,:;!?)")) {
			put(nbsp, at, at)
		}
		plain.WriteString(t)
	}
	p.text, p.plain = b.String(), plain.String()
	if len(p.code) > 0 || len(p.ct.chips) > 0 {
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
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.replyFoot(gtx, i, it, r, start, end) })
	}
	return vstack(gtx, gtx.Dp(gapFoot)-gtx.Dp((hit-20)/2), ws...)
}

// between draws the agent's words between calls: a report, findings it
// writes mid-turn, at the reply's reading size with a bar in the agent's
// color beside it, so it reads as the agent speaking and not as one more
// work row; short narration ("Let me check the tests") a step down, muted.
func (d *cdraw) between(gtx layout.Context, it *session.Item) layout.Dimensions {
	r := d.t.reply(it, false)
	if !report(it.Text) {
		d.narration = true
		defer func() { d.narration = false }()
		return d.markdown(gtx, it.ID, r)
	}
	pad := gtx.Dp(theme.SpaceXS) // a little air off the work rows
	dims := at(gtx, image.Pt(0, pad), func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, it.ID, r) })
	dims.Size.Y += 2 * pad
	bw, x := gtx.Dp(theme.Space2XS), -gtx.Dp(theme.SpaceS+theme.Space2XS)
	paint.FillShape(gtx.Ops, theme.Mix(d.th.Bg, d.th.Blue, 0.7), clip.UniformRRect(image.Rect(x, 0, x+bw, dims.Size.Y), bw/2).Op(gtx.Ops))
	return dims
}

// inlineMono is inline code's size over its text's: mono 12.5 in 14 sp
// prose.
const inlineMono = 12.5 / 14

// quoteIndent is how far each block quote level moves its text right.
const quoteIndent = theme.SpaceL

// foldIndent is how far a <details> body moves right, under its
// summary's text: the chevron's 14 and a gap, as a tool row's body.
const foldIndent = 14 + theme.SpaceS

// gapBefore is the space between blocks prev and b: 10 between
// paragraphs, 4 between items of one list, 20 above a heading and 6
// below it, 12 around code, tables and rules.
func gapBefore(gtx layout.Context, prev, b *mdview.Block) int {
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

// shownBlocks are r's blocks outside closed <details>, and how deep in
// open ones each is, by index.
func (d *cdraw) shownBlocks(gtx layout.Context, id string, r *reply) (shown []int, depth map[int]int) {
	open := map[int]bool{0: true} // folds whose bodies show
	deep := map[int]int{}         // folds' depth, by number
	depth = map[int]int{}
	for i := range r.blocks {
		b := &r.blocks[i]
		if b.Summary {
			if !open[b.Parent] {
				continue
			}
			deep[b.Fold] = deep[b.Parent] + 1
			open[b.Fold] = d.t.toggled(gtx, id+"/fold/"+strconv.Itoa(b.Fold), b.Open)
			depth[i] = deep[b.Parent]
			shown = append(shown, i)
			continue
		}
		if !open[b.Fold] {
			continue
		}
		depth[i] = deep[b.Fold]
		shown = append(shown, i)
	}
	return shown, depth
}

// markdown draws r's blocks top to bottom, those in a closed <details>
// left out, and each run of a block quote with one bar beside it.
func (d *cdraw) markdown(gtx layout.Context, id string, r *reply) layout.Dimensions {
	th := d.th
	r.doc.Size = th.Sp(theme.Large)
	r.doc.Code, r.doc.CodeFill = th.Fg, th.SurfaceSecondary
	y, w, right := 0, gtx.Constraints.Max.X, 0
	shown, depth := d.shownBlocks(gtx, id, r)
	var runs []int // where each quote level's open run started
	for n, i := range shown {
		b := &r.blocks[i]
		if n > 0 {
			y += gapBefore(gtx, &r.blocks[shown[n-1]], b)
		}
		for len(runs) < b.Quote {
			runs = append(runs, y)
		}
		key := id + "/" + strconv.Itoa(i)
		fx := depth[i] * gtx.Dp(foldIndent)
		x := b.Quote*gtx.Dp(quoteIndent) + b.Indent*gtx.Dp(listIndent)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w-fx, 1<<20)}
		var dims layout.Dimensions
		switch b.Kind {
		case mdview.Para, mdview.Heading:
			dims = at(g, image.Pt(fx, y), func(gtx layout.Context) layout.Dimensions {
				if b.Summary {
					return d.summary(gtx, id, key, b, r.paras[i])
				}
				return d.para(gtx, key, b, r.paras[i])
			})
		case mdview.Code:
			dims = at(g, image.Pt(fx+x, y), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X -= x
				return d.codeBlock(gtx, r, key, b.Lang, b.Text)
			})
			dims.Size.X += x
		case mdview.Rule:
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(fx+x, y), Max: image.Pt(w, y+max(1, gtx.Dp(1)))}.Op())
			dims.Size = image.Pt(w-fx, max(1, gtx.Dp(1)))
		case mdview.Mermaid:
			if b.Graph == nil {
				dims = at(g, image.Pt(fx+x, y), func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X -= x
					return d.codeBlock(gtx, r, key, "mermaid", b.Text)
				})
				dims.Size.X += x
				break
			}
			fallthrough
		case mdview.Table:
			dims = at(g, image.Pt(fx, y), func(gtx layout.Context) layout.Dimensions {
				return d.whole(gtx, key, r.tables[i], func(gtx layout.Context) layout.Dimensions { return r.doc.Block(gtx, th, i) })
			})
		default:
			dims = at(g, image.Pt(fx, y), func(gtx layout.Context) layout.Dimensions { return r.doc.Block(gtx, th, i) })
		}
		y += dims.Size.Y
		right = max(right, fx+dims.Size.X)
		next := 0
		if n+1 < len(shown) {
			next = r.blocks[shown[n+1]].Quote
		}
		for len(runs) > next {
			q := len(runs) - 1
			d.quoteBar(gtx, fx+q*gtx.Dp(quoteIndent), runs[q], y)
			runs = runs[:q]
		}
	}
	if d.own { // the bubble fits the text
		w = min(w, right)
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// quoteBar is a block quote's bar at x from y0 to y1, its ends round.
func (d *cdraw) quoteBar(gtx layout.Context, x, y0, y1 int) {
	bw := gtx.Dp(theme.Space2XS)
	paint.FillShape(gtx.Ops, d.th.BorderStrong, clip.UniformRRect(image.Rect(x, y0, x+bw, y1), bw/2).Op(gtx.Ops))
}

// summary is a <details> block's summary: a row like a tool row's, its
// chevron, then the summary in the label weight; a click opens or closes
// the body under it.
func (d *cdraw) summary(gtx layout.Context, id, key string, b *mdview.Block, p para) layout.Dimensions {
	th := d.th
	ck := d.t.click(id + "/fold/" + strconv.Itoa(b.Fold))
	open := isOpen(d.t.open, id+"/fold/"+strconv.Itoa(b.Fold), b.Open)
	x := b.Quote*gtx.Dp(quoteIndent) + b.Indent*gtx.Dp(listIndent)
	st := d.prose()
	st.f, st.c = medium(st.f), th.Fg
	tw := min(d.proseW, gtx.Constraints.Max.X) - x - gtx.Dp(foldIndent)
	if tw <= 0 {
		return layout.Dimensions{}
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(tw, gtx.Constraints.Max.Y)}
	tc, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return widget.Label{LineHeightScale: st.lh}.Layout(gtx, th.Shaper, st.f, st.size, p.plain, material(gtx, st.c))
	})
	pad := gtx.Dp(theme.SpaceS)
	h := max(gtx.Dp(hit), td.Size.Y)
	row := image.Pt(pad+gtx.Dp(foldIndent)+td.Size.X+pad, h)
	return at(gtx, image.Pt(x-pad, 0), func(gtx layout.Context) layout.Dimensions {
		return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
			r := gtx.Dp(theme.RadiusControl)
			switch {
			case ck.Pressed():
				paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rectangle{Max: row}, r).Op(gtx.Ops))
			case ck.Hovered():
				paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rectangle{Max: row}, r).Op(gtx.Ops))
			}
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			ty := (h - td.Size.Y) / 2
			base := ty + td.Size.Y - td.Baseline
			icon(gtx, image.Pt(pad+gtx.Dp(1), base-gtx.Sp(st.size)*36/100-gtx.Dp(6)), chev, 12, th.Muted)
			at(gtx, image.Pt(pad+gtx.Dp(foldIndent), ty), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
			return layout.Dimensions{Size: row}
		})
	})
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
	d.copyAs(sl, p.ct)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, sl, st, p.text) })
	o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
	em := gtx.Sp(st.size)
	pill := th.SurfaceSecondary
	if d.own && !d.ledger { // on the prompt's bubble, as its code blocks
		pill = th.Surface
	}
	var rg []widget.Region
	for _, r := range p.code {
		rg = sl.Regions(r[0], r[1], rg[:0])
		for _, g := range rg {
			base := g.Bounds.Max.Y - g.Baseline
			px, py := gtx.Dp(theme.SpaceXS), gtx.Dp(theme.Space2XS)
			box := image.Rect(g.Bounds.Min.X-px, base-em*78/100-py, g.Bounds.Max.X+px, base+em*22/100+py)
			paint.FillShape(gtx.Ops, pill, clip.UniformRRect(box, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
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
			y := g.Bounds.Max.Y - g.Baseline + gtx.Dp(theme.Space2XS)
			under := clip.Rect{Min: image.Pt(g.Bounds.Min.X, y), Max: image.Pt(g.Bounds.Max.X, y+thin)}
			if p.hrefs[i] == "" {
				paint.FillShape(gtx.Ops, line, under.Op())
				continue
			}
			c := d.t.click(key + "/a" + strconv.Itoa(i) + "/" + strconv.Itoa(j))
			if c.Clicked(gtx) {
				d.t.openTarget(p.hrefs[i])
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
	d.imageChips(gtx, sl, p.ct.chips, st)
	o.Pop()
	if b.Marker != "" || b.Task != 0 {
		d.marker(gtx, b, x, st, td)
	}
	return layout.Dimensions{Size: image.Pt(x+td.Size.X, td.Size.Y)}
}

// marker draws a list item's marker in the gutter left of x, on the
// item's first line: a number right-aligned on its baseline, a bullet
// (a disc, then a ring, then a square, by depth) on its x-height, or a
// task box drawn as pitwall's checkbox.
func (d *cdraw) marker(gtx layout.Context, b *mdview.Block, x int, st tstyle, td layout.Dimensions) {
	th := d.th
	c := theme.Mix(st.c, th.Bg, 0.3)
	base := td.Size.Y - td.Baseline // the first line's baseline
	em := gtx.Sp(st.size)
	mid := base - em*27/100 // the x-height's middle
	if b.Task != 0 {
		s := gtx.Dp(theme.SpaceL)
		r := image.Rect(x-gtx.Dp(theme.SpaceXS)-s, mid-s/2, x-gtx.Dp(theme.SpaceXS), mid-s/2+s)
		rad := gtx.Dp(theme.RadiusControl)
		if b.Task == 2 {
			paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(r, rad).Op(gtx.Ops))
			k := float32(s) / 16
			o := f32.Pt(float32(r.Min.X), float32(r.Min.Y))
			var p clip.Path
			p.Begin(gtx.Ops)
			p.MoveTo(o.Add(f32.Pt(4*k, 8.5*k)))
			p.LineTo(o.Add(f32.Pt(7*k, 11.5*k)))
			p.LineTo(o.Add(f32.Pt(12*k, 5*k)))
			paint.FillShape(gtx.Ops, th.OnPrimary, clip.Stroke{Path: p.End(), Width: 2 * k}.Op())
		} else {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.25), clip.UniformRRect(r, rad).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(r.Inset(1), rad-1).Op(gtx.Ops))
		}
		return
	}
	if b.Marker == "•" {
		r := gtx.Dp(2.5) // a 5 dp bullet, a glyph's size rather than a space
		cx := x - gtx.Dp(listIndent)/2
		box := image.Rect(cx-r, mid-r, cx+r, mid+r)
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
	// why: "100." is wider than the gutter; it reaches left of it, as a browser's outside marker does, instead of cutting to "1…".
	mw := 3 * gtx.Dp(listIndent)
	call, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(mw, 0), Max: image.Pt(mw, 1<<20)}
		return widget.Label{Alignment: text.End, MaxLines: 1}.Layout(gtx, th.Shaper, st.f, st.size, b.Marker, material(gtx, c))
	})
	at(gtx, image.Pt(x-mw-gtx.Dp(theme.SpaceS), base-(ld.Size.Y-ld.Baseline)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return ld })
}

// replyFoot is a finished reply's row: Copy reply, Copy as Markdown, the
// time it finished and how long the turn took. Its buttons sit flush
// with the reply's left edge.
func (d *cdraw) replyFoot(gtx layout.Context, idx int, it *session.Item, r *reply, start, end time.Time) layout.Dimensions {
	plain, md := d.t.click("copy:plain:"+it.ID), d.t.click("copy:md:"+it.ID)
	if plain.Clicked(gtx) {
		d.t.copy(gtx, "plain:"+it.ID, r.plain)
	}
	if md.Clicked(gtx) {
		d.t.copy(gtx, "md:"+it.ID, strings.TrimSpace(it.Text))
	}
	// The time it came; how long the turn took is its Worked for line's
	// in Column, or here in Ledger where the gutter carries the start time.
	when := ""
	if d.ledger {
		if !start.IsZero() && !end.IsZero() && end.Sub(start) >= time.Second {
			when = duration(end.Sub(start))
		}
	} else if !end.IsZero() {
		when = end.Local().Format("15:04")
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
					return d.footMeta(gtx, idx, when)
				})
			},
		)
	})
}
