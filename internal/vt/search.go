package vt

import (
	"bytes"
	"context"
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
	lines, more := t.search(context.Background(), query, limit, false)
	var matches []Match
	for _, l := range lines {
		matches = append(matches, l.Match)
	}
	return matches, more
}

// SearchLine is a match and a bounded excerpt of its row.
type SearchLine struct {
	Match  Match
	Text   string
	Hidden bool // retained main-screen history while the alternate screen is up
}

// SearchContext captures excerpts with their matches under one lock. It is
// optional on Emulator and checks cancellation between rows. Session search
// includes retained main-screen history even while the alternate screen is up.
func (t *emulator) SearchContext(ctx context.Context, query string, limit int) ([]SearchLine, bool) {
	return t.search(ctx, query, limit, true)
}

func (t *emulator) search(ctx context.Context, query string, limit int, snippets bool) ([]SearchLine, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if query == "" || ctx.Err() != nil {
		return nil, false
	}
	f := NewFinder(query)
	g := t.snapshot()
	h := &t.st.hist
	var out []SearchLine
	var row []Match
	// add takes one row's matches, newest first, and reports whether limit
	// left some out.
	add := func(cells []Cell, ascii string) bool {
		for i := len(row) - 1; i >= 0; i-- {
			if len(out) == limit {
				return true
			}
			m := row[i]
			text := ""
			if snippets {
				start, end := max(0, m.Col-20), m.Col+max(m.Cols, 100)
				if cells == nil {
					text = ascii[min(start, len(ascii)):min(end, len(ascii))]
				} else {
					var b strings.Builder
					for _, c := range cells[min(start, len(cells)):min(end, len(cells))] {
						if c.Width > 0 {
							if c.Content == "" {
								b.WriteByte(' ')
							} else {
								b.WriteString(c.Content)
							}
						}
					}
					text = b.String()
				}
				text = strings.TrimSpace(text)
			}
			out = append(out, SearchLine{Match: m, Text: text, Hidden: g.AltScreen && m.Line < h.pushed})
		}
		return false
	}
	more := false
	for y := g.Rows - 1; y >= 0 && !more; y-- {
		if ctx.Err() != nil {
			return nil, false
		}
		cells := g.Cells[y*g.Cols : (y+1)*g.Cols]
		row = f.Row(row[:0], h.pushed+uint64(y), cells)
		more = add(cells, "")
	}
	if !g.AltScreen || snippets {
		var buf []Cell
		for i := h.len() - 1; i >= 0 && !more; i-- {
			if ctx.Err() != nil {
				return nil, false
			}
			l := h.at(i)
			line := h.pushed - uint64(h.len()-i)
			if l.cells == nil {
				// Packed ASCII has one byte per cell; rebuilding styled cells
				// for each row made session search miss its 300 ms budget.
				row = f.asciiRow(row[:0], line, l.text)
				more = add(nil, l.text)
			} else {
				buf = slices.Grow(buf[:0], l.width())[:l.width()]
				l.fill(buf)
				row = f.Row(row[:0], line, buf)
				more = add(buf, "")
			}
		}
	}
	slices.Reverse(out)
	return out, more
}

func (f *Finder) asciiRow(dst []Match, line uint64, text string) []Match {
	if f.fold {
		f.text = f.text[:0]
		for i := range len(text) {
			c := text[i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			f.text = append(f.text, c)
		}
		for i := 0; i < len(f.text); {
			j := bytes.Index(f.text[i:], f.q)
			if j < 0 {
				break
			}
			dst = append(dst, Match{Line: line, Col: i + j, Cols: len(f.q)})
			i += j + len(f.q)
		}
		return dst
	}
	for i := 0; i < len(text); {
		j := strings.Index(text[i:], string(f.q))
		if j < 0 {
			break
		}
		dst = append(dst, Match{Line: line, Col: i + j, Cols: len(f.q)})
		i += j + len(f.q)
	}
	return dst
}
