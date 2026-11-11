package mdview

import (
	"image"
	"image/color"
	"math"
	"path/filepath"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/richtext"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Doc draws parsed blocks: the reader's file, or an agent's reply in the
// phone's chat. It keeps each block's link, click and scroll state until
// Set gives it other blocks.
type Doc struct {
	// Follow runs a link clicked in the blocks; nil leaves links inert.
	Follow func(link string)
	// Picture is what an image shows, for a block width pixels wide; nil
	// shows each image's name instead.
	Picture func(im Img, width int) Pic
	// Size is the body text's size; zero is the reader's, the type
	// scale's Title step.
	Size unit.Sp

	blocks  []Block
	th      *theme.Theme
	metric  unit.Metric
	mono    float32
	texts   map[int]*richtext.InteractiveText
	styles  map[int][]richtext.SpanStyle // texts' spans styled, by the same key
	clicks  map[int]*widget.Clickable    // image links
	wide    map[int]*sideways
	natural map[int][]int // a table's columns, unwrapped
	cellOps op.Ops
	cellTxt richtext.InteractiveText
}

// Pic is an image as a block shows it: Img at Size once loaded, else the
// line Note in its place.
type Pic struct {
	Img  paint.ImageOp
	Size image.Point
	Note string
}

// sideways is a code block's or a table's sideways scroll, when it is
// wider than the column.
type sideways struct {
	g   gesture.Scroll
	x   int
	bar int // the bar's tag
}

// Set makes b the blocks drawn, dropping the old ones' state.
func (d *Doc) Set(b []Block) {
	d.blocks = b
	d.texts, d.styles, d.clicks, d.wide, d.natural = nil, nil, nil, nil, nil
}

// Blocks are the blocks drawn.
func (d *Doc) Blocks() []Block { return d.blocks }

// prep follows a change of theme or scale.
func (d *Doc) prep(gtx gl.Context, th *theme.Theme) {
	if d.th != th || d.metric != gtx.Metric {
		d.th, d.metric, d.mono, d.natural, d.styles = th, gtx.Metric, MonoScale(th), nil, nil
	}
}

func (d *Doc) size(th *theme.Theme) unit.Sp {
	if d.Size != 0 {
		return d.Size
	}
	return th.Sp(theme.Title)
}

// Layout draws every block top to bottom, gtx.Constraints.Max.X wide.
func (d *Doc) Layout(gtx gl.Context, th *theme.Theme) gl.Dimensions {
	d.prep(gtx, th)
	body := gtx.Sp(d.size(th))
	y := 0
	for i := range d.blocks {
		t := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		dims := d.Block(gtx, th, i)
		t.Pop()
		g := gap(d.blocks, i, body)
		d.bars(gtx, th, i, image.Pt(0, y), dims.Size.Y, dims.Size.Y+g)
		y += dims.Size.Y + g
	}
	return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
}

// gap is the space after block i.
func gap(doc []Block, i, body int) int {
	if i+1 >= len(doc) {
		return 0
	}
	b, n := &doc[i], &doc[i+1]
	switch {
	case n.Kind == Heading && n.Level <= 2:
		return body * 2
	case n.Kind == Heading:
		return body * 3 / 2
	case b.Kind == Heading:
		return body * 3 / 4
	case b.Indent > 0 && n.Indent > 0 && n.Kind == Para && b.Kind == Para:
		return body / 4
	}
	return body
}

// bars draws the block quote bars beside block i at at, own high, or
// joined high for a quote the next block is in too.
func (d *Doc) bars(gtx gl.Context, th *theme.Theme, i int, at image.Point, own, joined int) {
	for q := 1; q <= d.blocks[i].Quote; q++ {
		h := own
		if i+1 < len(d.blocks) && d.blocks[i+1].Quote >= q {
			h = joined
		}
		x := at.X + (q-1)*gtx.Dp(16)
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(x, at.Y), Max: image.Pt(x+gtx.Dp(3), at.Y+h)}.Op())
	}
}

// indent is how far block b sits right of the column's edge.
func indent(gtx gl.Context, b *Block) int {
	return b.Quote*gtx.Dp(16) + b.Indent*gtx.Dp(24)
}

