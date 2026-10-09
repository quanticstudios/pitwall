// Package term draws a vt.Grid with the monospace font and turns key, text,
// paste and mouse input on the pane into PTY bytes.
package term

import (
	"hash/maphash"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const (
	// padding is the gap between the pane edge and the grid, aide's p-3
	// around its xterm.
	padding = unit.Dp(12)
	// blink is the cursor blink half-period of xterm.js and Ghostty.
	blink = 530 * time.Millisecond
)

// View renders one pane. The zero value is ready to use; keep one View per
// pane so its row cache and selection survive between frames.
type View struct {
	// Keys are the bindings: the view runs copy, paste and the scroll keys
	// itself and leaves the window's Alt chords alone. Nil is the default
	// preset.
	Keys *config.Bindings
	// CopyOnSelect copies a mouse selection to the clipboard once it is
	// made: when a drag is released, or a word is double-clicked.
	CopyOnSelect bool
	// Links underlines links and opens the one under a Ctrl+click, even
	// while the program owns the mouse.
	Links bool

	th       *theme.Theme
	ppem     fixed.Int26_6
	cell     image.Point
	baseline int // y of the baseline inside a cell
	line     int // decoration thickness in pixels
	pad      int // padding in pixels
	families []string
	shaper   shaping.HarfbuzzShaper
	raster   vector.Rasterizer
	glyphs   map[glyphKey]*glyphImg

	// Rows are rasterized once per distinct content and replayed by hash,
	// so unchanged (or scrolled) rows cost one map lookup per frame.
	rows, prev map[uint64]*rowImg
	free       []*rowImg
	seed       maphash.Seed
	jobs       []rowJob
	cur        struct {
		key glyphKey
		img paint.ImageOp
	}

	sel       vt.Selection // the selection, while selOn
	selOn     bool
	selCols   int  // the grid width sel was made at: a rewrap renumbers lines
	dragging  bool // a mouse selection is being dragged
	dragAt    f32.Point
	auto      int // rows the drag is above (< 0) or below the view, which scrolls it
	autoAt    time.Time
	autoAcc   float64 // autoscroll not yet a whole line
	clicks    int     // presses in a row on one cell: 2 selects a word, 3 a line
	lastPress pointer.Event
	lastCell  image.Point
	buttons   pointer.Buttons // held as of the last pointer event
	selDone   bool            // a selection was finished this frame
	copied    *Copy           // what this frame asks to copy
	queued    string          // the pane action Run asked for
	cm        copyMode
	wantFind  bool // copy mode's / or ? asks for the find bar

	keyText string // text of the key press report-all just encoded
	eatText string // text of the key press that just left copy mode

	inside    bool        // the pointer is over the pane
	ptr       image.Point // the cell under the pointer
	hover     link        // the link drawn hovered, while hoverOn
	hoverY    int
	hoverOn   bool
	linkPress bool   // a Ctrl+click opened a link; its drag and release go nowhere
	open      string // the link this frame's Ctrl+click asks to open
	links     []link // scratch for rowLinks
	hovLinks  []link // the hovered links on the row being drawn
	linkBuf   []byte

	scrollPx          float32 // wheel distance not yet a whole line
	scrollLines       int
	prompts           int // prompt jumps asked for, > 0 back
	scrollOff, scrMax int
	pushed            uint64 // proto.Frame's ScrollPushed
	top               uint64 // the line the frame's top row shows

	find    *vt.Finder  // highlights its matches; nil for none
	findQ   string      // find's query
	findCur image.Point // the current match's first cell; Y -1 for none
	found   []vt.Match  // the matches on the row being drawn

	blinkAt    time.Time
	wasFocused bool

	keyFocus  bool // Gio key focus with the window focused
	focusIn   bool // the focus state the program was last told
	focusMode bool // the program had mode 1004 on in the last frame

	filters    []event.Filter
	filtersFor *config.Bindings // the Keys filters was built for
}

type rowImg struct {
	img *image.RGBA
	op  paint.ImageOp
}

// rowJob is a row to rasterize this frame. Glyphs are looked up on the UI
// goroutine first, so the rows can be painted in parallel.
type rowJob struct {
	r     *rowImg
	cells []vt.Cell
	st    []style
	gl    []*glyphImg
}

// style is a cell after color, reverse, faint and selection are resolved.
type style struct {
	fg, bg color.NRGBA
	font   uint8   // bit 0 bold, bit 1 italic
	deco   vt.Attr // Underline|Strike
	link   bool    // part of a link at rest: a faint underline
	blank  bool    // no glyph to draw
}

// Layout draws g filling gtx.Constraints.Max. It returns the input bytes for
// the PTY from this frame, and the cols/rows that fit, which the caller sends
// as a Resize when they differ from g.
func (v *View) Layout(gtx layout.Context, th *theme.Theme, g *vt.Grid, m vt.Modes, focused bool) (dims layout.Dimensions, input []byte, cols, rows int) {
	v.metrics(gtx, th)
	size := gtx.Constraints.Max
	v.pad = gtx.Dp(padding)
	cols, rows = fit(size.Sub(image.Pt(2*v.pad, 2*v.pad)), v.cell)

	v.dropStale(g)
	input = v.events(gtx, g, m, focused, rows)
	v.keepSelection(gtx, g)
	v.hoverOn = false
	if v.Links && ctrlDown && v.inside {
		if l, ok := v.linkAt(g, v.ptr); ok {
			v.hover, v.hoverY, v.hoverOn = l, v.ptr.Y, true
		}
	}

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.TermBg, clip.Rect{Max: size}.Op())

	n, h := min(g.Cols, cols), min(g.Rows, rows)
	v.drawRows(gtx.Ops, g, n, h)
	if v.cm.on {
		v.copyCursor(gtx.Ops, g, n, h)
	}

	// The cursor blinks only while focused, restarting on input or focus,
	// and an unfocused pane schedules no frames for it.
	if len(input) > 0 || focused && !v.wasFocused {
		v.blinkAt = gtx.Now
	}
	v.wasFocused = focused
	on := true
	if c := g.Cursor; focused && c.Visible {
		el := gtx.Now.Sub(v.blinkAt)
		on = el/blink%2 == 0
		gtx.Execute(op.InvalidateCmd{At: v.blinkAt.Add((el/blink + 1) * blink)})
	}
	if c := g.Cursor; c.Visible && on && !v.cm.on && c.X >= 0 && c.X < n && c.Y >= 0 && c.Y < h {
		v.cursor(gtx.Ops, g, c, focused)
	}
	v.scrollbar(gtx, size, rows)

	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, v)
	if v.hoverOn {
		pointer.CursorPointer.Add(gtx.Ops)
	} else if m.Mouse == vt.MouseOff {
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

// SetScroll tells the view where the frame it is about to draw sits in the
// scrollback (proto.Frame's ScrollOffset, ScrollMax and ScrollPushed), which
// keeps a selection on its text as output arrives. Call it before Layout;
// the view draws a scrollbar while offset > 0.
func (v *View) SetScroll(offset, max int, pushed uint64) {
	v.scrollOff, v.scrMax, v.pushed = offset, max, pushed
	v.top = pushed - uint64(offset)
}

// SetFind highlights query's matches in the grid Layout draws next, and the
// one starting at cell cur as the current match (cur.Y -1 for none), as
// the find bar does. An empty query turns it off. Call it before Layout.
func (v *View) SetFind(query string, cur image.Point) {
	if query != v.findQ {
		v.findQ, v.find = query, nil
		if query != "" {
			v.find = vt.NewFinder(query)
		}
	}
	if v.cm.on && cur.Y >= 0 && cur != v.findCur {
		v.cm.cur = vt.Pos{Line: v.top + uint64(cur.Y), Col: cur.X}
	}
	v.findCur = cur
}

// ScrollDelta returns and clears the wheel scrolling gathered by Layout while
// the program has not asked for the mouse, in lines (> 0 is back in history).
// The caller sends it as a proto.Scroll.
func (v *View) ScrollDelta() int {
	n := v.scrollLines
	v.scrollLines = 0
	return n
}

// PromptDelta returns and clears the prompt jumps the prompt keys asked
// for since the last call (> 0 is back in history). The caller sends them
// as proto.Scroll's Prompts.
func (v *View) PromptDelta() int {
	n := v.prompts
	v.prompts = 0
	return n
}

// OpenLink returns and clears the link the last Layout's Ctrl+click asked
// to open, or "". Only http, https, file and mailto links get here.
func (v *View) OpenLink() string {
	s := v.open
	v.open = ""
	return s
}

// Copy is a selection to put on the clipboard.
type Copy struct {
	Sel vt.Selection
	// Text is Sel's text as far as the frame shows it, and Whole whether
	// that is all of it. When it is not, the caller asks the daemon for
	// the rest (proto.Text).
	Text  string
	Whole bool
}

// Copied returns and clears the selection the last Layout asked to copy,
// by the copy key, copy mode or CopyOnSelect.
func (v *View) Copied() (Copy, bool) {
	c := v.copied
	v.copied = nil
	if c == nil {
		return Copy{}, false
	}
	return *c, true
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
	v.families = families(string(th.MonoFont.Typeface))
	v.glyphs = map[glyphKey]*glyphImg{}
	v.cur.key = glyphKey{}
	for _, r := range v.rows {
		v.free = append(v.free, r)
	}
	for _, r := range v.prev {
		v.free = append(v.free, r)
	}
	v.rows, v.prev = map[uint64]*rowImg{}, map[uint64]*rowImg{}
	v.seed = maphash.MakeSeed()

	w, h, asc := 1, 1, fixed.Int26_6(0)
	if face := v.face(0, 'M'); face != nil {
		out := v.shape(face, []rune{'M'})
		ext, _ := face.FontHExtents()
		s := float32(ppem) / float32(face.Upem())
		asc = fixed.Int26_6(ext.Ascender * s)
		desc := fixed.Int26_6(-ext.Descender * s)
		lh := th.LineHeight
		if lh == 0 {
			lh = 1
		}
		w, h = max(1, out.Advance.Round()), max(1, int(math.Round(float64(asc+desc)*float64(lh)/64)))
		v.baseline = ((fixed.I(h)-asc-desc)/2 + asc).Round()
	}
	v.cell = image.Pt(w, h)
	v.line = max(1, (ppem / 14).Round())
}

// drawRows paints rows [0,h) of g, n cells wide, reusing cached row images
// and rasterizing the rest in parallel.
func (v *View) drawRows(ops *op.Ops, g *vt.Grid, n, h int) {
	type placed struct {
		y int
		r *rowImg
	}
	if n == 0 {
		return // Gio can't make a zero-width texture
	}
	order := make([]placed, 0, h)
	jobs := v.jobs[:0] // keeps each job's buffers
	shown := v.sel.Expand(vt.GridLines(g, v.top))
	for y := range h {
		cells := g.Cells[y*g.Cols : y*g.Cols+n]
		s0, s1 := -1, -1
		if v.selOn {
			s0, s1 = shown.Cols(v.top+uint64(y), n)
		}
		v.hovLinks = v.hovLinks[:0]
		if v.Links && v.hoverOn {
			v.links, v.linkBuf = rowLinks(v.links[:0], v.linkBuf, cells)
			for _, l := range v.links {
				if v.hovered(l, y) {
					v.hovLinks = append(v.hovLinks, l)
				}
			}
		}
		v.found = v.found[:0]
		if v.find != nil {
			v.found = v.find.Row(v.found, 0, cells)
		}
		cur := -1
		if y == v.findCur.Y {
			cur = v.findCur.X
		}
		k := v.hashRow(cells, s0, s1, v.hovLinks, cur)
		r, ok := v.rows[k]
		if !ok {
			if r, ok = v.prev[k]; ok {
				delete(v.prev, k)
			} else {
				r = v.take(n*v.cell.X, v.cell.Y)
				if len(jobs) < cap(jobs) {
					jobs = jobs[:len(jobs)+1]
				} else {
					jobs = append(jobs, rowJob{})
				}
				j := &jobs[len(jobs)-1]
				j.r, j.cells = r, cells
				v.prepare(j, s0, s1, y, cur)
			}
			v.rows[k] = r
		}
		order = append(order, placed{y, r})
	}
	v.jobs = jobs

	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(jobs), runtime.GOMAXPROCS(0)) {
		wg.Go(func() {
			for i := int(next.Add(1) - 1); i < len(jobs); i = int(next.Add(1) - 1) {
				v.paintRow(&jobs[i])
			}
		})
	}
	wg.Wait()
	for _, j := range jobs {
		j.r.op = paint.NewImageOp(j.r.img)
		j.r.op.Filter = paint.FilterNearest
	}

	for _, p := range order {
		t := op.Offset(image.Pt(v.pad, v.pad+p.y*v.cell.Y)).Push(ops)
		p.r.op.Add(ops)
		paint.PaintOp{}.Add(ops)
		t.Pop()
	}
	// Rows not drawn this frame go back to the pool. Gio drops their
	// textures at the end of this frame, and each reuse gets a new ImageOp.
	for k, r := range v.prev {
		v.free = append(v.free, r)
		delete(v.prev, k)
	}
	v.rows, v.prev = v.prev, v.rows
}

