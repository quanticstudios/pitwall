package vt

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// replyBuf is a reply writer the pump goroutine and the test can share.
type replyBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (r *replyBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.Write(p)
}

// waitFor fails the test unless want shows up in the replies within a second,
// then clears them.
func (r *replyBuf) waitFor(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		r.mu.Lock()
		got := r.b.String()
		if strings.Contains(got, want) {
			r.b.Reset()
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("reply %q not seen; got %q", want, r.b.String())
}

func row(g Grid, y int) string {
	var s strings.Builder
	for x := range g.Cols {
		s.WriteString(g.At(x, y).Content)
	}
	return strings.TrimRight(s.String(), " ")
}

func screenText(g Grid) string {
	var s strings.Builder
	for y := range g.Rows {
		s.WriteString(row(g, y))
		s.WriteByte('\n')
	}
	return s.String()
}

func feed(e Emulator, s string) Grid {
	_, _ = e.Write([]byte(s))
	return e.Snapshot()
}

func TestTextAndCursor(t *testing.T) {
	e := New(10, 3, nil)
	g := feed(e, "hello\r\nworld")
	if row(g, 0) != "hello" || row(g, 1) != "world" {
		t.Fatalf("rows %q %q", row(g, 0), row(g, 1))
	}
	if g.Cursor.X != 5 || g.Cursor.Y != 1 || !g.Cursor.Visible || g.Cursor.Shape != CursorBlock {
		t.Fatalf("cursor %+v", g.Cursor)
	}
	if len(g.Cells) != 30 || g.Cols != 10 || g.Rows != 3 {
		t.Fatalf("grid %dx%d len %d", g.Cols, g.Rows, len(g.Cells))
	}
}

func TestColorsAndAttrs(t *testing.T) {
	e := New(10, 1, nil)
	g := feed(e, "\x1b[31ma\x1b[91mb\x1b[38;5;200mc\x1b[38;2;1;2;3;48;2;4;5;6md\x1b[0;1;3;4;7;9me\x1b[0mf")
	want := []struct {
		fg, bg Color
		attrs  Attr
	}{
		{PaletteFlag | 1, 0, 0},
		{PaletteFlag | 9, 0, 0},
		{PaletteFlag | 200, 0, 0},
		{RGBFlag | 0x010203, RGBFlag | 0x040506, 0},
		{0, 0, Bold | Italic | Underline | Reverse | Strike},
		{0, 0, 0},
	}
	for i, w := range want {
		c := g.At(i, 0)
		if c.FG != w.fg || c.BG != w.bg || c.Attrs != w.attrs {
			t.Errorf("cell %d %q: fg %#x bg %#x attrs %#x, want %#x %#x %#x", i, c.Content, c.FG, c.BG, c.Attrs, w.fg, w.bg, w.attrs)
		}
	}
}

func TestWideAndGraphemes(t *testing.T) {
	e := New(10, 1, nil)
	// CJK, a skin-tone emoji, and a ZWJ family each take two cells.
	g := feed(e, "中👍🏽👨‍👩‍👧x")
	for i, want := range []string{"中", "👍🏽", "👨‍👩‍👧"} {
		lead, trail := g.At(2*i, 0), g.At(2*i+1, 0)
		if lead.Content != want || lead.Width != 2 || trail.Content != "" || trail.Width != 0 {
			t.Errorf("pair %d: lead %+v trail %+v", i, lead, trail)
		}
	}
	if c := g.At(6, 0); c.Content != "x" || c.Width != 1 {
		t.Errorf("after wide: %+v", c)
	}
	if g.Cursor.X != 7 {
		t.Errorf("cursor x %d", g.Cursor.X)
	}
}

func TestAltScreen(t *testing.T) {
	e := New(10, 2, nil)
	feed(e, "main")
	g := feed(e, "\x1b[?1049halt")
	if !g.AltScreen || row(g, 0) != "alt" {
		t.Fatalf("alt: %v %q", g.AltScreen, row(g, 0))
	}
	g = feed(e, "\x1b[?1049l")
	if g.AltScreen || row(g, 0) != "main" || g.Cursor.X != 4 {
		t.Fatalf("back: %v %q %+v", g.AltScreen, row(g, 0), g.Cursor)
	}
}

func TestScrollRegion(t *testing.T) {
	e := New(5, 4, nil)
	feed(e, "a\r\nb\r\nc\r\nd")
	// Region rows 2-3; a LF at its bottom scrolls only those rows.
	g := feed(e, "\x1b[2;3r\x1b[3;1H\nX")
	if got := screenText(g); got != "a\nc\nX\nd\n" {
		t.Fatalf("screen %q", got)
	}
}

func TestTitleAndCursorStyle(t *testing.T) {
	e := New(5, 1, nil)
	if g := feed(e, "\x1b]0;one\x07"); g.Title != "one" {
		t.Fatalf("title %q", g.Title)
	}
	if g := feed(e, "\x1b]2;two\x1b\\"); g.Title != "two" {
		t.Fatalf("title %q", g.Title)
	}
	for seq, want := range map[string]CursorShape{"\x1b[5 q": CursorBar, "\x1b[4 q": CursorUnderline, "\x1b[2 q": CursorBlock} {
		if g := feed(e, seq); g.Cursor.Shape != want {
			t.Errorf("%q: shape %d want %d", seq, g.Cursor.Shape, want)
		}
	}
	if g := feed(e, "\x1b[?25l"); g.Cursor.Visible {
		t.Error("cursor visible after ?25l")
	}
	if g := feed(e, "\x1b[?25h"); !g.Cursor.Visible {
		t.Error("cursor hidden after ?25h")
	}
}

func TestModes(t *testing.T) {
	e := New(5, 1, nil)
	if m := e.Modes(); m != (Modes{}) {
		t.Fatalf("initial modes %+v", m)
	}
	feed(e, "\x1b[?1h\x1b=\x1b[?2004h\x1b[?1004h\x1b[?1000h\x1b[?1006h")
	want := Modes{AppCursorKeys: true, AppKeypad: true, BracketedPaste: true, FocusEvents: true, Mouse: MouseNormal, MouseSGR: true}
	if m := e.Modes(); m != want {
		t.Fatalf("modes %+v, want %+v", m, want)
	}
	feed(e, "\x1b[?1003h")
	if m := e.Modes(); m.Mouse != MouseAny {
		t.Fatalf("mouse %d", m.Mouse)
	}
	feed(e, "\x1b[?1003l\x1b[?1000l\x1b[?1l\x1b>\x1b[?2004l")
	if m := e.Modes(); m.Mouse != MouseOff || m.AppCursorKeys || m.AppKeypad || m.BracketedPaste {
		t.Fatalf("after reset %+v", m)
	}
}

func TestReplies(t *testing.T) {
	r := &replyBuf{}
	e := New(10, 5, r)
	feed(e, "\x1b[c")
	r.waitFor(t, "\x1b[?62;1;6;22c")
	feed(e, "\x1b[>c")
	r.waitFor(t, "\x1b[>1;10;0c")
	feed(e, "\x1b[3;4H\x1b[6n")
	r.waitFor(t, "\x1b[3;4R")
	feed(e, "\x1b[5n")
	r.waitFor(t, "\x1b[0n")
	feed(e, "\x1b[?2026$p")
	r.waitFor(t, "\x1b[?2026;2$y")
	feed(e, "\x1b[?2004h\x1b[?2004$p")
	r.waitFor(t, "\x1b[?2004;1$y")
}

func TestKittyKeyboard(t *testing.T) {
	r := &replyBuf{}
	e := New(10, 5, r)
	feed(e, "\x1b[?u")
	r.waitFor(t, "\x1b[?0u")
	feed(e, "\x1b[>1u\x1b[>5u\x1b[?u")
	r.waitFor(t, "\x1b[?5u")
	if m := e.Modes(); m.KittyKeyboard != 5 {
		t.Fatalf("flags %d", m.KittyKeyboard)
	}
	feed(e, "\x1b[=2;3u") // clear bit 2
	if m := e.Modes(); m.KittyKeyboard != 1|4 {
		t.Fatalf("after =2;3 flags %d", m.KittyKeyboard)
	}
	// The alt screen has its own stack.
	feed(e, "\x1b[?1049h\x1b[?u")
	r.waitFor(t, "\x1b[?0u")
	feed(e, "\x1b[>3u\x1b[?1049l")
	if m := e.Modes(); m.KittyKeyboard != 5 {
		t.Fatalf("main flags after alt %d", m.KittyKeyboard)
	}
	feed(e, "\x1b[<u")
	if m := e.Modes(); m.KittyKeyboard != 1 {
		t.Fatalf("after pop %d", m.KittyKeyboard)
	}
	feed(e, "\x1b[<5u")
	if m := e.Modes(); m.KittyKeyboard != 0 {
		t.Fatalf("after over-pop %d", m.KittyKeyboard)
	}
}

func TestSynchronizedOutput(t *testing.T) {
	e := New(10, 1, nil)
	feed(e, "old")
	if g := feed(e, "\x1b[?2026h\r\x1b[Knew"); row(g, 0) != "old" {
		t.Fatalf("mid-frame snapshot %q, want old frame", row(g, 0))
	}
	if g := feed(e, "\x1b[?2026l"); row(g, 0) != "new" {
		t.Fatalf("after frame %q", row(g, 0))
	}
}

func TestClaudeStartup(t *testing.T) {
	data, err := os.ReadFile("testdata/claude.raw")
	if err != nil {
		t.Fatal(err)
	}
	e := New(120, 40, nil)
	g := feed(e, string(data))
	text := screenText(g)
	for _, want := range []string{"Accessing workspace:", "Yes, I trust this folder", "Security guide"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	// Claude pushed kitty flags 5 and popped them on exit.
	if m := e.Modes(); m != (Modes{}) {
		t.Errorf("modes after exit %+v", m)
	}
}

func TestCodexStartup(t *testing.T) {
	data, err := os.ReadFile("testdata/codex.raw")
	if err != nil {
		t.Fatal(err)
	}
	r := &replyBuf{}
	e := New(120, 40, r)
	g := feed(e, string(data))
	if !g.AltScreen {
		t.Error("codex should be on the alt screen")
	}
	if !strings.Contains(screenText(g), "OpenAI Codex") {
		t.Errorf("missing banner in\n%s", screenText(g))
	}
	if !strings.HasSuffix(g.Title, " cap") {
		t.Errorf("title %q", g.Title)
	}
	want := Modes{BracketedPaste: true, FocusEvents: true, Mouse: MouseAny, MouseSGR: true, KittyKeyboard: 5}
	if m := e.Modes(); m != want {
		t.Errorf("modes %+v, want %+v", m, want)
	}
	// Codex pushes flags 5, then probes with the kitty query and DA1.
	r.waitFor(t, "\x1b[?5u\x1b[?62;1;6;22c")
}

func TestConcurrentSnapshot(t *testing.T) {
	data, err := os.ReadFile("testdata/codex.raw")
	if err != nil {
		t.Fatal(err)
	}
	e := New(120, 40, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			_, _ = e.Write(data)
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
			g := e.Snapshot()
			_ = e.Modes()
			if len(g.Cells) != g.Cols*g.Rows {
				t.Fatalf("bad grid %dx%d len %d", g.Cols, g.Rows, len(g.Cells))
			}
		}
	}
}

// BenchmarkWrite feeds about 1MB of recorded claude and codex output per op.
func BenchmarkWrite(b *testing.B) {
	var chunk []byte
	for _, f := range []string{"testdata/claude.raw", "testdata/codex.raw"} {
		d, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		chunk = append(chunk, d...)
	}
	data := bytes.Repeat(chunk, 1<<20/len(chunk)+1)
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		e := New(120, 40, nil)
		_, _ = e.Write(data)
	}
}

func TestDroppedEmulatorStopsReplyPump(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 50 {
		New(10, 5, nil).Write([]byte("\x1b[c"))
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+5 {
			return
		}
	}
	t.Fatalf("goroutines: %d before, %d after", before, runtime.NumGoroutine())
}

// stuckWriter never returns from Write until released, like a PTY whose
// child never reads its input.
type stuckWriter chan struct{}

func (w stuckWriter) Write(p []byte) (int, error) { <-w; return len(p), nil }

func TestReplyFloodIsCapped(t *testing.T) {
	w := make(stuckWriter)
	defer close(w)
	e := New(10, 1, w).(*emulator)
	const queries = 1 << 17
	_, _ = e.Write([]byte(strings.Repeat("\x1b[c", queries)))
	total := uint64(queries * len("\x1b[?62;1;6;22c"))
	// Kept: the one read the stuck writer took, plus at most replyCap pending.
	for deadline := time.Now().Add(2 * time.Second); total-e.st.dropped.Load() > replyCap+4096; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("kept %d of %d reply bytes, cap %d", total-e.st.dropped.Load(), total, replyCap)
		}
	}
}
