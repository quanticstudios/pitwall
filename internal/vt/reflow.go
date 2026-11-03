package vt

import (
	"image/color"
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

// pos is a cell in a list of rows.
type pos struct{ row, col int }

// reflow resizes to w×h and rewraps history and the main screen as one text,
// as Ghostty, kitty and WezTerm do: soft-wrapped rows join back into logical
// lines that wrap again at w, and line breaks stay. The cursor and the saved
// cursor stay on their characters. The bottom stays put: blank rows below the
// text take up growth first, then the top rows go into history; when the text
// needs fewer rows, history comes back down to fill the screen. The alt screen
// is only resized; its app redraws it.
func (t *emulator) reflow(w, h int) {
	lines, flags, cur, saved := t.e.MainLines()
	saved.Y = min(saved.Y, len(lines)-1)
	// Rows below both cursors and the last non-blank row are space, not text.
	last := max(cur.Y, saved.Y)
	for y := len(lines) - 1; y > last; y-- {
		if flags[y]&xvt.LineWrapped != 0 || !blank(lines[y]) {
			last = y
			break
		}
	}
	trail := len(lines) - 1 - last

	hist := &t.st.hist
	n := hist.len()
	rows := make([]line, n, n+last+1)
	for i := range n {
		rows[i] = *hist.at(i)
	}
	for y := range last + 1 {
		keep := 0 // blanks the cursor sits after are text too
		if y == cur.Y {
			keep = cur.X
		}
		if y == saved.Y {
			keep = max(keep, saved.X)
		}
		rows = append(rows, hist.line(lines[y], flags[y], keep))
	}
	c, s, top := pos{n + cur.Y, cur.X}, pos{n + saved.Y, saved.X}, pos{n, 0}
	// Images move with the cell at their top left, as the cursors do. Row
	// i of rows is line base+i, as kitty graphics placements number them.
	base, old := int64(hist.pushed)-int64(n), len(rows)
	var placed, under []*place
	marks := []*pos{&c, &s, &top}
	for _, pl := range t.st.gfx.places {
		switch r := int64(pl.line) - base; {
		case pl.alt:
		case r >= int64(old):
			under = append(under, pl)
		case r >= 0:
			placed = append(placed, pl)
			marks = append(marks, &pos{int(r), pl.col})
		}
	}
	shellStart := len(marks)
	var shell []*shellMark
	for i := range t.st.shell {
		m := &t.st.shell[i]
		if r := int64(m.At.Line) - base; r >= 0 && r < int64(old) {
			shell = append(shell, m)
			marks = append(marks, &pos{int(r), m.At.Col})
		}
	}
	if w != len(lines[0]) {
		rows = rewrap(rows, w, marks...)
	}

	below := min(trail, max(0, h-(len(rows)-top.row)))
	start := min(max(0, len(rows)+below-h), c.row)
	hist.set(rows[:start])
	// Row i is now line pushed-start+i, the same line number as before
	// unless history dropped its oldest rows.
	at := func(row int64) uint64 { return uint64(max(0, int64(hist.pushed)-int64(start)+row)) }
	for i, pl := range placed {
		m := marks[3+i]
		pl.line, pl.col = at(int64(m.row)), min(m.col, w-1)
	}
	for _, pl := range under { // on blank rows under the text: as far below it
		pl.line = at(int64(pl.line) - base + int64(len(rows)-old))
	}
	for i, m := range shell {
		p := marks[shellStart+i]
		m.At = Pos{Line: at(int64(p.row)), Col: p.col}
	}
	t.pruneShell()
	screen, soft := make([]uv.Line, h), make([]xvt.LineFlags, h)
	for y := range screen {
		if i := start + y; i < len(rows) {
			screen[y], soft[y] = rows[i].unpack(w), rows[i].flags
		} else {
			screen[y] = uv.NewLine(w)
		}
	}
	t.e.ResizeMain(w, h, screen, soft, uv.Pos(c.col, c.row-start), uv.Pos(s.col, max(0, s.row-start)))
	t.st.gfx.prune(t.e, t.st)
}

func blank(l uv.Line) bool {
	for i := range l {
		if !l[i].IsZero() && !l[i].Equal(&uv.EmptyCell) {
			return false
		}
	}
	return true
}

// rewrap joins each run of soft-wrapped rows into one logical line and wraps
// it again at w columns. A wide char that would straddle the edge moves to the
// next row whole. Each mark moves with the cell it points at; one past the end
// of a line stays past its end. A row that is a whole line and fits is kept
// as it is, so lines that never wrapped cost one copy.
func rewrap(rows []line, w int, marks ...*pos) []line {
	out := make([]line, 0, len(rows))
	moved := make([]pos, len(marks))
	at := make([]int, len(marks)) // a mark's column in the logical line
	order := make([]int, len(marks))
	for k, m := range marks {
		moved[k] = *m
		order[k] = k
	}
	// Shell marks can number in the thousands; visit only the current line's.
	slices.SortFunc(order, func(a, b int) int { return marks[a].row - marks[b].row })
	next := 0
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows)-1 && rows[j].wrapped() {
			j++
		}
		for next < len(order) && marks[order[next]].row < i {
			next++
		}
		first := next
		for next < len(order) && marks[order[next]].row <= j {
			next++
		}
		group := order[first:next]
		if i == j && rows[i].width() <= w {
			for _, k := range group {
				moved[k].row = len(out)
			}
			out = append(out, rows[i])
			i++
			continue
		}
		row, offset := i, 0
		for _, k := range group {
			m := marks[k]
			for row < m.row {
				offset += rows[row].width()
				row++
			}
			at[k] = offset + m.col
		}
		slices.SortFunc(group, func(a, b int) int { return at[a] - at[b] })
		markNext := 0
		l := join(rows[i : j+1])
		var shell xvt.LineFlags // the OSC 133 marks of the line's rows go on its first
		for _, r := range rows[i : j+1] {
			shell |= r.flags &^ xvt.LineWrapped
		}
		n, off, ri := l.width(), 0, 0
		for a := 0; ; {
			b := min(a+w, n)
			if b < n && l.cells != nil && l.cells[b]&3 == 0 {
				b-- // keep a wide char whole
			}
			if b == a && a < n {
				b = min(a+2, n) // a wide char wider than the screen
			}
			r := line{}
			if a == 0 {
				r.flags = shell
			}
			if b < n || rows[j].wrapped() {
				r.flags |= xvt.LineWrapped
			}
			size := b - a
			if l.cells != nil {
				r.cells, size = l.cells[a:b:b], 0
				for _, c := range r.cells {
					size += int(c >> 2)
				}
			}
			r.text = l.text[off : off+size]
			off += size
			for ri+1 < len(l.runs) && int(l.runs[ri+1].col) <= a {
				ri++
			}
			if b > a {
				r.runs = append(r.runs, l.runs[ri].withCol(0))
				for _, rn := range l.runs[ri+1:] {
					if int(rn.col) >= b {
						break
					}
					r.runs = append(r.runs, rn.withCol(rn.col-uint32(a)))
				}
			}
			for markNext < len(group) && (at[group[markNext]] < b || b == n) {
				k := group[markNext]
				if at[k] >= a {
					moved[k] = pos{len(out), at[k] - a}
				}
				markNext++
			}
			out = append(out, r)
			if a = b; a >= n {
				break
			}
		}
		i = j + 1
	}
	for k, m := range marks {
		*m = moved[k]
	}
	return out
}

