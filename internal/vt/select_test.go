package vt

import (
	"strings"
	"testing"
)

// textGrid builds a grid of rows cols wide; a row ending in "\" soft-wraps
// into the next.
func textGrid(cols int, rows ...string) *Grid {
	g := &Grid{Cols: cols, Rows: len(rows), Wrapped: make([]bool, len(rows))}
	for y, r := range rows {
		r, g.Wrapped[y] = strings.CutSuffix(r, `\`)
		for x := range cols {
			c := Cell{Content: " ", Width: 1}
			if x < len(r) {
				c.Content = r[x : x+1]
			}
			g.Cells = append(g.Cells, c)
		}
	}
	return g
}

func TestSelectionText(t *testing.T) {
	g := textGrid(6, "one", `long l\`, `ine he\`, "re", "x  y", "last")
	lines := GridLines(g, 100) // the top row is line 100
	pos := func(y, x int) Pos { return Pos{Line: 100 + uint64(y), Col: x} }
	for _, tc := range []struct {
		name string
		s    Selection
		want string
	}{
		{"backwards across a hard break", Selection{A: pos(1, 2), B: pos(0, 1)}, "ne\nlon"},
		{"a soft wrap adds no line break", Selection{A: pos(1, 5), B: pos(3, 1)}, "line here"},
		{"blanks before a break go", Selection{A: pos(4, 0), B: pos(5, 0)}, "x  y\nl"},
		{"block", Selection{A: pos(1, 3), B: pos(4, 0), Mode: SelectBlock}, "long\nine\nre\nx  y"},
		{"block keeps a line per row", Selection{A: pos(1, 5), B: pos(2, 5), Mode: SelectBlock}, "l\ne"},
		{"line on a middle row takes the logical line", Selection{A: pos(2, 3), B: pos(2, 3), Mode: SelectLines}, "long line here"},
		{"lines across", Selection{A: pos(0, 4), B: pos(1, 0), Mode: SelectLines}, "one\nlong line here"},
		{"lines past the view are left out", Selection{A: pos(5, 0), B: pos(9, 0)}, "last"},
	} {
		if got := tc.s.Text(lines); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if s := (Selection{A: pos(3, 0), B: pos(3, 0), Mode: SelectLines}).Expand(lines); s.A != pos(1, 0) || s.B.Line != pos(3, 0).Line {
		t.Errorf("Expand = %+v", s)
	}
	if c0, c1 := (Selection{A: pos(1, 4), B: pos(3, 2), Mode: SelectBlock}).Cols(pos(2, 0).Line, 6); c0 != 2 || c1 != 5 {
		t.Errorf("block cols %d,%d", c0, c1)
	}
	// Wide chars: the right half is skipped, not doubled.
	g = &Grid{Cols: 4, Rows: 1, Cells: []Cell{{Content: "a", Width: 1}, {Content: "漢", Width: 2}, {}, {Content: "b", Width: 1}}}
	if got := (Selection{B: Pos{Col: 3}}).Text(GridLines(g, 0)); got != "a漢b" {
		t.Errorf("wide = %q", got)
	}
}

// TestEmulatorText copies a selection that spans history and the screen,
// and a logical line that wrapped across both, through the emulator.
func TestEmulatorText(t *testing.T) {
	e := New(8, 3, nil)
	feed(e, lines("h", 0, 6)+"0123456789abcdefghij\r\nend")
	// History: h0-h5 and "01234567"; the screen: "89abcdef", "ghij", "end".
	pushed := e.ScrollbackPushed()
	at := func(back int, col int) Pos { return Pos{Line: pushed - uint64(back), Col: col} }
	if got, want := e.Text(Selection{A: at(3, 1), B: at(-1, 1)}), "4\nh5\n0123456789abcdefgh"; got != want {
		t.Errorf("chars: %q, want %q", got, want)
	}
	if got, want := e.Text(Selection{A: at(-1, 0), B: at(-1, 0), Mode: SelectLines}), "0123456789abcdefghij"; got != want {
		t.Errorf("logical line: %q, want %q", got, want)
	}
	if got, want := e.Text(Selection{A: at(4, 0), B: at(0, 1), Mode: SelectBlock}), "h3\nh4\nh5\n01\n89"; got != want {
		t.Errorf("block: %q, want %q", got, want)
	}

	small := New(8, 3, nil)
	small.(interface{ SetScrollback(int) }).SetScrollback(5)
	feed(small, lines("s", 0, 20))
	if n := small.ScrollbackLen(); n != 5 {
		t.Errorf("SetScrollback(5): ScrollbackLen %d", n)
	}
}
