package vt

import (
	"reflect"
	"testing"
)

// eAcute is e and a combining acute accent: one cell, two runes.
var eAcute = "e" + string(rune(0x301))

// cellsOf lays s out as cells: CJK takes two columns, and a combining accent
// joins the cell before it.
func cellsOf(s string) []Cell {
	var cells []Cell
	for _, r := range s {
		switch {
		case r == 0x301:
			cells[len(cells)-1].Content += string(r)
		case r >= 0x2e80:
			cells = append(cells, Cell{Content: string(r), Width: 2}, Cell{})
		default:
			cells = append(cells, Cell{Content: string(r), Width: 1})
		}
	}
	return cells
}

func TestFinderRow(t *testing.T) {
	type m = Match
	for _, tc := range []struct {
		text, query string
		want        []Match
	}{
		{"Error error ERROR", "error", []m{{7, 0, 5}, {7, 6, 5}, {7, 12, 5}}},
		{"Error error ERROR", "Error", []m{{7, 0, 5}}}, // a capital turns case on
		{"Error error ERROR", "ERR", []m{{7, 12, 3}}},
		{"aaaa", "aa", []m{{7, 0, 2}, {7, 2, 2}}}, // left to right, not overlapping
		{"漢字 error 漢字", "error", []m{{7, 5, 5}}},
		{"漢字 error 漢字", "字", []m{{7, 2, 2}, {7, 13, 2}}},
		{"漢字 error 漢字", "漢字", []m{{7, 0, 4}, {7, 11, 4}}},
		{"r 漢字", "r 漢", []m{{7, 0, 4}}},
		{"caf" + eAcute + " cafe", "cafe", []m{{7, 5, 4}}}, // not the first: its e has an accent
		{"caf" + eAcute + " cafe", "caf\u00e9", nil},       // a composed é is another string
		{"caf" + eAcute, "caf" + eAcute, []m{{7, 0, 4}}},
		{"ÉCOLE école", "école", []m{{7, 0, 5}, {7, 6, 5}}},
		{"anything", "", nil},
	} {
		got := NewFinder(tc.query).Row(nil, 7, cellsOf(tc.text))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q in %q: %v, want %v", tc.query, tc.text, got, tc.want)
		}
	}
	// A cell with no content is a blank.
	if got := NewFinder("a b").Row(nil, 0, []Cell{{Content: "a", Width: 1}, {Width: 1}, {Content: "b", Width: 1}}); len(got) != 1 {
		t.Errorf("blank cell: %v", got)
	}
}

func TestSearch(t *testing.T) {
	e := New(10, 3, nil)
	feed(e, lines("L", 0, 6))
	// L0-L3 scrolled off; the screen shows L4, L5 and the cursor's row.
	if e.ScrollbackLen() != 4 || e.ScrollbackPushed() != 4 {
		t.Fatalf("history %d, pushed %d", e.ScrollbackLen(), e.ScrollbackPushed())
	}
	got, more := e.Search("l", 100)
	var lines []uint64
	for _, m := range got {
		lines = append(lines, m.Line)
	}
	if !reflect.DeepEqual(lines, []uint64{0, 1, 2, 3, 4, 5}) || more {
		t.Fatalf("lines %v, more %v", lines, more)
	}
	// Row y of SnapshotAt(off) shows line ScrollbackPushed()-off+y.
	for _, m := range got {
		off := max(0, 4-int(m.Line))
		g := e.SnapshotAt(off)
		y := int(m.Line) - (4 - off)
		if c := g.At(m.Col, y); c.Content != "L" {
			t.Errorf("match %v: row %d shows %q", m, y, screenText(g))
		}
	}

	got, more = e.Search("L", 4)
	if len(got) != 4 || got[0].Line != 2 || got[3].Line != 5 || !more {
		t.Fatalf("limit 4: %v, more %v", got, more)
	}
	if got, more = e.Search("L", 6); len(got) != 6 || more {
		t.Fatalf("limit 6: %v, more %v", got, more)
	}

	// New output scrolls lines off; their numbers stay.
	feed(e, "x\r\ny\r\n")
	if got, _ = e.Search("L5", 10); len(got) != 1 || got[0].Line != 5 {
		t.Fatalf("after scrolling: %v", got)
	}

	// The alt screen searches only itself.
	feed(e, "\x1b[?1049hL9 alt")
	if got, _ = e.Search("L", 10); !reflect.DeepEqual(got, []Match{{Line: e.ScrollbackPushed(), Col: 0, Cols: 1}}) {
		t.Fatalf("alt screen: %v", got)
	}
}

// TestSearchAfterReflow checks match numbers stay what ScrollbackPushed
// says after a resize rewraps history: narrowing makes more history rows
// than lines were ever pushed, and widening fewer. Row y of SnapshotAt(off)
// still shows line ScrollbackPushed()-off+y.
func TestSearchAfterReflow(t *testing.T) {
	e := New(10, 3, nil)
	feed(e, "L0-456789\r\nL1-456789\r\nL2-456789\r\nL3-456789\r\nL4")
	for _, w := range []int{5, 3, 10, 20} {
		e.Resize(w, 3)
		pushed, n := e.ScrollbackPushed(), e.ScrollbackLen()
		if uint64(n) > pushed {
			t.Fatalf("at %d cols: %d history rows, %d pushed", w, n, pushed)
		}
		got, _ := e.Search("L", 100)
		if len(got) != 5 {
			t.Fatalf("at %d cols: %v", w, got)
		}
		for _, m := range got {
			off := max(0, min(n, int(int64(pushed-m.Line))))
			g, y := e.SnapshotAt(off), int(int64(m.Line-pushed))+off
			if c := g.At(m.Col, y); c.Content != "L" {
				t.Errorf("at %d cols: match %v not at row %d of %q", w, m, y, screenText(g))
			}
		}
		for i := 1; i < len(got); i++ {
			if got[i].Line <= got[i-1].Line {
				t.Fatalf("at %d cols: lines out of order %v", w, got)
			}
		}
	}
}