// Block draws block i at the origin, gtx.Constraints.Max.X wide, and
// returns its size.
func (d *Doc) Block(gtx gl.Context, th *theme.Theme, i int) gl.Dimensions {
	d.prep(gtx, th)
	b := &d.blocks[i]
	in := indent(gtx, b)
	w := gtx.Constraints.Max.X - in
	if w <= 0 {
		return gl.Dimensions{}
	}
	t := op.Offset(image.Pt(in, 0)).Push(gtx.Ops)
	defer t.Pop()
	gtx.Constraints = gl.Constraints{Max: image.Pt(w, 1<<24)}
	body := d.size(th)
	fg := th.Fg
	if b.Quote > 0 {
		fg = th.Muted
	}
	var dims gl.Dimensions
	switch b.Kind {
	case Para:
		dims = d.text(gtx, th, i*1024, b.Spans, body, fg, false)
		d.marker(gtx, th, b)
	case Heading:
		scale := map[int]float32{1: 1.75, 2: 1.45, 3: 1.2, 4: 1.05}[b.Level]
		c := fg
		if scale == 0 {
			scale = 1
			if b.Level == 6 {
				c = th.Muted
			}
		}
		dims = d.text(gtx, th, i*1024, b.Spans, body*unit.Sp(scale), c, true)
		if b.Level <= 2 {
			y := dims.Size.Y + gtx.Dp(theme.SpaceS)
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
			dims.Size.Y = y + 1
		}
	case Code:
		dims = d.code(gtx, th, i, b.Text)
	case Mermaid:
		if b.Graph != nil {
			dims = d.graph(gtx, th, b.Graph)
			break
		}
		nh := gtx.Sp(th.Sp(theme.Body)) * 3 / 2
		label(gtx, th, image.Point{}, nh, w, th.UIFont, th.Sp(theme.Body), th.TextQuiet, b.Note)
		ct := op.Offset(image.Pt(0, nh)).Push(gtx.Ops)
		cd := d.code(gtx, th, i, b.Text)
		ct.Pop()
		dims = gl.Dimensions{Size: image.Pt(w, nh+cd.Size.Y)}
	case Table:
		dims = d.table(gtx, th, i, b)
	case Rule:
		h := gtx.Dp(theme.SpaceS)
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(0, h), Max: image.Pt(w, h+1)}.Op())
		dims.Size = image.Pt(w, 2*h+1)
	case Images:
		dims = d.images(gtx, th, i, b)
	}
	dims.Size.X = w
	return dims
}