// join concatenates rows into one line.
func join(rows []line) line {
	if len(rows) == 1 {
		return rows[0]
	}
	size, cols, ascii := 0, 0, true
	for i := range rows {
		size += len(rows[i].text)
		cols += rows[i].width()
		ascii = ascii && rows[i].cells == nil
	}
	var l line
	var b strings.Builder
	b.Grow(size)
	if !ascii {
		l.cells = make([]uint16, 0, cols)
	}
	col := uint32(0)
	for i := range rows {
		r := &rows[i]
		b.WriteString(r.text)
		switch {
		case ascii:
		case r.cells != nil:
			l.cells = append(l.cells, r.cells...)
		default:
			for range len(r.text) {
				l.cells = append(l.cells, 1<<2|1)
			}
		}
		for _, rn := range r.runs {
			rn.col += col
			if k := len(l.runs); k == 0 || l.runs[k-1] != rn.withCol(l.runs[k-1].col) {
				l.runs = append(l.runs, rn)
			}
		}
		col += uint32(r.width())
	}
	l.text = b.String()
	return l
}

// unpack turns l back into a screen row of w cells. A soft-wrapped row is
// padded with zero cells, which line drops again, rather than blanks, which
// are text.
func (l *line) unpack(w int) uv.Line {
	cells := make([]Cell, w)
	l.fill(cells)
	out := make(uv.Line, w)
	n := l.width()
	for x, c := range cells {
		if x >= n && l.wrapped() || c.Width == 0 && c.Content == "" {
			continue // padding, or the right half of a wide char
		}
		out[x] = uv.Cell{Content: c.Content, Width: int(c.Width), Link: uv.Link{URL: c.Link},
			Style: uv.Style{Fg: fromColor(c.FG), Bg: fromColor(c.BG), Attrs: fromAttr(c.Attrs)}}
		if c.Attrs&Underline != 0 {
			out[x].Style.Underline = uv.UnderlineSingle
		}
	}
	return out
}

func fromColor(c Color) color.Color {
	switch c &^ 0xffffff {
	case PaletteFlag:
		return ansi.IndexedColor(c & 0xff)
	case RGBFlag:
		return rgb(uint32(c))
	}
	return nil
}

func fromAttr(a Attr) uint8 {
	var out uint8
	for _, m := range attrs {
		if a&m.a != 0 {
			out |= m.uv
		}
	}
	return out
}
