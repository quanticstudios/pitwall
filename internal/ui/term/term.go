// Package term draws a vt.Grid with the monospace font and turns key, text,
// paste and mouse input on the pane into PTY bytes.
package term

import (
	"hash/maphash"
	"image"
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// View renders one pane. The zero value is ready to use; keep one View per
// pane so its row cache and selection survive between frames.
type View struct {
	th       *theme.Theme
	ppem     fixed.Int26_6
	cell     image.Point
	baseline int // y of the baseline inside a cell
	line     int // decoration thickness in pixels
	fonts    [4]font.Font
	glyphs   map[glyphKey]glyphRun

	// Rows are recorded once per distinct content and replayed by hash, so
	// unchanged (or scrolled) rows cost one map lookup per frame.
	rows, prev map[uint64]*rowOps
	free       []*rowOps
	seed       maphash.Seed

	styles []style
	gbuf   []text.Glyph
	rbuf   [][2]int

	sel       selection
	dragging  bool
	lastPress pointer.Event
	lastCell  image.Point

	filters []event.Filter
}

type glyphKey struct {
	font uint8
	s    string
}

type glyphRun struct {
	gs  []text.Glyph
	adv fixed.Int26_6
}

type rowOps struct {
	ops  op.Ops
	call op.CallOp
}

// style is a cell after color, reverse, faint and selection are resolved.
type style struct {
	fg, bg color.NRGBA
	font   uint8   // bit 0 bold, bit 1 italic
	deco   vt.Attr // Underline|Strike
	blank  bool    // no glyph to draw
}

// Layout draws g filling gtx.Constraints.Max. It returns the input bytes for
// the PTY from this frame, and the cols/rows that fit, which the caller sends
// as a Resize when they differ from g.
func (v *View) Layout(gtx layout.Context, th *theme.Theme, g *vt.Grid, m vt.Modes, focused bool) (dims layout.Dimensions, input []byte, cols, rows int) {
	v.metrics(gtx, th)
	size := gtx.Constraints.Max
	cols, rows = fit(size, v.cell)

	input = v.events(gtx, g, m, focused)

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.TermBg, clip.Rect{Max: size}.Op())

	n, h := min(g.Cols, cols), min(g.Rows, rows)
	for y := range h {
		cells := g.Cells[y*g.Cols : y*g.Cols+n]
		s0, s1 := v.sel.cols(y, n)
		t := op.Offset(image.Pt(0, y*v.cell.Y)).Push(gtx.Ops)
		v.row(cells, s0, s1).Add(gtx.Ops)
		t.Pop()
	}
	v.endFrame()

	if c := g.Cursor; c.Visible && c.X >= 0 && c.X < n && c.Y >= 0 && c.Y < h {
		v.cursor(gtx.Ops, g, c, focused)
	}

	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, v)
	if m.Mouse == vt.MouseOff {
		pointer.CursorText.Add(gtx.Ops)
	}
	area.Pop()

	return layout.Dimensions{Size: size}, input, cols, rows
}

// CellSize is the pixel size of one cell at the current scale.
func (v *View) CellSize(gtx layout.Context, th *theme.Theme) image.Point {
	v.metrics(gtx, th)
	return v.cell
}

// fit is how many whole cells fit in size, at least one each way so a
// collapsed pane never asks the PTY for a zero size.
func fit(size, cell image.Point) (cols, rows int) {
	return max(1, size.X/cell.X), max(1, size.Y/cell.Y)
}

// metrics recomputes the cell size when the theme or scale changes. Cell
// width and height are whole pixels so every glyph and rect lands on the
// pixel grid at any scale.
func (v *View) metrics(gtx layout.Context, th *theme.Theme) {
	ppem := fixed.Int26_6(math.Round(float64(gtx.Metric.PxPerSp) * float64(th.MonoSize) * 64))
	if th == v.th && ppem == v.ppem {
		return
	}
	v.th, v.ppem = th, ppem
	base := th.MonoFont
	if !strings.Contains(string(base.Typeface), "emoji") {
		// The shaper only falls back to color emoji when asked by name.
		base.Typeface += ", emoji"
	}
	for i := range v.fonts {
		f := base
		if i&1 != 0 {
			f.Weight = font.Bold
		}
		if i&2 != 0 {
			f.Style = font.Italic
		}
		v.fonts[i] = f
	}
	v.glyphs = map[glyphKey]glyphRun{}
	for _, r := range v.rows {
		v.free = append(v.free, r)
	}
	for _, r := range v.prev {
		v.free = append(v.free, r)
	}
	v.rows, v.prev = map[uint64]*rowOps{}, map[uint64]*rowOps{}
	v.seed = maphash.MakeSeed()

	m := v.glyph(0, "M")
	var asc, desc fixed.Int26_6
	if len(m.gs) > 0 {
		asc, desc = m.gs[0].Ascent, m.gs[0].Descent
	}
	w, h := max(1, m.adv.Round()), max(1, (asc+desc).Round())
	v.cell = image.Pt(w, h)
	v.baseline = ((fixed.I(h)-asc-desc)/2 + asc).Round()
	v.line = max(1, (ppem / 14).Round())
}

