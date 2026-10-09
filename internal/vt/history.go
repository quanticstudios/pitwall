package vt

import (
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	xvt "github.com/charmbracelet/x/vt"
)

// historyMax is how many lines that scrolled off the main screen a pane
// keeps unless SetScrollback says otherwise.
const historyMax = 10000

// line is one row of history. x/vt keeps a full uv.Cell (112 bytes) per
// column, about 134MB per pane at 10k lines of 120 columns; this keeps the
// text and the style runs.
type line struct {
	text  string        // cell contents, concatenated
	cells []uint16      // per cell len(Content)<<2 | Width; nil when every cell is one ASCII byte of width 1
	runs  []run         // style and link changes in column order; the first starts at column 0
	flags xvt.LineFlags // the row's OSC 133 marks, and LineWrapped
}

// wrapped reports whether l's text goes on in the next line (a soft wrap),
// rather than ending in a line break.
func (l *line) wrapped() bool { return l.flags&xvt.LineWrapped != 0 }

type run struct {
	col    uint32 // a logical line joined for a reflow can pass 65,535 columns
	attrs  Attr
	fg, bg Color
	link   string
}

func (r run) withCol(c uint32) run { r.col = c; return r }

// history is a ring of lines; once full, head is the oldest.
type history struct {
	lines  []line
	head   int
	max    int    // lines kept; 0 means historyMax
	pushed uint64 // lines ever pushed; clear does not reset it
	runs   []run  // scratch for push
}

func (h *history) len() int { return len(h.lines) }

// limit is how many lines h keeps.
func (h *history) limit() int {
	if h.max > 0 {
		return h.max
	}
	return historyMax
}

// at returns line i, 0 being the oldest.
func (h *history) at(i int) *line { return &h.lines[(h.head+i)%len(h.lines)] }

func (h *history) clear() { h.lines, h.head = nil, 0 }

// set replaces the history with lines, oldest first, keeping the newest
// limit. pushed moves by the change in length, so the oldest row keeps
// its number and the rows after it, the screen's too, number on from it.
func (h *history) set(lines []line) {
	n := h.len()
	h.lines, h.head = slices.Clone(lines[max(0, len(lines)-h.limit()):]), 0
	h.pushed = h.pushed + uint64(h.len()) - uint64(n)
}

func (h *history) push(cells uv.Line, flags xvt.LineFlags) {
	l := h.line(cells, flags, 0)
	h.pushed++
	if len(h.lines) < h.limit() {
		h.lines = append(h.lines, l)
		return
	}
	h.lines[h.head] = l
	h.head = (h.head + 1) % len(h.lines)
}

// line packs a row of cells. Trailing blanks go, except within the first
// keep cells; a soft-wrapped row keeps its blanks, which are text, and drops
// only the padding left where a wide char did not fit.
func (h *history) line(cells uv.Line, flags xvt.LineFlags, keep int) line {
	wrapped := flags&xvt.LineWrapped != 0
	n := len(cells)
	for ; n > keep; n-- {
		c := &cells[n-1]
		if c.IsZero() && n > 1 && cells[n-2].Width > 1 {
			break // the right half of a wide char
		}
		if !c.IsZero() && (wrapped || !c.Equal(&uv.EmptyCell)) {
			break
		}
	}
	cells = cells[:n]
	l := line{flags: flags}
	for _, c := range cells {
		if c.Width != 1 || len(c.Content) != 1 || c.Content[0] >= 0x80 {
			l.cells = make([]uint16, len(cells))
			break
		}
	}
	var b strings.Builder
	b.Grow(len(cells))
	h.runs = h.runs[:0]
	for i, c := range cells {
		s := c.Content
		if len(s) > 0x3fff { // no real grapheme is 16KB
			s = " "
		}
		b.WriteString(s)
		if l.cells != nil {
			l.cells[i] = uint16(len(s))<<2 | uint16(c.Width&3)
		}
		r := run{col: uint32(i), fg: toColor(c.Style.Fg), bg: toColor(c.Style.Bg),
			attrs: toAttr(c.Style.Attrs, c.Style.Underline != 0), link: c.Link.URL}
		if n := len(h.runs); n == 0 || h.runs[n-1] != r.withCol(h.runs[n-1].col) {
			h.runs = append(h.runs, r)
		}
	}
	l.text = b.String()
	l.runs = slices.Clone(h.runs)
	return l
}

// width is how many columns l holds.
func (l *line) width() int {
	if l.cells != nil {
		return len(l.cells)
	}
	return len(l.text)
}

// fill writes l into dst, one Cell per column: longer lines are cut, shorter
// ones padded with blanks.
func (l *line) fill(dst []Cell) {
	off, ri := 0, 0
	n := l.width()
	for x := range dst {
		if x >= n {
			dst[x] = Cell{Content: " ", Width: 1}
			continue
		}
		size, w := 1, uint8(1)
		if l.cells != nil {
			size, w = int(l.cells[x]>>2), uint8(l.cells[x]&3)
		}
		for ri+1 < len(l.runs) && int(l.runs[ri+1].col) <= x {
			ri++
		}
		r := l.runs[ri]
		dst[x] = Cell{Content: l.text[off : off+size], Width: w, FG: r.fg, BG: r.bg, Attrs: r.attrs, Link: r.link}
		off += size
		if w == 2 && x == len(dst)-1 { // its right half is cut off
			dst[x].Content, dst[x].Width = " ", 1
		}
	}
}
