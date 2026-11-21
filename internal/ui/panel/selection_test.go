package panel

import (
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// selRig drives a Transcript with real pointer and key events through a
// router, with a stand-in composer that takes the key focus back every
// frame as chatin.Composer does, and a fake clipboard.
type selRig struct {
	t      *testing.T
	tr     Transcript
	s      *session.Snapshot
	r      input.Router
	ops    op.Ops
	now    time.Time
	size   image.Point
	ed     widget.Editor // the composer
	copies []string
	copied []string    // what Copied told the window
	keys   []key.Event // key events the composer saw after the transcript
	// keysHere is whether the chat holds the keys: the window then lets
	// the transcript take Ctrl+C before the composer.
	keysHere bool
	scale    float32 // px per dp, 1 if 0
}

func newSelRig(t *testing.T, s *session.Snapshot) *selRig {
	g := &selRig{t: t, s: s, size: image.Pt(1000, 800)}
	g.start()
	return g
}

// start wires the fake clipboard and draws the first frames.
func (g *selRig) start() {
	g.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	g.tr.Copy = func(_ layout.Context, text string) { g.copies = append(g.copies, text) }
	g.tr.Copied = func(_ layout.Context, text string, _ image.Rectangle) { g.copied = append(g.copied, text) }
	g.tr.CopyOnSelect, g.keysHere = true, true
	if g.scale == 0 {
		g.scale = 1
	}
	g.frame()
	g.frame()
}

func (g *selRig) frame(evs ...any) {
	for _, e := range evs {
		switch e := e.(type) {
		case pointer.Event:
			g.r.Queue(e)
		case key.Event:
			g.r.Queue(e)
		case key.EditEvent:
			g.r.Queue(e)
		}
	}
	g.ops.Reset()
	g.now = g.now.Add(time.Millisecond)
	gtx := layout.Context{Ops: &g.ops, Metric: unit.Metric{PxPerDp: g.scale, PxPerSp: g.scale}, Constraints: layout.Exact(g.size), Now: g.now, Source: g.r.Source()}
	if g.keysHere {
		g.tr.CopyKey(gtx)
	}
	g.tr.Layout(gtx, theme.Dark(), g.s, false)
	// The composer: it takes the keys back whenever it lost them.
	if !gtx.Focused(&g.ed) {
		gtx.Execute(key.FocusCmd{Tag: &g.ed})
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &g.ed, Name: "C", Required: key.ModShortcut})
		if !ok {
			break
		}
		g.keys = append(g.keys, ev.(key.Event))
	}
	eg := gtx
	eg.Constraints = layout.Exact(image.Pt(400, 30))
	g.ed.Layout(eg, theme.Dark().Shaper, theme.Dark().UIFont, 14, op.CallOp{}, op.CallOp{})
	g.r.Frame(&g.ops)
}

// box is where key's text was drawn in the last frame, in the window.
func (g *selRig) box(key string) image.Rectangle {
	g.t.Helper()
	for sl, k := range g.tr.msg.keys {
		if k != key || !slices.Contains(g.tr.drawn, sl) {
			continue
		}
		tr, ok := g.r.Source().Transform(sl)
		if !ok {
			continue
		}
		sz := g.tr.span.size[sl]
		return image.Rectangle{Min: tr.Transform(f32.Point{}).Round(), Max: tr.Transform(f32.Pt(float32(sz.X), float32(sz.Y))).Round()}
	}
	g.t.Fatalf("no text %q drawn; drawn: %v", key, g.drawnKeys())
	return image.Rectangle{}
}

func (g *selRig) drawnKeys() []string {
	var out []string
	for _, sl := range g.tr.drawn {
		out = append(out, g.tr.msg.keys[sl])
	}
	return out
}

// middle is the drawn text nearest the middle of the view.
func (g *selRig) middle() string {
	g.t.Helper()
	best, d := "", 1<<30
	for _, k := range g.drawnKeys() {
		r := g.box(k)
		if c := (r.Min.Y+r.Max.Y)/2 - g.size.Y/2; r.Min.Y >= 0 && r.Max.Y <= g.size.Y && max(c, -c) < d {
			best, d = k, max(c, -c)
		}
	}
	return best
}

