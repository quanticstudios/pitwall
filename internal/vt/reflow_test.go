package vt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// all returns every row, history first, then the screen.
func all(e Emulator) []string {
	n := e.ScrollbackLen()
	var out []string
	for i := range n {
		out = append(out, row(e.SnapshotAt(n-i), 0))
	}
	g := e.Snapshot()
	for y := range g.Rows {
		out = append(out, row(g, y))
	}
	return out
}

func TestReflowWidenRestoresLines(t *testing.T) {
	e := New(10, 4, nil)
	feed(e, "0123456789abcdefghij-tail\r\nshort")
	if got, want := all(e), []string{"0123456789", "abcdefghij", "-tail", "short"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 10 cols %q, want %q", got, want)
	}
	e.Resize(30, 4)
	g := e.Snapshot()
	if got, want := all(e), []string{"0123456789abcdefghij-tail", "short", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 30 cols %q, want %q", got, want)
	}
	if g.Cursor.X != 5 || g.Cursor.Y != 1 {
		t.Fatalf("cursor %+v", g.Cursor)
	}
}

func TestReflowRoundTrip(t *testing.T) {
	e := New(20, 4, nil)
	feed(e, "first line, long enough to wrap\r\nsecond\r\n\r\nfourth: a b c d e f g h\r\nlast")
	before := all(e)
	e.Resize(7, 4)
	want := []string{"first l", "ine, lo", "ng enou", "gh to w", "rap", "second", "", "fourth:", " a b c", "d e f g", " h", "last"}
	if got := all(e); !reflect.DeepEqual(got, want) {
		t.Fatalf("at 7 cols %q, want %q", got, want)
	}
	e.Resize(20, 4)
	if got := all(e); !reflect.DeepEqual(got, before) {
		t.Fatalf("back at 20 cols %q, want %q", got, before)
	}
	if g := e.Snapshot(); g.Cursor.X != 4 || row(g, g.Cursor.Y) != "last" {
		t.Fatalf("cursor %+v on %q", g.Cursor, row(g, g.Cursor.Y))
	}
}