// marker draws a list item's bullet, number or task box left of it.
func (d *Doc) marker(gtx gl.Context, th *theme.Theme, b *Block) {
	if b.Marker == "" {
		return
	}
	gw := gtx.Dp(22)
	line := gtx.Sp(d.size(th)) * 7 / 5
	if b.Task != 0 {
		s := gtx.Dp(14)
		r := image.Rect(-gw+gtx.Dp(2), (line-s)/2, -gw+gtx.Dp(2)+s, (line-s)/2+s)
		rr := clip.UniformRRect(r, gtx.Dp(3))
		if b.Task == 2 {
			paint.FillShape(gtx.Ops, th.Primary, rr.Op(gtx.Ops))
			t := op.Offset(r.Min.Add(image.Pt(gtx.Dp(1), gtx.Dp(1)))).Push(gtx.Ops)
			sidebar.Icon(gtx, "check", s-gtx.Dp(2), th.OnPrimary)
			t.Pop()
		} else {
			paint.FillShape(gtx.Ops, th.Muted, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
		}
		return
	}
	lg := gtx
	lg.Constraints = gl.Constraints{Min: image.Pt(gw-gtx.Dp(6), 0), Max: image.Pt(gw-gtx.Dp(6), line)}
	t := op.Offset(image.Pt(-gw, 0)).Push(gtx.Ops)
	widget.Label{Alignment: text.End, MaxLines: 1, LineHeightScale: 1.4}.Layout(lg, th.Shaper, th.UIFont, d.size(th), b.Marker, colorCall(gtx, th.Muted))
	t.Pop()
}

// state is the click state of the text key names.
func (d *Doc) state(key int) *richtext.InteractiveText {
	st := d.texts[key]
	if st == nil {
		if d.texts == nil {
			d.texts = map[int]*richtext.InteractiveText{}
		}
		st = &richtext.InteractiveText{}
		d.texts[key] = st
	}
	return st
}

// clicked runs the links clicked in st.
func (d *Doc) clicked(gtx gl.Context, st *richtext.InteractiveText) {
	for {
		span, ev, ok := st.Update(gtx)
		if !ok {
			return
		}
		if u, _ := span.Get("url").(string); ev.Type == richtext.Click && u != "" && d.Follow != nil {
			d.Follow(u)
		}
	}
}

// text draws spans wrapped to the width, taking link clicks; key names
// its click state.
func (d *Doc) text(gtx gl.Context, th *theme.Theme, key int, spans []Span, size unit.Sp, c color.NRGBA, heading bool) gl.Dimensions {
	st := d.state(key)
	d.clicked(gtx, st)
	return richtext.TextStyle{State: st, Styles: d.styled(key, th, spans, size, c, heading), Shaper: th.Shaper, LineHeightScale: 1.4}.Layout(gtx)
}

// styled is d.rich of the text key names, styled once for its blocks
// and theme.
func (d *Doc) styled(key int, th *theme.Theme, spans []Span, size unit.Sp, c color.NRGBA, heading bool) []richtext.SpanStyle {
	if s, ok := d.styles[key]; ok {
		return s
	}
	if d.styles == nil {
		d.styles = map[int][]richtext.SpanStyle{}
	}
	s := d.rich(th, spans, size, c, heading)
	d.styles[key] = s
	return s
}

// rich is spans styled for the reader.
func (d *Doc) rich(th *theme.Theme, spans []Span, size unit.Sp, c color.NRGBA, heading bool) []richtext.SpanStyle {
	link := th.Readable(th.Blue, th.Bg)
	code := th.Readable(theme.Mix(th.Fg, th.Purple, 0.45), th.Bg)
	out := make([]richtext.SpanStyle, 0, len(spans))
	for _, s := range spans {
		f, sz, col := th.UIFont, size, c
		if heading {
			f.Weight = font.SemiBold
		}
		if s.Bold {
			f.Weight = font.Bold
		}
		if s.Italic {
			f.Style = font.Italic
		}
		if s.Mono {
			f, sz, col = th.MonoFont, size*unit.Sp(d.mono), code
		}
		if s.Strike {
			col = th.TextQuiet
		}
		ss := richtext.SpanStyle{Font: f, Size: sz, Color: col, Content: Detab(s.Text)}
		if s.Link != "" {
			ss.Color, ss.Interactive = link, true
			ss.Set("url", s.Link)
		}
		out = append(out, ss)
	}
	return GluePunct(out)
}

// code draws a code block on a panel, its lines unwrapped: a line wider
// than the column scrolls sideways.
func (d *Doc) code(gtx gl.Context, th *theme.Theme, i int, s string) gl.Dimensions {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceM)
	lg := gtx
	lg.Constraints = gl.Constraints{Max: image.Pt(1<<20, 1<<24)}
	m := op.Record(gtx.Ops)
	t := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	dl := widget.Label{LineHeightScale: 1.4}.Layout(lg, th.Shaper, th.MonoFont, d.size(th)*0.875, Detab(s), colorCall(gtx, th.Fg))
	t.Pop()
	call := m.Stop()
	h := dl.Size.Y + 2*pad
	rr := clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
	extra := d.sideways(gtx, th, i, w, h, dl.Size.X+2*pad, call)
	return gl.Dimensions{Size: image.Pt(w, h+extra)}
}