func pt(p image.Point) f32.Point { return f32.Pt(float32(p.X), float32(p.Y)) }

func (g *selRig) press(p image.Point, mods key.Modifiers) {
	g.frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(p), Modifiers: mods})
}
func (g *selRig) move(p image.Point) {
	g.frame(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(p)})
}
func (g *selRig) release(p image.Point) {
	g.frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pt(p)})
}

// drag presses at a, a second after the last event, moves to b in steps,
// and releases there.
func (g *selRig) drag(a, b image.Point) {
	g.now = g.now.Add(time.Second) // not a double click on the last press
	g.press(a, 0)
	for i := 1; i <= 4; i++ {
		g.move(a.Add(b.Sub(a).Mul(i).Div(4)))
	}
	g.release(b)
	g.frame()
}

func tid(id string) *string { return &id }

func chatFixture() *session.Snapshot {
	return &session.Snapshot{Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Explain the bug", TurnID: tid("t1")},
		{ID: "c1", Kind: session.KindToolCall, TurnID: tid("t1"), Tool: &session.ToolCall{ToolName: "Bash", Command: "go test ./internal/vt", Status: "completed", Output: "ok vt 0.2s"}},
		{ID: "a1", Kind: session.KindAssistantText, TurnID: tid("t1"), Text: "The cursor moves past the edge.\n\n```go\nx := 1\ny := 2\n```\n\nThen it wraps."},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "Fix it please", TurnID: tid("t2")},
		{ID: "a2", Kind: session.KindAssistantText, TurnID: tid("t2"), Text: "Fixed in vt.go and tested."},
	}}
}

// sel is key's selectable as the last frame drew it.
func (g *selRig) sel(key string) *widget.Selectable {
	g.t.Helper()
	sl := g.tr.sels[key]
	if sl == nil {
		g.t.Fatalf("no selectable %q", key)
	}
	return sl
}

// painted is the text key paints selected.
func (g *selRig) painted(key string) string {
	sl := g.sel(key)
	if !sl.Focused() && !sl.PaintSelectionWhenUnfocused {
		return "" // Gio paints no selection
	}
	return sl.SelectedText()
}

func left(r image.Rectangle) image.Point  { return image.Pt(r.Min.X+1, (r.Min.Y+r.Max.Y)/2) }
func right(r image.Rectangle) image.Point { return image.Pt(r.Max.X-1, (r.Min.Y+r.Max.Y)/2) }

// The cause: a press on a block took the key focus, the composer took it
// back the same frame, and Gio paints no selection on an unfocused
// selectable. Now a drag selects and paints with the composer keeping the
// keys, and copies on release.
func TestSelectWhileComposerFocused(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("a2/0")
	g.drag(left(r), right(r))
	if got := g.painted("a2/0"); got != "Fixed in vt.go and tested." {
		t.Errorf("painted %q", got)
	}
	if !g.r.Source().Focused(&g.ed) {
		t.Error("the composer lost the keys")
	}
	if len(g.copies) != 1 || g.copies[0] != "Fixed in vt.go and tested." || len(g.copied) != 1 {
		t.Errorf("copied %q, notices %q", g.copies, g.copied)
	}
}

// Typing after a selection goes to the composer: no key is lost.
func TestSelectThenType(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("a2/0")
	g.drag(left(r), right(r))
	g.frame(key.Event{Name: "A", State: key.Press}, key.EditEvent{Text: "a"})
	g.frame(key.EditEvent{Range: key.Range{Start: 1, End: 1}, Text: "b"})
	if g.ed.Text() != "ab" {
		t.Errorf("composer has %q", g.ed.Text())
	}
	if g.painted("a2/0") == "" {
		t.Error("typing dropped the selection")
	}
}