// take returns a w x h row image from the pool or a new one.
func (v *View) take(w, h int) *rowImg {
	for i := len(v.free) - 1; i >= 0; i-- {
		if r := v.free[i]; r.img.Rect.Dx() == w && r.img.Rect.Dy() == h {
			v.free[i] = v.free[len(v.free)-1]
			v.free = v.free[:len(v.free)-1]
			return r
		}
	}
	v.free = v.free[:0] // a resize left the rest the wrong size
	return &rowImg{img: image.NewRGBA(image.Rect(0, 0, w, h))}
}

// prepare resolves the job's styles and glyphs on the UI goroutine. y is
// the row, for the hovered link; v.found are its find matches, and cur
// the column of the current one or -1.
func (v *View) prepare(j *rowJob, s0, s1, y, cur int) {
	j.st = v.resolveStyles(j.st[:0], j.cells, s0, s1)
	if v.Links {
		v.links, v.linkBuf = rowLinks(v.links[:0], v.linkBuf, j.cells)
		for _, l := range v.links {
			hot := v.hovered(l, y)
			for x := l.x0; x < l.x1; x++ {
				if hot {
					j.st[x].fg, j.st[x].deco = v.th.Blue, j.st[x].deco|vt.Underline
				} else {
					j.st[x].link = true
				}
			}
		}
	}
	// Matches show in yellow, the current one solid; a selection shows
	// over them.
	for _, m := range v.found {
		for x := m.Col; x < min(m.Col+m.Cols, len(j.st)); x++ {
			switch {
			case x >= s0 && x < s1:
			case m.Col == cur:
				j.st[x].bg, j.st[x].fg = v.th.Yellow, v.th.TermBg
			default:
				j.st[x].bg = theme.Mix(v.th.TermBg, v.th.Yellow, 0.3)
			}
		}
	}
	j.gl = j.gl[:0]
	for x, c := range j.cells {
		var g *glyphImg
		if _, block := blockRect(c.Content); !j.st[x].blank && !block {
			g = v.glyph(j.st[x].font, c.Content, max(1, int(c.Width)))
		}
		j.gl = append(j.gl, g)
	}
}

