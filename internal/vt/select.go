package vt

import "bytes"

// Pos is a cell of a pane's text. Line numbers lines as Match does, so a
// position stays on its text while output scrolls it into history; Col is
// the cell in the row.
type Pos struct {
	Line uint64
	Col  int
}

// Before reports whether p comes before q in reading order.
func (p Pos) Before(q Pos) bool { return p.Line < q.Line || p.Line == q.Line && p.Col < q.Col }

// SelectMode is how a Selection's two ends pick cells.
type SelectMode uint8

const (
	SelectChars SelectMode = iota // every cell from one end to the other in reading order
	SelectLines                   // the whole logical lines the ends are on
	SelectBlock                   // the rectangle the ends are corners of
)

// Selection runs from anchor A to head B, both included.
type Selection struct {
	A, B Pos
	Mode SelectMode
}

// LineFunc returns line n's cells and whether it soft-wraps into line n+1;
// ok is false when there is no line n. The cells are only good until the
// next call.
type LineFunc func(n uint64) (cells []Cell, wrapped, ok bool)

// GridLines reads g's rows as lines top, top+1 and on: a view whose top
// row shows line top.
func GridLines(g *Grid, top uint64) LineFunc {
	return func(n uint64) ([]Cell, bool, bool) {
		if n < top || n-top >= uint64(g.Rows) {
			return nil, false, false
		}
		y := int(n - top)
		return g.Cells[y*g.Cols : (y+1)*g.Cols], y < len(g.Wrapped) && g.Wrapped[y], true
	}
}

// Ordered returns the ends in reading order.
func (s Selection) Ordered() (Pos, Pos) {
	if s.B.Before(s.A) {
		return s.B, s.A
	}
	return s.A, s.B
}

// Cols is the selected column range [c0,c1) of line in a row n cells wide,
// or (-1,-1) when the line has none. A line selection takes whole rows;
// Expand it first to take in the rest of each logical line.
func (s Selection) Cols(line uint64, n int) (int, int) {
	p, q := s.Ordered()
	if line < p.Line || line > q.Line {
		return -1, -1
	}
	c0, c1 := 0, n
	switch s.Mode {
	case SelectChars:
		if line == p.Line {
			c0 = p.Col
		}
		if line == q.Line {
			c1 = q.Col + 1
		}
	case SelectBlock:
		c0, c1 = min(s.A.Col, s.B.Col), max(s.A.Col, s.B.Col)+1
	}
	return min(c0, n), min(c1, n)
}

// Expand widens a line selection to the logical lines its ends are on, as
// far as lines reaches. Other selections come back as they are.
func (s Selection) Expand(lines LineFunc) Selection {
	if s.Mode != SelectLines {
		return s
	}
	p, q := s.Ordered()
	for p.Line > 0 {
		if _, w, ok := lines(p.Line - 1); !ok || !w {
			break
		}
		p.Line--
	}
	for {
		if _, w, ok := lines(q.Line); !ok || !w {
			break
		}
		if _, _, ok := lines(q.Line + 1); !ok {
			break
		}
		q.Line++
	}
	return Selection{A: p, B: q, Mode: SelectLines}
}

// Text is the selected text. Rows joined by a soft wrap stay one line, a
// block gives one line per row, and blanks before each line break go.
// Lines that lines lacks are left out.
func (s Selection) Text(lines LineFunc) string {
	s = s.Expand(lines)
	p, q := s.Ordered()
	var out []byte
	brk := false // the line before ended in a line break
	for n := p.Line; ; n++ {
		if cells, wrapped, ok := lines(n); ok {
			if brk {
				out = append(bytes.TrimRight(out, " "), '\n')
			}
			c0, c1 := s.Cols(n, len(cells))
			for x := max(c0, 0); x < c1; x++ {
				switch c := cells[x]; {
				case c.Width == 0 && c.Content == "": // the right half of a wide char
				case c.Content == "":
					out = append(out, ' ')
				default:
					out = append(out, c.Content...)
				}
			}
			brk = !wrapped || s.Mode == SelectBlock
		}
		if n == q.Line {
			break
		}
	}
	return string(bytes.TrimRight(out, " "))
}

// Text is s's text from history and the screen; see Selection.Text. While
// the alt screen is up, only the screen's lines are there.
func (t *emulator) Text(s Selection) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.snapshot()
	h := &t.st.hist
	screen := GridLines(&g, h.pushed)
	oldest := h.pushed - uint64(h.len())
	var buf []Cell
	return s.Text(func(n uint64) ([]Cell, bool, bool) {
		if n >= h.pushed || g.AltScreen || n < oldest {
			return screen(n)
		}
		l := h.at(int(n - oldest))
		buf = append(buf[:0], make([]Cell, l.width())...)
		l.fill(buf)
		return buf, l.wrapped(), true
	})
}