// A drag from one message into the next selects all between, in reading
// order, and copies it a line per block; code keeps its exact text.
func TestSelectAcrossMessages(t *testing.T) {
	g := newSelRig(t, chatFixture())
	from, to := g.box("a1/0"), g.box("a2/0")
	start := image.Pt(from.Min.X+(from.Dx()*4)/10, (from.Min.Y+from.Max.Y)/2) // in "cursor"
	end := image.Pt(to.Min.X+to.Dx()/3, (to.Min.Y+to.Max.Y)/2)
	g.drag(start, end)
	if len(g.copies) != 1 {
		t.Fatalf("copies %q", g.copies)
	}
	got := g.copies[0]
	want := []string{"x := 1\ny := 2", "Then it wraps.", "Fix it please"}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("copy %q lacks %q", got, w)
		}
	}
	if first, _, _ := strings.Cut(got, "\n"); first == "The cursor moves past the edge." || !strings.HasSuffix("The cursor moves past the edge.", first) {
		t.Errorf("copy %q does not start at the press", got)
	}
	if !strings.Contains(got, "wraps.\nFix it please\nFixed") {
		t.Errorf("blocks not a line apart in order: %q", got)
	}
	for _, k := range []string{"a1/1", "a1/2", "p2"} {
		if g.painted(k) != g.sel(k).Text() {
			t.Errorf("%s painted %q of %q", k, g.painted(k), g.sel(k).Text())
		}
	}
	// Dragging back up selects the other way, from the same press.
	g.drag(end, start)
	if got2 := g.copies[1]; got2 != got {
		t.Errorf("upward drag copied %q, downward %q", got2, got)
	}
}

// A press on what is drawn over text, the find bar here, does not reach
// the text under it: no selection starts there.
func TestSelectUnderOverlay(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("p1")
	a, b := right(r), left(r)
	g.tr.OpenFind()
	g.frame()
	g.frame()
	f := g.tr.msg.find
	if !f.on {
		t.Fatal("the find bar did not open")
	}
	g.drag(a, b)
	if len(g.copies) != 0 || g.tr.span.has() {
		t.Fatalf("a drag on the find bar over the prompt selected it: %q", g.copies)
	}
	g.frame(key.Event{Name: key.NameEscape, State: key.Press})
	g.tr.msg.find.on = false
	g.frame()
	g.drag(a, b)
	if len(g.copies) != 1 || g.copies[0] != "Explain the bug" {
		t.Fatalf("with the find bar shut, the drag copied %q", g.copies)
	}
}

// A finger's drag on a touch screen scrolls, as it did: it selects
// nothing and the row under it does not open.
func TestSelectTouchScrolls(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("a2/0")
	a, b := left(r), right(r)
	g.frame(pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: pt(a)})
	g.frame(pointer.Event{Kind: pointer.Move, Source: pointer.Touch, Buttons: pointer.ButtonPrimary, Position: pt(b)})
	g.frame(pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: pt(b)})
	g.frame()
	if g.tr.span.has() || len(g.copies) != 0 {
		t.Errorf("a touch drag selected: %q", g.copies)
	}
}

// A press with no drag clears the selection; a press on the background
// clears it too; a button keeps it.
func TestSelectClear(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("a2/0")
	g.drag(left(r), right(r))
	p := left(g.box("a1/0"))
	g.press(p, 0)
	g.release(p)
	g.frame()
	if g.tr.span.has() || g.painted("a2/0") != "" {
		t.Error("a click on text kept the selection")
	}
	g.drag(left(r), right(r))
	g.press(image.Pt(500, 790), 0) // the empty space below the rows
	g.release(image.Pt(500, 790))
	g.frame()
	if g.tr.span.has() {
		t.Error("a press on the background kept the selection")
	}
}

// Double-click selects a word, triple-click a line.
func TestSelectWordAndLine(t *testing.T) {
	g := newSelRig(t, chatFixture())
	r := g.box("a2/0")
	p := image.Pt(r.Min.X+r.Dx()/10, (r.Min.Y+r.Max.Y)/2) // in "Fixed"
	g.press(p, 0)
	g.release(p)
	g.press(p, 0)
	g.release(p)
	g.frame()
	if got := g.painted("a2/0"); got != "Fixed" {
		t.Errorf("double-click selected %q", got)
	}
	if n := len(g.copies); n == 0 || g.copies[n-1] != "Fixed" {
		t.Errorf("double-click copied %q", g.copies)
	}
	g.press(p, 0)
	g.release(p)
	g.frame()
	if got := g.painted("a2/0"); got != "Fixed in vt.go and tested." {
		t.Errorf("triple-click selected %q", got)
	}
	// A slow second press is a new single click.
	g.now = g.now.Add(time.Second)
	g.press(p, 0)
	g.release(p)
	g.frame()
	if g.tr.span.has() {
		t.Error("a slow press made a double click")
	}
}

