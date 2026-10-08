package vt

import (
	"bytes"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match is one hit of a search. Line numbers lines as ScrollbackPushed
// counts them, so a match keeps its number while output scrolls it into
// history: screen row y is line ScrollbackPushed()+y, and the oldest history
// line is ScrollbackPushed()-ScrollbackLen(). Col is its first cell and Cols
// the cells it covers; a wide character covers two.
type Match struct {
	Line      uint64
	Col, Cols int
}

// Finder finds one query in rows of cells, case-insensitively unless the
// query has an upper-case letter. A match starts and ends on cell
// boundaries, so it never takes half a grapheme. Rows match on their own: a
// line that wrapped does not match across the wrap.
type Finder struct {
	q     []byte
	fold  bool
	text  []byte // the row's text, folded when fold is set
	cells []span // where each cell's text starts, and a sentinel at the end
}

type span struct{ at, col int }

// NewFinder returns a Finder for query; an empty query matches nothing.
func NewFinder(query string) *Finder {
	f := &Finder{fold: !strings.ContainsFunc(query, unicode.IsUpper)}
	if f.fold {
		query = strings.ToLower(query)
	}
	f.q = []byte(query)
	return f
}

// Row appends the matches in cells, left to right, to dst with Line set to
// line.
func (f *Finder) Row(dst []Match, line uint64, cells []Cell) []Match {
	if len(f.q) == 0 {
		return dst
	}
	f.text, f.cells = f.text[:0], f.cells[:0]
	end := 0
	for x, c := range cells {
		if c.Width == 0 {
			continue // the right half of a wide character
		}
		f.cells = append(f.cells, span{len(f.text), x})
		end = x + int(c.Width)
		switch s := c.Content; {
		case s == "":
			f.text = append(f.text, ' ')
		case !f.fold:
			f.text = append(f.text, s...)
		case len(s) == 1 && s[0] < utf8.RuneSelf:
			b := s[0]
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			f.text = append(f.text, b)
		default:
			f.text = append(f.text, strings.ToLower(s)...)
		}
	}
	f.cells = append(f.cells, span{len(f.text), end})
	cell := func(at int) (int, bool) {
		return slices.BinarySearchFunc(f.cells, at, func(s span, at int) int { return s.at - at })
	}
	for i := 0; i < len(f.text); {
		j := bytes.Index(f.text[i:], f.q)
		if j < 0 {
			break
		}
		a, okA := cell(i + j)
		b, okB := cell(i + j + len(f.q))
		if okA && okB {
			dst = append(dst, Match{Line: line, Col: f.cells[a].col, Cols: f.cells[b].col - f.cells[a].col})
			i += j + len(f.q)
			continue
		}
		_, size := utf8.DecodeRune(f.text[i+j:])
		i += j + size
	}
	return dst
}

// Search returns query's matches in history and on the screen, oldest
// first; while the alt screen is up, only the screen's. It keeps the newest
// limit and reports whether it left older ones out.
func (t *emulator) Search(query string, limit int) ([]Match, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := NewFinder(query)
	g := t.snapshot()
	h := &t.st.hist
	var out, row []Match
	// add takes one row's matches, newest first, and reports whether limit
	// left some out.
	add := func() bool {
		for i := len(row) - 1; i >= 0; i-- {
			if len(out) == limit {
				return true
			}
			out = append(out, row[i])
		}
		return false
	}
	more := false
	for y := g.Rows - 1; y >= 0 && !more; y-- {
		row = f.Row(row[:0], h.pushed+uint64(y), g.Cells[y*g.Cols:(y+1)*g.Cols])
		more = add()
	}
	if !g.AltScreen {
		var buf []Cell
		for i := h.len() - 1; i >= 0 && !more; i-- {
			l := h.at(i)
			buf = slices.Grow(buf[:0], l.width())[:l.width()]
			l.fill(buf)
			row = f.Row(row[:0], h.pushed-uint64(h.len()-i), buf)
			more = add()
		}
	}
	slices.Reverse(out)
	return out, more
}
