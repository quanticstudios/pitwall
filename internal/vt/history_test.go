package vt

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	xvt "github.com/charmbracelet/x/vt"
)

func lines(prefix string, from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%s%d\r\n", prefix, i)
	}
	return b.String()
}

func TestHistoryOrderAndStyles(t *testing.T) {
	e := New(10, 3, nil)
	feed(e, "\x1b[31mred\x1b[0m \x1b[1;44mbold\x1b[0m\r\n"+lines("L", 1, 5))
	// 5 lines and an empty cursor row on a 3-row screen: 3 scrolled off.
	if n := e.ScrollbackLen(); n != 3 {
		t.Fatalf("ScrollbackLen %d", n)
	}
	g := e.SnapshotAt(3)
	if got := screenText(g); got != "red bold\nL1\nL2\n" {
		t.Fatalf("view %q", got)
	}
	for x, want := range []Cell{
		{Content: "r", Width: 1, FG: PaletteFlag | 1},
		{Content: " ", Width: 1},
		{Content: "b", Width: 1, BG: PaletteFlag | 4, Attrs: Bold},
	} {
		if c := g.At([]int{0, 3, 4}[x], 0); c != want {
			t.Errorf("cell %d: %+v, want %+v", x, c, want)
		}
	}
	if g.Cursor.Visible {
		t.Error("cursor below the view should be hidden")
	}
}

func TestSnapshotAtOffsets(t *testing.T) {
	e := New(5, 3, nil)
	feed(e, lines("h", 0, 4)+"a\r\nb\r\nc")
	if n := e.ScrollbackLen(); n != 4 {
		t.Fatalf("ScrollbackLen %d", n)
	}
	if !reflect.DeepEqual(e.SnapshotAt(0), e.Snapshot()) {
		t.Fatal("SnapshotAt(0) differs from Snapshot")
	}
	g := e.SnapshotAt(1)
	if got := screenText(g); got != "h3\na\nb\n" {
		t.Fatalf("offset 1: %q", got)
	}
	if g.Cursor.Y != 3 || g.Cursor.Visible {
		t.Fatalf("offset 1 cursor %+v", g.Cursor)
	}
	if got := screenText(e.SnapshotAt(4)); got != "h0\nh1\nh2\n" {
		t.Fatalf("offset 4: %q", got)
	}
	if got := screenText(e.SnapshotAt(99)); got != "h0\nh1\nh2\n" {
		t.Fatalf("offset past max: %q", got)
	}
	// The cursor stays visible while its row is in view.
	g = feed(e, "\x1b[1;2H")
	if g = e.SnapshotAt(2); g.Cursor.Y != 2 || g.Cursor.X != 1 || !g.Cursor.Visible {
		t.Fatalf("offset 2 cursor %+v", g.Cursor)
	}
}

func TestAltScreenHasNoHistory(t *testing.T) {
	e := New(5, 2, nil)
	feed(e, "main\r\n")
	feed(e, "\x1b[?1049h"+lines("alt", 0, 10))
	if n := e.ScrollbackLen(); n != 0 {
		t.Fatalf("ScrollbackLen on alt %d", n)
	}
	if !reflect.DeepEqual(e.SnapshotAt(5), e.Snapshot()) {
		t.Fatal("alt screen should ignore the offset")
	}
	feed(e, "\x1b[?1049l")
	if n := e.ScrollbackLen(); n != 0 {
		t.Fatalf("ScrollbackLen after alt %d", n)
	}
}

func TestHistoryClearAndRing(t *testing.T) {
	e := New(10, 2, nil)
	feed(e, lines("x", 0, 5))
	feed(e, "\x1b[3J")
	if n := e.ScrollbackLen(); n != 0 {
		t.Fatalf("after ED 3: %d", n)
	}
	before := e.ScrollbackPushed()
	feed(e, lines("r", 0, historyMax+10))
	if n := e.ScrollbackLen(); n != historyMax {
		t.Fatalf("ScrollbackLen %d", n)
	}
	// The blank row 0 left by ED 3 goes first, then r0..r10008.
	if d := e.ScrollbackPushed() - before; d != historyMax+10 {
		t.Fatalf("pushed %d", d)
	}
	// The blank and r0..r8 dropped out.
	if got := row(e.SnapshotAt(historyMax), 0); got != "r9" {
		t.Fatalf("oldest %q", got)
	}
}

func TestColorQueryReplies(t *testing.T) {
	r := &replyBuf{}
	e := New(10, 2, r)
	for _, c := range []struct{ query, want string }{
		{"\x1b]10;?\x07", "\x1b]10;rgb:f4f4/f4f4/f6f6\x07"},
		{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0808/0909/0c0c\x1b\\"},
		{"\x1b]12;?\x07", "\x1b]12;rgb:ffff/ffff/ffff\x07"},
		{"\x1b]10;?;?\x1b\\", "\x1b]10;rgb:f4f4/f4f4/f6f6\x1b\\\x1b]11;rgb:0808/0909/0c0c\x1b\\"},
		{"\x1b]4;1;?\x07", "\x1b]4;1;rgb:ffff/6161/6161\x07"},
		{"\x1b]4;8;?;196;?\x1b\\", "\x1b]4;8;rgb:2424/2727/2828\x1b\\\x1b]4;196;rgb:ffff/0000/0000\x1b\\"},
		// A color set by the program is what it reads back.
		{"\x1b]11;#102030\x07\x1b]11;?\x07", "\x1b]11;rgb:1010/2020/3030\x07"},
		{"\x1b]111\x07\x1b]11;?\x07", "\x1b]11;rgb:0808/0909/0c0c\x07"},
	} {
		feed(e, c.query)
		r.exactly(t, c.want)
	}
}

// exactly fails unless the replies are exactly want within a second, then
// clears them.
func (r *replyBuf) exactly(t *testing.T, want string) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		r.mu.Lock()
		got = r.b.String()
		r.mu.Unlock()
		if len(got) >= len(want) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond) // anything extra would show up by now
	r.mu.Lock()
	defer r.mu.Unlock()
	if got = r.b.String(); got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
	r.b.Reset()
}