// Shift+click extends the selection to the press.
func TestSelectShiftClick(t *testing.T) {
	g := newSelRig(t, chatFixture())
	g.press(left(g.box("a1/2")), 0)
	g.release(left(g.box("a1/2")))
	g.press(right(g.box("a2/0")), key.ModShift)
	g.release(right(g.box("a2/0")))
	g.frame()
	if n := len(g.copies); n == 0 || g.copies[n-1] != "Then it wraps.\nFix it please\nFixed in vt.go and tested." {
		t.Errorf("shift-click copied %q", g.copies)
	}
}

// Ctrl+C copies a selection while the chat holds the keys, and is left
// to the composer without one or when another pane holds them.
func TestSelectCtrlC(t *testing.T) {
	g := newSelRig(t, chatFixture())
	g.tr.CopyOnSelect = false
	ctrlC := key.Event{Name: "C", Modifiers: key.ModShortcut, State: key.Press}
	g.frame(ctrlC)
	if len(g.copies) != 0 || len(g.keys) != 1 {
		t.Fatalf("no selection: transcript copied %q, composer saw %d", g.copies, len(g.keys))
	}
	r := g.box("a2/0")
	g.drag(left(r), right(r))
	if len(g.copies) != 0 {
		t.Fatalf("copy_on_select off copied %q", g.copies)
	}
	g.frame(ctrlC)
	if len(g.copies) != 1 || g.copies[0] != "Fixed in vt.go and tested." || len(g.keys) != 1 {
		t.Errorf("Ctrl+C copied %q, composer saw %d", g.copies, len(g.keys))
	}
	g.keysHere = false
	g.frame(ctrlC)
	if len(g.copies) != 1 || len(g.keys) != 2 {
		t.Errorf("keys elsewhere: transcript copied %q, composer saw %d", g.copies, len(g.keys))
	}
}

// A right-click on a selection offers Copy; Esc or a press elsewhere
// closes it.
func TestSelectMenu(t *testing.T) {
	g := newSelRig(t, chatFixture())
	g.tr.CopyOnSelect = false
	r := g.box("a2/0")
	g.drag(left(r), right(r))
	at := pt(left(r).Add(image.Pt(20, 0)))
	g.frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: at})
	g.frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	if !g.tr.span.menu {
		t.Fatal("no menu on a right-click")
	}
	g.frame(key.Event{Name: key.NameEscape, State: key.Press})
	if g.tr.span.menu || !g.tr.span.has() {
		t.Fatal("Esc did not close the menu, or dropped the selection")
	}
	g.frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: at})
	g.frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	m := g.tr.span.menuR.Min.Add(image.Pt(20, 15))
	g.press(m, 0)
	g.release(m)
	g.frame()
	if len(g.copies) != 1 || g.copies[0] != "Fixed in vt.go and tested." || g.tr.span.menu {
		t.Errorf("menu Copy copied %q, open %v", g.copies, g.tr.span.menu)
	}
	if !g.tr.span.has() {
		t.Error("menu Copy dropped the selection")
	}
}