// glyph shapes one grapheme in one of the four mono faces and caches it.
// The shaper falls back to other faces (emoji, CJK) when the mono face
// lacks a glyph.
func (v *View) glyph(f uint8, s string) glyphRun {
	k := glyphKey{f, s}
	if r, ok := v.glyphs[k]; ok {
		return r
	}
	sh := v.th.Shaper
	sh.LayoutString(text.Parameters{Font: v.fonts[f], PxPerEm: v.ppem, MaxWidth: 1 << 20}, s)
	var r glyphRun
	for {
		g, ok := sh.NextGlyph()
		if !ok {
			break
		}
		r.gs = append(r.gs, g)
		r.adv += g.Advance
	}
	v.glyphs[k] = r
	return r
}

// row returns the recorded draw ops for one row, recording on a miss.
func (v *View) row(cells []vt.Cell, s0, s1 int) op.CallOp {
	h := v.hashRow(cells, s0, s1)
	if r, ok := v.rows[h]; ok {
		return r.call
	}
	r, ok := v.prev[h]
	if ok {
		delete(v.prev, h)
	} else {
		if n := len(v.free); n > 0 {
			r, v.free = v.free[n-1], v.free[:n-1]
			r.ops.Reset()
		} else {
			r = new(rowOps)
		}
		m := op.Record(&r.ops)
		v.drawRow(&r.ops, cells, s0, s1)
		r.call = m.Stop()
	}
	v.rows[h] = r
	return r.call
}

// endFrame drops rows that were not drawn this frame.
func (v *View) endFrame() {
	for h, r := range v.prev {
		v.free = append(v.free, r)
		delete(v.prev, h)
	}
	v.rows, v.prev = v.prev, v.rows
}

func (v *View) hashRow(cells []vt.Cell, s0, s1 int) uint64 {
	var h maphash.Hash
	h.SetSeed(v.seed)
	var b [16]byte
	put := func(i int, x uint32) {
		b[i], b[i+1], b[i+2], b[i+3] = byte(x), byte(x>>8), byte(x>>16), byte(x>>24)
	}
	put(0, uint32(len(cells)))
	put(4, uint32(s0))
	put(8, uint32(s1))
	h.Write(b[:12])
	for _, c := range cells {
		put(0, uint32(c.FG))
		put(4, uint32(c.BG))
		put(8, uint32(c.Attrs)|uint32(c.Width)<<16|uint32(len(c.Content))<<24)
		h.Write(b[:12])
		h.WriteString(c.Content)
	}
	return h.Sum64()
}

// resolveStyles fills v.styles for one row. Cells in [s0,s1) are selected.
func (v *View) resolveStyles(cells []vt.Cell, s0, s1 int) []style {
	th := v.th
	st := v.styles[:0]
	for x, c := range cells {
		s := style{fg: resolve(th, c.FG, th.TermFg), bg: resolve(th, c.BG, th.TermBg)}
		if c.Attrs&vt.Reverse != 0 {
			s.fg, s.bg = s.bg, s.fg
		}
		if x >= s0 && x < s1 {
			s.bg = selected(s.bg)
		}
		if c.Attrs&vt.Faint != 0 {
			s.fg.A /= 2
		}
		if c.Attrs&vt.Bold != 0 {
			s.font |= 1
		}
		if c.Attrs&vt.Italic != 0 {
			s.font |= 2
		}
		s.deco = c.Attrs & (vt.Underline | vt.Strike)
		s.blank = c.Width == 0 || c.Content == "" || c.Content == " " || c.Attrs&vt.Invisible != 0
		if c.Attrs&vt.Invisible != 0 {
			s.deco = 0
		}
		st = append(st, s)
	}
	v.styles = st
	return st
}