// sideways draws content, cw wide and h high, in a strip w wide at the
// origin. Wider than w, it scrolls by a sideways swipe, a sideways wheel
// or the bar it draws under it, and sideways returns the bar's height.
func (d *Doc) sideways(gtx gl.Context, th *theme.Theme, key, w, h, cw int, content op.CallOp) int {
	over := cw - w
	if over <= 0 {
		content.Add(gtx.Ops)
		return 0
	}
	if d.wide == nil {
		d.wide = map[int]*sideways{}
	}
	s := d.wide[key]
	if s == nil {
		s = &sideways{}
		d.wide[key] = s
	}
	s.x = min(max(s.x, 0), over)
	dx := s.g.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Horizontal, pointer.ScrollRange{Min: -s.x, Max: over - s.x}, pointer.ScrollRange{})
	s.x = min(max(s.x+dx, 0), over)
	bh, in := gtx.Dp(10), gtx.Dp(3)
	thumb := max(gtx.Dp(32), w*w/cw)
	free := max(w-thumb, 1)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.bar, Kinds: pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			s.x = min(max((int(e.Position.X)-thumb/2)*over/free, 0), over)
		}
	}
	cl := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	s.g.Add(gtx.Ops)
	t := op.Offset(image.Pt(-s.x, 0)).Push(gtx.Ops)
	content.Add(gtx.Ops)
	t.Pop()
	cl.Pop()
	x := free * s.x / over
	r := image.Rect(x, h+in, x+thumb, h+bh-in)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Fg, 0.22), clip.UniformRRect(r, r.Dy()/2).Op(gtx.Ops))
	bar := clip.Rect{Min: image.Pt(0, h), Max: image.Pt(w, h+bh)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.bar)
	pointer.CursorDefault.Add(gtx.Ops)
	bar.Pop()
	return bh
}

// minColumn is the narrowest a table's column gets before the table
// scrolls sideways: about a dozen characters.
//
// ponytail: one width for every column; a column's longest word would
// keep a path from breaking, at the cost of measuring each word.
const minColumn unit.Dp = 112

func (d *Doc) table(gtx gl.Context, th *theme.Theme, i int, b *Block) gl.Dimensions {
	w := gtx.Constraints.Max.X
	cols := 0
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return gl.Dimensions{}
	}
	pad := gtx.Dp(theme.SpaceS)
	size := d.size(th) * 0.9375
	natural := d.natural[i]
	if len(natural) != cols {
		// Natural widths, measured without wrapping, once per theme and scale.
		natural = make([]int, cols)
		mgtx := gtx.Disabled()
		for ri, r := range b.Rows {
			for c, cell := range r {
				d.cellOps.Reset()
				mgtx.Ops = &d.cellOps
				mgtx.Constraints = gl.Constraints{Max: image.Pt(1<<20, 1<<20)}
				dm := richtext.TextStyle{State: &d.cellTxt, Styles: d.rich(th, cell, size, th.Fg, ri == 0), Shaper: th.Shaper}.Layout(mgtx)
				natural[c] = max(natural[c], dm.Size.X+2*pad+1)
			}
		}
		if d.natural == nil {
			d.natural = map[int][]int{}
		}
		d.natural[i] = natural
	}
	widths := fitColumns(natural, w, gtx.Dp(minColumn))
	tw := 0
	for _, cw := range widths {
		tw += cw
	}
	m := op.Record(gtx.Ops)
	y := 0
	line := th.BorderSubtle
	calls := make([]op.CallOp, cols)
	for ri, r := range b.Rows {
		h := 0
		for c := range cols {
			var cell []Span
			if c < len(r) {
				cell = r[c]
			}
			cg := gtx
			cg.Constraints = gl.Constraints{Min: image.Pt(widths[c]-2*pad, 0), Max: image.Pt(widths[c]-2*pad, 1<<24)}
			al := text.Start
			if c < len(b.Align) {
				al = map[byte]text.Alignment{'c': text.Middle, 'r': text.End}[b.Align[c]]
			}
			cm := op.Record(gtx.Ops)
			key := i*1024 + 1 + ri*cols + c
			st := d.state(key)
			d.clicked(gtx, st)
			dm := richtext.TextStyle{State: st, Styles: d.styled(key, th, cell, size, th.Fg, ri == 0), Shaper: th.Shaper, Alignment: al, LineHeightScale: 1.2}.Layout(cg)
			calls[c] = cm.Stop()
			h = max(h, dm.Size.Y)
		}
		rh := h + 2*pad
		if ri == 0 {
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(tw, y+rh)}.Op())
		}
		x := 0
		for c := range cols {
			t := op.Offset(image.Pt(x+pad, y+pad)).Push(gtx.Ops)
			calls[c].Add(gtx.Ops)
			t.Pop()
			x += widths[c]
		}
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(0, y+rh), Max: image.Pt(tw, y+rh+1)}.Op())
		y += rh + 1
	}
	// The outline and the column lines.
	paint.FillShape(gtx.Ops, line, clip.Stroke{Path: clip.Rect{Max: image.Pt(tw, y)}.Path(), Width: 1}.Op())
	x := 0
	for c := 0; c < cols-1; c++ {
		x += widths[c]
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(x, 0), Max: image.Pt(x+1, y)}.Op())
	}
	call := m.Stop()
	extra := d.sideways(gtx, th, i, w, y+1, tw+1, call)
	return gl.Dimensions{Size: image.Pt(w, y+1+extra)}
}