// An open work log's command and output are selectable; a click on the
// row still folds it, a drag on it selects and leaves it open.
func TestSelectToolRow(t *testing.T) {
	g := newSelRig(t, chatFixture())
	g.tr.open[logID(g.s, g.tr.blocks[1])] = true
	g.frame()
	g.frame()
	var cmd string
	for _, k := range g.drawnKeys() {
		if strings.HasPrefix(k, "cmd:") {
			cmd = k
		}
	}
	if cmd == "" {
		t.Fatalf("the command is not selectable: %v", g.drawnKeys())
	}
	r := g.box(cmd)
	shut := isOpen(g.tr.open, "c1", false)
	g.drag(left(r), right(r))
	if n := len(g.copies); n != 1 || g.copies[0] != "go test ./internal/vt" {
		t.Errorf("drag on the command copied %q", g.copies)
	}
	if !slices.Contains(g.drawnKeys(), cmd) || isOpen(g.tr.open, "c1", false) != shut {
		t.Error("the drag folded or opened a row")
	}
	// A click on the command opens the row's output, as it did.
	g.press(left(r), 0)
	g.release(left(r))
	g.frame()
	if isOpen(g.tr.open, "c1", false) == shut {
		t.Error("a click on the command did not open the row")
	}
}

// The selection holds while the reply streams more text and new items
// come, and a held button keeps the view from following the newest line.
func TestSelectWhileStreaming(t *testing.T) {
	s := chatFixture()
	longer(s, 20) // make it overflow, so it follows
	g := newSelRig(t, s)
	for i := 0; i < 3; i++ {
		g.frame()
	}
	k := g.middle()
	r := g.box(k)
	g.press(left(r), 0)
	g.move(left(r).Add(image.Pt(30, 0)))
	g.move(right(r))
	off := g.tr.list.Position
	n := len(g.s.Content)
	g.s.Content[n-1].Text += " and more streams in"
	g.s.Content = append(g.s.Content, session.Item{ID: "new", Kind: session.KindAssistantText, TurnID: g.s.Content[n-1].TurnID, Text: "a new paragraph\n\nand another\n\nand more"})
	g.frame()
	g.frame()
	if g.tr.list.Position.First != off.First || g.tr.list.Position.Offset != off.Offset {
		t.Errorf("the view followed while the button was held: %+v then %+v", off, g.tr.list.Position)
	}
	g.release(right(r))
	g.frame()
	if g.painted(k) == "" || len(g.copies) != 1 {
		t.Errorf("the selection did not survive new items: painted %q, copies %q", g.painted(k), g.copies)
	}
}

// A drag held past the bottom edge scrolls the transcript and selects
// what scrolls in; past the top, the other way.
func TestSelectAutoScroll(t *testing.T) {
	s := chatFixture()
	longer(s, 20)
	g := newSelRig(t, s)
	for i := 0; i < 10; i++ { // the wheel, to the top
		g.frame(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Scroll: f32.Pt(0, -1000), Position: f32.Pt(500, 400)})
	}
	g.frame()
	r := g.box("a1/0")
	g.press(left(r), 0)
	g.move(left(r).Add(image.Pt(0, 40)))
	g.move(image.Pt(500, g.size.Y+60)) // below the bottom edge
	first := g.tr.list.Position.First
	for i := 0; i < 30; i++ {
		g.frame()
	}
	if g.tr.list.Position.First <= first {
		t.Fatalf("no scroll past the bottom: first %d then %d", first, g.tr.list.Position.First)
	}
	g.release(image.Pt(500, g.size.Y+60))
	if len(g.copies) != 1 || !strings.Contains(g.copies[0], "Fix it please") || !strings.Contains(g.copies[0], "line of the long reply") {
		t.Errorf("auto-scroll selection copied %q", g.copies)
	}
	// Past the top, it scrolls back up.
	k := g.middle()
	g.now = g.now.Add(time.Second)
	g.press(left(g.box(k)), 0)
	g.move(left(g.box(k)).Add(image.Pt(0, -40)))
	g.move(image.Pt(500, -60))
	first = g.tr.list.Position.First
	for i := 0; i < 30; i++ {
		g.frame()
	}
	if g.tr.list.Position.First >= first {
		t.Fatalf("no scroll past the top: first %d then %d", first, g.tr.list.Position.First)
	}
	g.release(image.Pt(500, -60))
	if len(g.copies) != 2 || strings.Count(g.copies[1], "\n") < 4 {
		t.Errorf("upward auto-scroll copied %q", g.copies[1:])
	}
}

