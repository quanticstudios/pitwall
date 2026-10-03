package vt

import (
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// historyMax is how many lines that scrolled off the main screen a pane keeps.
const historyMax = 10000

// line is one row of history. x/vt keeps a full uv.Cell (112 bytes) per
// column, about 134MB per pane at 10k lines of 120 columns; this keeps the
// text and the style runs.
type line struct {
	text  string   // cell contents, concatenated
	cells []uint16 // per cell len(Content)<<2 | Width; nil when every cell is one ASCII byte of width 1
	runs  []run    // style and link changes in column order; the first starts at column 0
}

type run struct {
	col    uint16
	attrs  Attr
	fg, bg Color
	link   string
}

func (r run) withCol(c uint16) run { r.col = c; return r }

// history is a ring of lines; once full, head is the oldest.
type history struct {
	lines  []line
	head   int
	pushed uint64 // lines ever pushed; clear does not reset it
	runs   []run  // scratch for push
}

func (h *history) len() int { return len(h.lines) }

// at returns line i, 0 being the oldest.
func (h *history) at(i int) *line { return &h.lines[(h.head+i)%len(h.lines)] }

func (h *history) clear() { h.lines, h.head = nil, 0 }

func (h *history) push(cells uv.Line) {
	var l line
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
		r := run{col: uint16(i), fg: toColor(c.Style.Fg), bg: toColor(c.Style.Bg),
			attrs: toAttr(c.Style.Attrs, c.Style.Underline != 0), link: c.Link.URL}
		if n := len(h.runs); n == 0 || h.runs[n-1] != r.withCol(h.runs[n-1].col) {
			h.runs = append(h.runs, r)
		}
	}
	l.text = b.String()
	l.runs = slices.Clone(h.runs)

	h.pushed++
	if len(h.lines) < historyMax {
		h.lines = append(h.lines, l)
		return
	}
	h.lines[h.head] = l
	h.head = (h.head + 1) % historyMax
}

// fill writes l into dst, one Cell per column: longer lines are cut, shorter
// ones padded with blanks.
func (l *line) fill(dst []Cell) {
	off, ri := 0, 0
	n := len(l.text)
	if l.cells != nil {
		n = len(l.cells)
	}
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