// hashRow keys a row image: its cells, selection, whether links show, the
// hovered links on it, and its find matches (v.found) with the current one
// at column cur.
func (v *View) hashRow(cells []vt.Cell, s0, s1 int, hov []link, cur int) uint64 {
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
	if v.Links {
		h.WriteByte(1)
	}
	for _, l := range hov {
		put(0, uint32(l.x0))
		put(4, uint32(l.x1))
		h.Write(b[:8])
	}
	if len(v.found) > 0 {
		h.WriteByte(2) // apart from the hovered links
		put(0, uint32(cur))
		h.Write(b[:4])
	}
	for _, m := range v.found {
		put(0, uint32(m.Col))
		put(4, uint32(m.Cols))
		h.Write(b[:8])
	}
	for _, c := range cells {
		put(0, uint32(c.FG))
		put(4, uint32(c.BG))
		put(8, uint32(c.Attrs)|uint32(c.Width)<<16|uint32(len(c.Content))<<24)
		h.Write(b[:12])
		h.WriteString(c.Content)
		if c.Link != "" {
			h.WriteString(c.Link)
			h.WriteByte(0)
		}
	}
	return h.Sum64()
}

// resolveStyles appends the styles of cells to st. Cells in [s0,s1) are
// selected.
func (v *View) resolveStyles(st []style, cells []vt.Cell, s0, s1 int) []style {
	th := v.th
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
	return st
}