// TestHistoryMemory fills 10k lines of agent-like output: prose, styled
// markers, diffs with backgrounds, the odd emoji, and reports the heap cost.
func TestHistoryMemory(t *testing.T) {
	var b strings.Builder
	for i := range historyMax + 100 {
		switch i % 5 {
		case 0:
			fmt.Fprintf(&b, "\x1b[38;2;215;119;87m⏺\x1b[0m I'll read the file and check the error handling around line %d before changing it.\r\n", i)
		case 1:
			fmt.Fprintf(&b, "  \x1b[48;2;34;92;43m\x1b[38;2;255;255;255m+ \x1b[0m\x1b[48;2;34;92;43m	if err := conn.Send(msg); err != nil { return fmt.Errorf(\"send %d: %%w\", err) }\x1b[0m\r\n", i)
		case 2:
			fmt.Fprintf(&b, "  \x1b[2m⎿  Read 120 lines (ctrl+r to expand)\x1b[0m\r\n")
		case 3:
			fmt.Fprintf(&b, "\x1b[1mSummary\x1b[0m: tests pass ✅, \x1b[36mgo vet\x1b[0m is clean, and the diff touches %d files.\r\n", i%17)
		default:
			b.WriteString("\r\n")
		}
	}
	data := []byte(b.String())

	var m0, m1 runtime.MemStats
	e := New(120, 40, nil)
	runtime.GC()
	runtime.ReadMemStats(&m0)
	for len(data) > 0 { // the pane reads at most 32KB at a time
		n := min(len(data), 32*1024)
		_, _ = e.Write(data[:n])
		data = data[n:]
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	if n := e.ScrollbackLen(); n != historyMax {
		t.Fatalf("ScrollbackLen %d", n)
	}
	total := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	t.Logf("%d history lines at 120 cols: %d KB, %d bytes per line", historyMax, total/1024, total/historyMax)
	if total > 10<<20 {
		t.Fatalf("history costs %d bytes, want under 10MB", total)
	}
	runtime.KeepAlive(e)
}

// TestOSC8Links checks an OSC 8 hyperlink lands on its cells, both on the
// live screen and once the line scrolls into history, and stops at the
// closing sequence.
func TestOSC8Links(t *testing.T) {
	e := New(12, 2, nil)
	feed(e, "a \x1b]8;id=1;https://example.com/x\x1b\\docs\x1b]8;;\x1b\\ b")
	check := func(what string, g Grid) {
		for x, want := range []string{"", "", "https://example.com/x", "https://example.com/x", "https://example.com/x", "https://example.com/x", "", ""} {
			if got := g.At(x, 0).Link; got != want {
				t.Errorf("%s: cell %d link %q, want %q", what, x, got, want)
			}
		}
	}
	check("screen", e.Snapshot())
	feed(e, "\r\n\r\n")
	check("history", e.SnapshotAt(1))
}

// TestPromptMarks records OSC 133 marks on their rows, keeps them in
// history, and jumps between prompts from any scroll offset.
func TestPromptMarks(t *testing.T) {
	e := New(10, 3, nil)
	var b strings.Builder
	for i := range 5 { // the prompt on even rows 0-8, its output on odd ones
		fmt.Fprintf(&b, "\x1b]133;A\x07$ \x1b]133;B\x07c%d\r\n\x1b]133;C\x07o%d\r\n\x1b]133;D;0\x07", i, i)
	}
	g := feed(e, b.String()+"\x1b]133;A\x07$ ") // row 10, on screen with rows 8 and 9
	if row(g, 0) != "$ c4" || row(g, 2) != "$" {
		t.Fatalf("marks printed something: %q", screenText(g))
	}
	h := &e.(*emulator).st.hist
	if h.len() != 8 {
		t.Fatalf("history %d lines", h.len())
	}
	// D, the end of the command before, lands on the next prompt's row.
	for i, want := range []xvt.LineFlags{xvt.LinePrompt | xvt.LineInput | xvt.LineEnd, xvt.LineOutput} {
		if got := h.at(2 + i).flags; got != want {
			t.Errorf("history line %d flags %b, want %b", 2+i, got, want)
		}
	}

	p := e.(interface{ PromptOffset(off, n int) int })
	for _, tc := range []struct{ off, n, want int }{
		{0, 1, 2},  // the live screen's top is row 8; the prompt above is row 6
		{2, 1, 4},  // from row 6 to row 4
		{0, 3, 6},  // three back, row 2
		{3, 1, 4},  // from output row 5 back to row 4
		{3, -1, 2}, // and forward to row 6
		{8, 1, 8},  // nothing above row 0
		{8, -1, 6}, // row 0 to row 2
		{2, -1, 0}, // the next prompt, row 8, is on the live screen
		{0, -1, 0},
		{99, -2, 4}, // off is clamped to the history first
	} {
		if got := p.PromptOffset(tc.off, tc.n); got != tc.want {
			t.Errorf("PromptOffset(%d, %d) = %d, want %d", tc.off, tc.n, got, tc.want)
		}
	}
	if got := row(e.SnapshotAt(p.PromptOffset(0, 1)), 0); got != "$ c3" {
		t.Errorf("view after a jump starts with %q", got)
	}
	feed(e, "\x1b[?1049h")
	if got := p.PromptOffset(4, 1); got != 4 {
		t.Errorf("alt screen moved the view to %d", got)
	}
}