// longer adds n turns of a prompt and a reply to s.
func longer(s *session.Snapshot, n int) {
	for i := 0; i < n; i++ {
		turn := fmt.Sprint("l", i)
		s.Content = append(s.Content,
			session.Item{ID: "lp" + turn, Kind: session.KindUserPrompt, Text: "and then?", TurnID: tid(turn)},
			session.Item{ID: "la" + turn, Kind: session.KindAssistantText, TurnID: tid(turn), Text: "line of the long reply"})
	}
}

func TestWordAndLineAt(t *testing.T) {
	for _, c := range []struct {
		txt    string
		i      int
		wa, wb int
		la, lb int
	}{
		{"go test ./...", 1, 0, 2, 0, 13},
		{"go test ./...", 2, 0, 2, 0, 13}, // just after a word: the word
		{"a  b", 2, 2, 3, 0, 4},           // a space alone
		{"x := 1\ny := 2", 8, 7, 8, 7, 13},
		{"héllo wörld", 9, 6, 11, 0, 11},
		{"", 0, 0, 0, 0, 0},
	} {
		if a, b := wordAt(c.txt, c.i); a != c.wa || b != c.wb {
			t.Errorf("wordAt(%q, %d) = %d,%d, want %d,%d", c.txt, c.i, a, b, c.wa, c.wb)
		}
		if a, b := lineAt(c.txt, c.i); a != c.la || b != c.lb {
			t.Errorf("lineAt(%q, %d) = %d,%d, want %d,%d", c.txt, c.i, a, b, c.la, c.lb)
		}
	}
}

// Over a reply's markdown, the copy is the text shown: a heading, list
// items and a link's words, a table as tab-apart cells a line per row,
// and code with its tabs, as typed.
func TestSelectMarkdown(t *testing.T) {
	g := newSelRig(t, richChat())
	g.size = image.Pt(1000, 1200)
	g.frame()
	g.frame()
	from, to := g.box("a1/0"), g.box("a2/0")
	g.drag(left(from), right(to))
	if len(g.copies) != 1 {
		t.Fatalf("copies %q", g.copies)
	}
	got := g.copies[0]
	for _, w := range []string{
		"The cause\nThe check compared against w-1",
		"See the spec.\nwrap now compares with w\nthe test covers column 80\n",
		"Width\tBefore\tAfter\n80\t79\t80\n132\t131\t132\n",
		"func wrap(x, w int) bool {\n\treturn x >= w\n}\nThe test passes now.\nGreat, commit it\nCommitted as fix(vt): wrap at the last column.",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("copy lacks %q:\n%s", w, got)
		}
	}
	// A drag that ends in a table's top half leaves it out; its bottom
	// half takes it whole.
	var table string
	for _, k := range g.drawnKeys() {
		if g.tr.span.whole[g.sel(k)] {
			table = k
		}
	}
	if table == "" {
		t.Fatal("no table drawn as a whole block")
	}
	tb := g.box(table)
	g.drag(left(from), image.Pt(tb.Min.X+10, tb.Min.Y+5))
	if c := g.copies[len(g.copies)-1]; strings.Contains(c, "Width") {
		t.Errorf("a drag into the table's top took it: %q", c)
	}
	g.drag(left(from), image.Pt(tb.Min.X+10, tb.Max.Y-5))
	if c := g.copies[len(g.copies)-1]; !strings.HasSuffix(c, "132\t131\t132") {
		t.Errorf("a drag into the table's bottom: %q", c)
	}
}

// The code card's Copy button copies the tabs too.
func TestCodeCopyKeepsTabs(t *testing.T) {
	if got := tabbed("a\tb", "a    b").typed(0, 6); got != "a\tb" {
		t.Errorf("all: %q", got)
	}
	if got := tabbed("a\tb", "a    b").typed(2, 6); got != "\tb" {
		t.Errorf("from inside the tab: %q", got)
	}
	if got := tabbed("x\n\ty\nz", "x\n    y").typed(0, 7); got != "x\n\ty" {
		t.Errorf("a cut block: %q", got)
	}
	if ct := tabbed("no tabs", "no tabs"); ct.orig != nil {
		t.Error("text without tabs mapped")
	}
}