// fitColumns shares w between columns of the natural widths: each gets
// its natural width when all fit, else the narrow ones keep theirs and
// the rest share what is left, never under least.
func fitColumns(natural []int, w, least int) []int {
	out := make([]int, len(natural))
	sum := 0
	for _, n := range natural {
		sum += n
	}
	if sum <= w {
		copy(out, natural)
		return out
	}
	left, open := w, len(natural)
	fixed := make([]bool, len(natural))
	for changed := true; changed && open > 0; {
		changed = false
		share := left / open
		for c, n := range natural {
			if !fixed[c] && n <= share {
				out[c], fixed[c] = n, true
				left -= n
				open--
				changed = true
			}
		}
	}
	for c := range natural {
		if !fixed[c] {
			out[c] = max(left/max(open, 1), min(least, natural[c]))
		}
	}
	return out
}

// imgName is what an image is called in a line in its place.
func imgName(im Img) string {
	if im.Alt != "" {
		return im.Alt
	}
	return filepath.Base(im.Src)
}

func (d *Doc) images(gtx gl.Context, th *theme.Theme, i int, b *Block) gl.Dimensions {
	w := gtx.Constraints.Max.X
	sp := gtx.Dp(theme.SpaceS)
	x, y, rowH := 0, 0, 0
	for k, im := range b.Imgs {
		pic := Pic{Note: "Image " + imgName(im) + " not shown"}
		if d.Picture != nil {
			pic = d.Picture(im, w)
		}
		var sz image.Point
		var draw func(at image.Point)
		if pic.Size != (image.Point{}) {
			sz = image.Pt(gtx.Dp(unit.Dp(pic.Size.X)), gtx.Dp(unit.Dp(pic.Size.Y)))
			if sz.X > w {
				sz = image.Pt(w, sz.Y*w/sz.X)
			}
			draw = func(at image.Point) {
				t := op.Offset(at).Push(gtx.Ops)
				cl := clip.Rect{Max: sz}.Push(gtx.Ops)
				s := float32(sz.X) / float32(pic.Size.X)
				a := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(s, s))).Push(gtx.Ops)
				pic.Img.Add(gtx.Ops)
				paint.PaintOp{}.Add(gtx.Ops)
				a.Pop()
				cl.Pop()
				t.Pop()
			}
		} else {
			lg := gtx
			lg.Constraints = gl.Constraints{Max: image.Pt(w, 1<<24)}
			m := op.Record(gtx.Ops)
			dl := widget.Label{}.Layout(lg, th.Shaper, th.UIFont, th.Sp(theme.Body), pic.Note, colorCall(gtx, th.TextQuiet))
			call := m.Stop()
			sz = dl.Size
			draw = func(at image.Point) {
				t := op.Offset(at).Push(gtx.Ops)
				call.Add(gtx.Ops)
				t.Pop()
			}
		}
		if x > 0 && x+sz.X > w {
			x, y, rowH = 0, y+rowH+sp, 0
		}
		at := image.Pt(x, y)
		draw(at)
		if im.Link != "" {
			c := d.clicks[i*1024+k]
			if c == nil {
				if d.clicks == nil {
					d.clicks = map[int]*widget.Clickable{}
				}
				c = &widget.Clickable{}
				d.clicks[i*1024+k] = c
			}
			for c.Clicked(gtx) {
				if d.Follow != nil {
					d.Follow(im.Link)
				}
			}
			t := op.Offset(at).Push(gtx.Ops)
			cg := gtx
			cg.Constraints = gl.Exact(sz)
			c.Layout(cg, func(gtx gl.Context) gl.Dimensions {
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: sz}
			})
			t.Pop()
		}
		x += sz.X + sp
		rowH = max(rowH, sz.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// graph draws a flowchart on a panel, scaled down to the width.
func (d *Doc) graph(gtx gl.Context, th *theme.Theme, g *Graph) gl.Dimensions {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceL)
	fs := th.Sp(theme.Body)
	panel := th.SurfaceSecondary
	fill := theme.Mix(panel, th.Primary, 0.14)
	ring := theme.Mix(panel, th.Primary, 0.65)
	edge := th.Muted

	lgtx := gtx
	lgtx.Constraints = gl.Constraints{Max: image.Pt(gtx.Dp(180), 1<<20)}
	n := len(g.Nodes)
	calls := make([]op.CallOp, n)
	texts := make([]image.Point, n)
	sizes := make([]image.Point, n)
	px, py := gtx.Dp(14), gtx.Dp(8)
	for k, nd := range g.Nodes {
		m := op.Record(gtx.Ops)
		dl := widget.Label{Alignment: text.Middle}.Layout(lgtx, th.Shaper, th.UIFont, fs, nd.Label, colorCall(gtx, th.Fg))
		calls[k], texts[k] = m.Stop(), dl.Size
		s := dl.Size.Add(image.Pt(2*px, 2*py))
		switch nd.Shape {
		case Diamond:
			s = image.Pt(dl.Size.X+2*dl.Size.Y+2*px, 5*dl.Size.Y/2+2*py)
		case Circle:
			side := max(s.X, s.Y)
			s = image.Pt(side, side)
		case Stadium:
			s.X += s.Y / 2
		}
		sizes[k] = s
	}
	elabels := make([]op.CallOp, len(g.Edges))
	esizes := make([]image.Point, len(g.Edges))
	widest := 0
	for k, e := range g.Edges {
		if e.Label == "" {
			continue
		}
		m := op.Record(gtx.Ops)
		dl := widget.Label{Alignment: text.Middle}.Layout(lgtx, th.Shaper, th.UIFont, th.Sp(theme.Small), e.Label, colorCall(gtx, th.Muted))
		elabels[k], esizes[k] = m.Stop(), dl.Size
		widest = max(widest, dl.Size.X)
	}
	rank := gtx.Dp(44)
	if widest > 0 {
		if g.Dir == "LR" || g.Dir == "RL" {
			rank = max(rank, widest+gtx.Dp(32))
		} else {
			rank = max(rank, 3*gtx.Sp(th.Sp(theme.Small))+gtx.Dp(24))
		}
	}
	pl := g.Layout(sizes, gtx.Dp(28), rank)
	scale := min(float32(1), float32(w-2*pad)/float32(max(pl.Size.X, 1)))
	dw, dh := int(float32(pl.Size.X)*scale), int(float32(pl.Size.Y)*scale)
	r := image.Rect(0, 0, w, dh+2*pad)
	paint.FillShape(gtx.Ops, panel, clip.UniformRRect(r, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	cl := clip.Rect(r).Push(gtx.Ops)
	defer cl.Pop()
	a := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(f32.Pt(float32((w-dw)/2), float32(pad)))).Push(gtx.Ops)
	defer a.Pop()

	stroke := float32(gtx.Dp(1.5))
	head := float32(gtx.Dp(8))
	for k, e := range g.Edges {
		pts := pl.Lines[k]
		if len(pts) < 2 {
			continue
		}
		fp := make([]f32.Point, len(pts))
		for j, p := range pts {
			fp[j] = f32.Pt(float32(p.X), float32(p.Y))
		}
		width := stroke
		if e.Thick {
			width *= 2
		}
		if e.Head {
			tip, from := fp[len(fp)-1], fp[len(fp)-2]
			dir := unitVec(tip.Sub(from))
			fp[len(fp)-1] = tip.Sub(dir.Mul(head * 0.8))
			arrowHead(gtx, edge, tip, dir, head)
		}
		polyline(gtx, edge, fp, width, e.Dotted)
	}
	for k, nd := range g.Nodes {
		nr := pl.Nodes[k]
		shape(gtx, nd.Shape, nr, fill, ring, stroke)
		c := nr.Min.Add(nr.Size().Sub(texts[k]).Div(2))
		t := op.Offset(c).Push(gtx.Ops)
		calls[k].Add(gtx.Ops)
		t.Pop()
	}
	for k := range g.Edges {
		if esizes[k] == (image.Point{}) || len(pl.Lines[k]) < 2 {
			continue
		}
		pts := pl.Lines[k]
		m := len(pts) / 2
		mid := pts[m-1].Add(pts[m]).Div(2)
		if len(pts)%2 == 1 {
			mid = pts[m]
		}
		lr := image.Rectangle{Min: mid.Sub(esizes[k].Div(2)), Max: mid.Sub(esizes[k].Div(2)).Add(esizes[k])}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(lr.Inset(-gtx.Dp(3)), gtx.Dp(3)).Op(gtx.Ops))
		t := op.Offset(lr.Min).Push(gtx.Ops)
		elabels[k].Add(gtx.Ops)
		t.Pop()
	}
	return gl.Dimensions{Size: r.Max}
}