func (v *View) drawRow(ops *op.Ops, cells []vt.Cell, s0, s1 int) {
	st := v.resolveStyles(cells, s0, s1)
	cw, ch := v.cell.X, v.cell.Y

	for x := 0; x < len(st); {
		j := x + 1
		for j < len(st) && st[j].bg == st[x].bg {
			j++
		}
		if st[x].bg != v.th.TermBg {
			fill(ops, st[x].bg, image.Rect(x*cw, 0, j*cw, ch))
		}
		x = j
	}

	for x := 0; x < len(st); {
		b, ok := blockRect(cells[x].Content)
		if !ok || st[x].blank {
			x++
			continue
		}
		j := x + 1
		for j < len(st) && cells[j].Content == cells[x].Content && st[j].fg == st[x].fg {
			j++
		}
		fill(ops, st[x].fg, image.Rect(x*cw+cw*b.Min.X/8, ch*b.Min.Y/8, (j-1)*cw+cw*b.Max.X/8, ch*b.Max.Y/8))
		for ; x < j; x++ {
			st[x].blank = true
		}
	}

	// Runs of the same style anywhere in the row share one path: syntax
	// highlighting reuses a few colors, and Gio's cost is per path.
	runs := textRuns(st)
	for i, r := range runs {
		if r[0] < 0 {
			continue
		}
		group := append(v.rbuf[:0], r)
		for j := i + 1; j < len(runs); j++ {
			if q := runs[j]; q[0] >= 0 && st[q[0]].fg == st[r[0]].fg && st[q[0]].font == st[r[0]].font {
				group = append(group, q)
				runs[j][0] = -1
			}
		}
		v.rbuf = group
		v.paintRuns(ops, cells, st, group, st[r[0]].fg)
	}

	for _, d := range [...]struct {
		a vt.Attr
		y int
	}{
		{vt.Underline, min(ch-v.line, v.baseline+v.line)},
		{vt.Strike, v.baseline - (v.ppem * 3 / 10).Round()},
	} {
		for x := 0; x < len(st); {
			if st[x].deco&d.a == 0 {
				x++
				continue
			}
			j := x + 1
			for j < len(st) && st[j].deco&d.a != 0 && st[j].fg == st[x].fg {
				j++
			}
			fill(ops, st[x].fg, image.Rect(x*cw, d.y, j*cw, d.y+v.line))
			x = j
		}
	}
}

// textRuns splits a row into maximal runs of non-blank cells sharing color
// and face; each run is shaped and painted once. Blank cells never break a
// run since they draw nothing.
func textRuns(st []style) [][2]int {
	var runs [][2]int
	start, last := -1, -1
	for x, s := range st {
		if s.blank {
			continue
		}
		if start >= 0 && (s.fg != st[start].fg || s.font != st[start].font) {
			runs = append(runs, [2]int{start, last + 1})
			start = -1
		}
		if start < 0 {
			start = x
		}
		last = x
	}
	if start >= 0 {
		runs = append(runs, [2]int{start, last + 1})
	}
	return runs
}

// paintRuns draws the glyphs of the cell ranges in runs in color c. Each glyph is placed
// at its cell, centered in its one or two cells and snapped to whole pixels,
// so fallback glyphs of other widths stay on the grid.
func (v *View) paintRuns(ops *op.Ops, cells []vt.Cell, st []style, runs [][2]int, c color.NRGBA) {
	cw := v.cell.X
	gs := v.gbuf[:0]
	for _, run := range runs {
		for x := run[0]; x < run[1]; x++ {
			if st[x].blank {
				continue
			}
			r := v.glyph(st[x].font, cells[x].Content)
			w := max(1, int(cells[x].Width))
			base := fixed.I(x*cw + ((fixed.I(w*cw) - r.adv) / 2).Round())
			for _, g := range r.gs {
				g.X += base
				gs = append(gs, g)
			}
		}
	}
	v.gbuf = gs
	if len(gs) == 0 {
		return
	}
	sh := v.th.Shaper
	t := op.Offset(image.Pt(gs[0].X.Round(), v.baseline)).Push(ops)
	cl := clip.Outline{Path: sh.Shape(gs)}.Op().Push(ops)
	paint.ColorOp{Color: c}.Add(ops)
	paint.PaintOp{}.Add(ops)
	cl.Pop()
	if call := sh.Bitmaps(gs); call != (op.CallOp{}) {
		call.Add(ops)
	}
	t.Pop()
}