// paintRow rasterizes one row into its image. It runs on worker goroutines
// and only reads View fields that metrics sets.
func (v *View) paintRow(j *rowJob) {
	img, st, cells := j.r.img, j.st, j.cells
	cw, ch := v.cell.X, v.cell.Y

	for x := 0; x < len(st); {
		k := x + 1
		for k < len(st) && st[k].bg == st[x].bg {
			k++
		}
		fillRect(img, image.Rect(x*cw, 0, k*cw, ch), st[x].bg)
		x = k
	}

	// Block elements are rects, so neighbors join without seams.
	for x := 0; x < len(st); {
		b, ok := blockRect(cells[x].Content)
		if !ok || st[x].blank {
			x++
			continue
		}
		k := x + 1
		for k < len(st) && cells[k].Content == cells[x].Content && st[k].fg == st[x].fg {
			k++
		}
		fillRect(img, image.Rect(x*cw+cw*b.Min.X/8, ch*b.Min.Y/8, (k-1)*cw+cw*b.Max.X/8, ch*b.Max.Y/8), st[x].fg)
		x = k
	}

	for x, g := range j.gl {
		if g != nil {
			blit(img, x*cw, 0, g, st[x].fg)
		}
	}

	// A link at rest gets a faint line where an underline goes, in its own
	// color, so it reads as a link over any program's colors.
	uy := min(ch-v.line, v.baseline+v.line)
	for x := 0; x < len(st); {
		if !st[x].link || st[x].deco&vt.Underline != 0 {
			x++
			continue
		}
		k := x + 1
		for k < len(st) && st[k].link && st[k].deco&vt.Underline == 0 && st[k].fg == st[x].fg {
			k++
		}
		c := st[x].fg
		c.A /= 2
		fillRect(img, image.Rect(x*cw, uy, k*cw, uy+v.line), c)
		x = k
	}

	for _, d := range [...]struct {
		a vt.Attr
		y int
	}{
		{vt.Underline, uy},
		{vt.Strike, v.baseline - (v.ppem * 3 / 10).Round()},
	} {
		for x := 0; x < len(st); {
			if st[x].deco&d.a == 0 {
				x++
				continue
			}
			k := x + 1
			for k < len(st) && st[k].deco&d.a != 0 && st[k].fg == st[x].fg {
				k++
			}
			fillRect(img, image.Rect(x*cw, d.y, k*cw, d.y+v.line), st[x].fg)
			x = k
		}
	}
}