func TestReflowKeepsHardBreaks(t *testing.T) {
	e := New(10, 3, nil)
	// The first line fills the row exactly; CR LF ends it, so it does not wrap.
	feed(e, "0123456789\r\nabc")
	e.Resize(20, 3)
	if got, want := all(e), []string{"0123456789", "abc", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestReflowWideChars(t *testing.T) {
	e := New(6, 3, nil)
	feed(e, "ab中文字")
	if got, want := all(e), []string{"ab中文", "字", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 6 cols %q, want %q", got, want)
	}
	// 文 would take columns 4 and 5 of 5: it moves down whole.
	e.Resize(5, 3)
	g := e.Snapshot()
	if got, want := all(e), []string{"ab中", "文字", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 5 cols %q, want %q", got, want)
	}
	if c := g.At(1, 1); c.Content != "" || c.Width != 0 {
		t.Fatalf("right half of 文: %+v", c)
	}
	// The cell 文 left empty is not text: widening joins without a gap.
	e.Resize(8, 3)
	if got, want := all(e), []string{"ab中文字", "", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at 8 cols %q, want %q", got, want)
	}
}

func TestReflowCursor(t *testing.T) {
	e := New(10, 4, nil)
	feed(e, "0123456789abcde\x1b[1;8H") // on the 7
	e.Resize(5, 4)
	g := e.Snapshot()
	if c := g.Cursor; c.X != 2 || c.Y != 1 || g.At(c.X, c.Y).Content != "7" {
		t.Fatalf("at 5 cols cursor %+v on %q", c, g.At(c.X, c.Y).Content)
	}
	e.Resize(20, 4)
	if g = e.Snapshot(); g.Cursor.X != 7 || g.Cursor.Y != 0 {
		t.Fatalf("at 20 cols cursor %+v", g.Cursor)
	}

	// A full row leaves the cursor waiting to wrap; it still does after a
	// resize that ends a row at the same character.
	e = New(10, 4, nil)
	feed(e, "0123456789")
	e.Resize(5, 4)
	if g = feed(e, "x"); row(g, 2) != "x" || g.Cursor.X != 1 || g.Cursor.Y != 2 {
		t.Fatalf("pending wrap: %q cursor %+v", screenText(g), g.Cursor)
	}
}

func TestReflowLeavesAltScreen(t *testing.T) {
	e := New(10, 3, nil)
	feed(e, "0123456789abc\x1b[?1049hALT-456789xyz")
	e.Resize(20, 3)
	g := e.Snapshot()
	if !g.AltScreen || row(g, 0) != "ALT-456789" || row(g, 1) != "xyz" {
		t.Fatalf("alt screen %q", screenText(g))
	}
	g = feed(e, "\x1b[?1049l")
	if row(g, 0) != "0123456789abc" || g.Cursor.X != 13 || g.Cursor.Y != 0 {
		t.Fatalf("main screen %q cursor %+v", screenText(g), g.Cursor)
	}
}

func TestReflowKeepsColorsAndLinks(t *testing.T) {
	e := New(6, 4, nil)
	feed(e, "\x1b[31mredred\x1b[42;1mgreen\x1b[0m_\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\")
	red := Cell{Content: "r", Width: 1, FG: PaletteFlag | 1}
	green := Cell{Content: "g", Width: 1, FG: PaletteFlag | 1, BG: PaletteFlag | 2, Attrs: Bold}
	link := Cell{Content: "l", Width: 1, Link: "https://example.com"}
	for _, w := range []int{20, 4, 20} {
		e.Resize(w, 4)
		g := e.Snapshot()
		s := strings.Join(all(e), "")
		at := func(i int) Cell { return g.At(i%w, i/w) }
		if s != "redredgreen_link" || at(0) != red || at(6) != green || at(12) != link {
			t.Fatalf("at %d cols %q: %+v %+v %+v", w, s, at(0), at(6), at(12))
		}
	}
}

func TestReflowAnchorsBottom(t *testing.T) {
	// A full screen narrows: its top rows go into history, the prompt stays
	// on the bottom row.
	e := New(10, 3, nil)
	feed(e, lines("line-long", 0, 4)+"$ ")
	e.Resize(5, 3)
	g := e.Snapshot()
	if row(g, 2) != "$" || g.Cursor.Y != 2 || g.Cursor.X != 2 {
		t.Fatalf("narrow %q cursor %+v", screenText(g), g.Cursor)
	}
	// Widening brings history back down to fill the screen.
	e.Resize(10, 3)
	if got := screenText(e.Snapshot()); got != "line-long2\nline-long3\n$\n" {
		t.Fatalf("widen %q", got)
	}

	// Blank rows below a prompt take up growth before anything leaves.
	e = New(6, 4, nil)
	feed(e, "$ echo hi")
	e.Resize(3, 4)
	if g := e.Snapshot(); e.ScrollbackLen() != 0 || screenText(g) != "$ e\ncho\n hi\n\n" {
		t.Fatalf("prompt %q, history %d", screenText(g), e.ScrollbackLen())
	}

	// A shorter screen pushes its top into history rather than cutting off
	// the bottom.
	e = New(10, 4, nil)
	feed(e, "a\r\nb\r\nc\r\nd")
	e.Resize(10, 2)
	if got := screenText(e.Snapshot()); got != "c\nd\n" || e.ScrollbackLen() != 2 {
		t.Fatalf("shorter %q, history %d", got, e.ScrollbackLen())
	}
}

// BenchmarkReflow drags a pane's edge across 10,000 lines of history: each op
// is one resize, a column narrower or wider than the last.
func BenchmarkReflow(b *testing.B) {
	e := New(120, 40, nil)
	var s strings.Builder
	for i := range historyMax {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&s, "\x1b[1m%d\x1b[0m %s\r\n", i, strings.Repeat("agent output that runs past the edge ", 6))
		case 1:
			fmt.Fprintf(&s, "  \x1b[32m+ line %d\x1b[0m 中文 ✅\r\n", i)
		default:
			s.WriteString("\r\n")
		}
	}
	feed(e, s.String())
	if n := e.ScrollbackLen(); n != historyMax {
		b.Fatalf("history %d lines", n)
	}
	widths := make([]int, 0, 120)
	for w := 120; w > 60; w-- {
		widths = append(widths, w)
	}
	for w := 60; w < 120; w++ {
		widths = append(widths, w)
	}
	i := 0
	for b.Loop() {
		e.Resize(widths[i%len(widths)], 40)
		i++
	}
}