func (v *View) cursor(ops *op.Ops, g *vt.Grid, c vt.Cursor, focused bool) {
	cw, ch := v.cell.X, v.cell.Y
	x := c.X
	if x > 0 && g.At(x, c.Y).Width == 0 {
		x-- // on the trailing half of a wide char
	}
	cell := g.At(x, c.Y)
	w := max(1, int(cell.Width))
	r := image.Rect(x*cw, c.Y*ch, (x+w)*cw, (c.Y+1)*ch)
	cur, t := v.th.TermCur, v.line
	switch {
	case !focused:
		fill(ops, cur, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+t))
		fill(ops, cur, image.Rect(r.Min.X, r.Max.Y-t, r.Max.X, r.Max.Y))
		fill(ops, cur, image.Rect(r.Min.X, r.Min.Y, r.Min.X+t, r.Max.Y))
		fill(ops, cur, image.Rect(r.Max.X-t, r.Min.Y, r.Max.X, r.Max.Y))
	case c.Shape == vt.CursorUnderline:
		fill(ops, cur, image.Rect(r.Min.X, r.Max.Y-2*t, r.Max.X, r.Max.Y))
	case c.Shape == vt.CursorBar:
		fill(ops, cur, image.Rect(r.Min.X, r.Min.Y, r.Min.X+2*t, r.Max.Y))
	default:
		fill(ops, cur, r)
		cells := g.Cells[c.Y*g.Cols : c.Y*g.Cols+x+1]
		st := v.resolveStyles(cells, -1, -1)
		if !st[x].blank {
			off := op.Offset(image.Pt(0, c.Y*ch)).Push(ops)
			v.paintRuns(ops, cells, st, [][2]int{{x, x + 1}}, v.th.TermBg)
			off.Pop()
		}
	}
}

// blockRect is the filled part of a block element in eighths of a cell.
// Drawing these as rects instead of glyphs lets neighbors join without
// seams, which bar charts and half-block images rely on.
func blockRect(s string) (image.Rectangle, bool) {
	r, n := utf8.DecodeRuneInString(s)
	if n != len(s) || r < 0x2580 || r > 0x2595 {
		return image.Rectangle{}, false
	}
	switch {
	case r == 0x2580:
		return image.Rect(0, 0, 8, 4), true
	case r <= 0x2588:
		return image.Rect(0, 8-int(r-0x2580), 8, 8), true
	case r <= 0x258f:
		return image.Rect(0, 0, 8-int(r-0x2588), 8), true
	case r == 0x2590:
		return image.Rect(4, 0, 8, 8), true
	case r == 0x2594:
		return image.Rect(0, 0, 8, 1), true
	case r == 0x2595:
		return image.Rect(7, 0, 8, 8), true
	}
	return image.Rectangle{}, false // shades and quadrants stay glyphs
}

// selected lightens bg by 16% white, aide's xterm selectionBackground.
func selected(bg color.NRGBA) color.NRGBA {
	mix := func(c uint8) uint8 { return uint8((int(c)*84 + 255*16) / 100) }
	return color.NRGBA{R: mix(bg.R), G: mix(bg.G), B: mix(bg.B), A: 0xff}
}

func fill(ops *op.Ops, c color.NRGBA, r image.Rectangle) {
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

// resolve maps a vt.Color to RGB: 0 is def, palette 0-15 comes from the
// theme, 16-255 is the xterm 6x6x6 cube and gray ramp.
func resolve(th *theme.Theme, c vt.Color, def color.NRGBA) color.NRGBA {
	switch c &^ 0xffffff {
	case vt.PaletteFlag:
		return palette(th, uint8(c))
	case vt.RGBFlag:
		return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
	}
	return def
}

func palette(th *theme.Theme, i uint8) color.NRGBA {
	switch {
	case i < 16:
		return th.ANSI[i]
	case i < 232:
		i -= 16
		lvl := func(n uint8) uint8 {
			if n == 0 {
				return 0
			}
			return 55 + 40*n
		}
		return color.NRGBA{R: lvl(i / 36), G: lvl(i / 6 % 6), B: lvl(i % 6), A: 0xff}
	default:
		g := 8 + 10*(i-232)
		return color.NRGBA{R: g, G: g, B: g, A: 0xff}
	}
}

// ScrollDelta returns and clears the wheel scrolling gathered by Layout while
// the program has not asked for the mouse, in lines (> 0 is back in history).
// The caller sends it as a proto.Scroll.
func (v *View) ScrollDelta() int { return 0 }