func (v *View) cursor(ops *op.Ops, g *vt.Grid, c vt.Cursor, focused bool) {
	cw, ch := v.cell.X, v.cell.Y
	x := c.X
	if x > 0 && g.At(x, c.Y).Width == 0 {
		x-- // on the trailing half of a wide char
	}
	cell := g.At(x, c.Y)
	w := max(1, int(cell.Width))
	r := image.Rect(x*cw, c.Y*ch, (x+w)*cw, (c.Y+1)*ch).Add(image.Pt(v.pad, v.pad))
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
		// The block shows the glyph under it in the background color.
		st := v.resolveStyles(nil, []vt.Cell{cell}, -1, -1)[0]
		k := glyphKey{st.font | uint8(w)<<2, cell.Content}
		if st.blank {
			k.s = ""
		}
		if k != v.cur.key {
			img := image.NewRGBA(image.Rect(0, 0, w*cw, ch))
			fillRect(img, img.Rect, cur)
			if _, block := blockRect(cell.Content); !st.blank && !block {
				blit(img, 0, 0, v.glyph(st.font, cell.Content, w), v.th.TermBg)
			}
			v.cur.key, v.cur.img = k, paint.NewImageOp(img)
			v.cur.img.Filter = paint.FilterNearest
		}
		t := op.Offset(r.Min).Push(ops)
		v.cur.img.Add(ops)
		paint.PaintOp{}.Add(ops)
		t.Pop()
	}
}

// scrollbar draws aide's overlay thumb on the right edge while the view is
// scrolled back: 6dp wide, 2dp from the edge, white at 14%.
func (v *View) scrollbar(gtx layout.Context, size image.Point, rows int) {
	if v.scrollOff <= 0 || v.scrMax <= 0 {
		return
	}
	w, in := gtx.Dp(6), gtx.Dp(2)
	track := size.Y - 2*in
	total := v.scrMax + rows
	th := min(track, max(gtx.Dp(24), track*rows/total))
	top := in + (track-th)*(v.scrMax-min(v.scrollOff, v.scrMax))/v.scrMax
	r := image.Rect(size.X-in-w, top, size.X-in, top+th)
	c := theme.Mix(v.th.TermBg, v.th.TermFg, 0.14)
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, w/2).Op(gtx.Ops))
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

// selected is bg with 16% white over it, aide's xterm selectionBackground,
// or 16% black when bg is light, where white would barely show.
func selected(bg color.NRGBA) color.NRGBA {
	over := 255
	if 299*int(bg.R)+587*int(bg.G)+114*int(bg.B) > 140*1000 {
		over = 0
	}
	mix := func(c uint8) uint8 { return uint8((int(c)*84 + over*16) / 100) }
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