func unitVec(p f32.Point) f32.Point {
	l := float32(math.Hypot(float64(p.X), float64(p.Y)))
	if l == 0 {
		return f32.Point{}
	}
	return p.Div(l)
}

func polyline(gtx gl.Context, c color.NRGBA, pts []f32.Point, width float32, dotted bool) {
	var p clip.Path
	p.Begin(gtx.Ops)
	if !dotted {
		p.MoveTo(pts[0])
		for _, q := range pts[1:] {
			p.LineTo(q)
		}
	} else {
		dash, space := width*3, width*2.5
		for k := 1; k < len(pts); k++ {
			a, b := pts[k-1], pts[k]
			dv := unitVec(b.Sub(a))
			l := float32(math.Hypot(float64(b.X-a.X), float64(b.Y-a.Y)))
			for s := float32(0); s < l; s += dash + space {
				p.MoveTo(a.Add(dv.Mul(s)))
				p.LineTo(a.Add(dv.Mul(min(s+dash, l))))
			}
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func arrowHead(gtx gl.Context, c color.NRGBA, tip, dir f32.Point, size float32) {
	n := f32.Pt(-dir.Y, dir.X)
	base := tip.Sub(dir.Mul(size))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(tip)
	p.LineTo(base.Add(n.Mul(size / 2)))
	p.LineTo(base.Sub(n.Mul(size / 2)))
	p.Close()
	paint.FillShape(gtx.Ops, c, clip.Outline{Path: p.End()}.Op())
}

func shape(gtx gl.Context, s Shape, r image.Rectangle, fill, ring color.NRGBA, width float32) {
	var path clip.PathSpec
	switch s {
	case Diamond:
		c := r.Min.Add(r.Max).Div(2)
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(float32(c.X), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Max.X), float32(c.Y)))
		p.LineTo(f32.Pt(float32(c.X), float32(r.Max.Y)))
		p.LineTo(f32.Pt(float32(r.Min.X), float32(c.Y)))
		p.Close()
		path = p.End()
	case Circle:
		path = clip.Ellipse(r).Path(gtx.Ops)
	default:
		rad := gtx.Dp(4)
		switch s {
		case Round:
			rad = gtx.Dp(12)
		case Stadium:
			rad = r.Dy() / 2
		}
		path = clip.UniformRRect(r, rad).Path(gtx.Ops)
	}
	paint.FillShape(gtx.Ops, fill, clip.Outline{Path: path}.Op())
	paint.FillShape(gtx.Ops, ring, clip.Stroke{Path: path, Width: width}.Op())
}
