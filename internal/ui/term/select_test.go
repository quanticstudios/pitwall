package term

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// pane drives a View through a real router, one frame at a time.
type pane struct {
	t   *testing.T
	r   input.Router
	v   *View
	now time.Time
}

func newPane(t *testing.T) *pane {
	p := &pane{t: t, v: new(View), now: time.Unix(1e6, 0)}
	p.frame(grid("x"))
	p.frame(grid("x"))
	return p
}

// frame lays out g after queuing evs and returns the program's input.
func (p *pane) frame(g *vt.Grid, evs ...event.Event) string {
	p.r.Queue(evs...)
	gtx := testContext(image.Pt(400, 300))
	gtx.Source, gtx.Now = p.r.Source(), p.now
	_, in, _, _ := p.v.Layout(gtx, testTheme(), g, vt.Modes{}, true)
	p.r.Frame(gtx.Ops)
	return string(in)
}

// at is the middle of cell (x, y); y may be past the grid.
func (p *pane) at(x, y int) f32.Point {
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	return f32.Pt(pad+(float32(x)+0.5)*float32(p.v.cell.X), pad+(float32(y)+0.5)*float32(p.v.cell.Y))
}

func (p *pane) press(x, y int, mods key.Modifiers) pointer.Event {
	p.now = p.now.Add(time.Second)
	return pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: p.at(x, y), Modifiers: mods, Time: time.Duration(p.now.UnixNano())}
}

func (p *pane) move(x, y int) pointer.Event {
	return pointer.Event{Kind: pointer.Move, Buttons: pointer.ButtonPrimary, Position: p.at(x, y)}
}

func (p *pane) release(x, y int) pointer.Event {
	return pointer.Event{Kind: pointer.Release, Position: p.at(x, y), Time: time.Duration(p.now.UnixNano())}
}

// copyKey presses the copy key and returns what the view asked to copy.
func (p *pane) copyKey(g *vt.Grid) (Copy, bool) {
	p.frame(g, key.Event{Name: "C", Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
	return p.v.Copied()
}

// wrapped is grid with rows ending in "\" soft-wrapping into the next.
func wrapped(rows ...string) *vt.Grid {
	ws := make([]bool, len(rows))
	for i, r := range rows {
		rows[i], ws[i] = strings.CutSuffix(r, `\`)
	}
	g := grid(rows...)
	g.Wrapped = ws
	return g
}

// TestAnchoredSelection checks a selection stays on its text as output
// scrolls the screen and as the view scrolls back, is dropped once its
// text leaves history, and is dropped by a rewrap.
func TestAnchoredSelection(t *testing.T) {
	p := newPane(t)
	g := grid("aaaa", "bbbb", "cccc", "dddd", "eeee")
	p.v.SetScroll(0, 10, 100) // the top row is line 100
	p.frame(g, p.press(0, 1, 0), p.move(3, 2), p.release(3, 2))
	if c, ok := p.copyKey(g); !ok || c.Text != "bbbb\ncccc" || !c.Whole {
		t.Fatalf("copy %+v %v", c, ok)
	}

	// Two lines of output: line 101 is now above the screen.
	g = grid("cccc", "dddd", "eeee", "ffff", "gggg")
	p.v.SetScroll(0, 12, 102)
	c, ok := p.copyKey(g)
	if !ok || c.Text != "cccc" || c.Whole || c.Sel.A.Line != 101 || c.Sel.B.Line != 102 {
		t.Fatalf("after output: %+v %v", c, ok)
	}
	if s0, s1 := c.Sel.Cols(p.v.top, 4); s0 != 0 || s1 != 4 {
		t.Errorf("row 0 shows cols %d-%d selected", s0, s1)
	}

	// Scrolled back two lines, the view shows it all again.
	g = grid("aaaa", "bbbb", "cccc", "dddd", "eeee")
	p.v.SetScroll(2, 12, 102)
	if c, ok := p.copyKey(g); !ok || c.Text != "bbbb\ncccc" || !c.Whole {
		t.Errorf("scrolled back: %+v %v", c, ok)
	}

	// History keeps 10 lines: the oldest is 102 once 112 have scrolled.
	p.v.SetScroll(0, 10, 112)
	if c, ok := p.copyKey(g); ok {
		t.Errorf("trimmed selection copied %+v", c)
	}

	p.v.SetScroll(0, 10, 112)
	p.frame(g, p.press(0, 0, 0), p.move(2, 0), p.release(2, 0))
	if c, ok := p.copyKey(grid("aaaaa", "b")); ok {
		t.Errorf("selection survived a rewrap: %+v", c)
	}
}

// TestAutoscroll drags past the bottom and the top of the view and checks
// the view scrolls, faster the further the pointer is, only while held.
func TestAutoscroll(t *testing.T) {
	p := newPane(t)
	g := denseGrid(20, 5, 0, 7)
	p.v.SetScroll(0, 1000, 1000)
	scroll := func(evs ...event.Event) int {
		p.frame(g, evs...)
		for range 3 {
			p.now = p.now.Add(50 * time.Millisecond)
			p.frame(g)
		}
		return p.v.ScrollDelta()
	}
	p.frame(g, p.press(2, 1, 0))
	near := scroll(p.move(4, 6)) // 2 rows below
	far := scroll(p.move(4, 25))
	if near >= 0 || far > 5*near {
		t.Errorf("below: %d lines near, %d far", near, far)
	}
	if got := p.v.sel.B; got != (vt.Pos{Line: 1004, Col: 4}) {
		t.Errorf("head %+v, want the bottom row", got)
	}
	if up := scroll(p.move(4, -3)); up <= 0 {
		t.Errorf("above: %d lines", up)
	}
	if d := scroll(p.release(4, -3)); d != 0 {
		t.Errorf("after release: %d lines", d)
	}
}

// TestClickModes checks a triple click selects the logical line across
// soft wraps and Alt+drag selects a block.
func TestClickModes(t *testing.T) {
	p := newPane(t)
	p.v.CopyOnSelect = true
	g := wrapped("one", `long l\`, `ine he\`, "re", "x  y", "last")
	p.v.SetScroll(0, 0, 0)
	p.frame(g)
	click := p.press(1, 2, 0)
	evs := []event.Event{click, p.release(1, 2)}
	for range 2 {
		click.Time += 100 * time.Millisecond
		evs = append(evs, click, p.release(1, 2))
	}
	p.frame(g, evs...)
	if c, ok := p.v.Copied(); !ok || c.Text != "long line here" || !c.Whole || c.Sel.Mode != vt.SelectLines {
		t.Errorf("triple click: %+v %v", c, ok)
	}
	// A logical line from the top row may go on above the view.
	click = p.press(1, 0, 0)
	evs = []event.Event{click, p.release(1, 0)}
	for range 2 {
		click.Time += 100 * time.Millisecond
		evs = append(evs, click, p.release(1, 0))
	}
	p.frame(g, evs...)
	if c, ok := p.v.Copied(); !ok || c.Whole {
		t.Errorf("triple click on the top row: %+v %v", c, ok)
	}

	p.frame(g, p.press(1, 1, key.ModAlt), p.move(2, 4), p.release(2, 4))
	if c, ok := p.v.Copied(); !ok || c.Text != "on\nne\ne\n" || c.Sel.Mode != vt.SelectBlock {
		t.Errorf("Alt+drag: %+v %v", c, ok)
	}
}

// TestCopyMode drives copy mode with keys: nothing reaches the program, v
// and y copy from the mark to the cursor, and Esc leaves.
func TestCopyMode(t *testing.T) {
	p := newPane(t)
	g := grid("alpha beta", "gamma delta", "")
	g.Cursor = vt.Cursor{X: 0, Y: 2, Visible: true}
	p.v.SetScroll(0, 0, 0)
	press := func(name key.Name, mods key.Modifiers) []event.Event {
		return []event.Event{key.Event{Name: name, Modifiers: mods, State: key.Press}, key.Event{Name: name, Modifiers: mods, State: key.Release}}
	}
	keys := func(evs ...[]event.Event) string {
		var all []event.Event
		for _, e := range evs {
			all = append(all, e...)
		}
		return p.frame(g, all...)
	}
	if in := keys(press("X", key.ModCtrl|key.ModShift)); in != "" || p.v.CopyMode() != "COPY" {
		t.Fatalf("enter: input %q, mode %q", in, p.v.CopyMode())
	}
	if p.v.cm.cur != (vt.Pos{Line: 2}) {
		t.Errorf("cursor starts at %+v, want the terminal's", p.v.cm.cur)
	}
	in := keys(press("K", 0), press(key.NameUpArrow, 0), press(key.NameDownArrow, 0), press("W", 0), press("V", 0), press("$", key.ModShift))
	in += p.frame(g, key.EditEvent{Text: "kwv$"})
	if in != "" || p.v.CopyMode() != "VISUAL" {
		t.Errorf("keys reached the program: %q, mode %q", in, p.v.CopyMode())
	}
	if in := keys(press("Y", 0)) + p.frame(g, key.EditEvent{Text: "y"}); in != "" {
		t.Errorf("the y that left copy mode reached the program: %q", in)
	}
	if c, ok := p.v.Copied(); !ok || c.Text != "delta" || p.v.CopyMode() != "" {
		t.Errorf("y copied %+v %v, mode %q", c, ok, p.v.CopyMode())
	}
	keys(press("X", key.ModCtrl|key.ModShift), press("V", key.ModCtrl), press("K", 0), press("K", 0), press("L", 0), press(key.NameReturn, 0))
	if c, ok := p.v.Copied(); !ok || c.Text != "al\nga\n" || c.Sel.Mode != vt.SelectBlock {
		t.Errorf("Ctrl+V block copied %+v %v", c, ok)
	}
	keys(press("X", key.ModCtrl|key.ModShift), press("/", 0))
	if !p.v.FindWanted() || p.v.FindWanted() {
		t.Error("/ did not ask for the find bar once")
	}
	keys(press(key.NameEscape, 0))
	if p.v.CopyMode() != "" || p.v.selOn {
		t.Error("Esc did not leave copy mode")
	}
	if in := p.frame(g, key.EditEvent{Text: "k"}); in != "k" {
		t.Error("after copy mode, keys do not reach the program")
	}
}

// TestMotion checks copy mode's cursor motions on a view of lines 90-94
// in a pane of lines 0-99.
func TestMotion(t *testing.T) {
	g := grid("one two  three", "", "  four", "five six", "seven")
	v := view{first: 0, last: 99, top: 90, rows: 5, cols: 14, lines: vt.GridLines(g, 90)}
	at := func(line uint64, col int) vt.Pos { return vt.Pos{Line: line, Col: col} }
	for _, tc := range []struct {
		name     key.Name
		mods     key.Modifiers
		from, to vt.Pos
	}{
		{"H", 0, at(90, 0), at(90, 0)},
		{key.NameRightArrow, 0, at(90, 13), at(90, 13)},
		{"L", 0, at(90, 3), at(90, 4)},
		{"K", 0, at(0, 2), at(0, 2)},
		{"J", 0, at(99, 2), at(99, 2)},
		{key.NameDownArrow, 0, at(90, 2), at(91, 2)},
		{"0", 0, at(90, 5), at(90, 0)},
		{"$", key.ModShift, at(90, 0), at(90, 13)},
		{"$", key.ModShift, at(91, 4), at(91, 0)},
		{"G", 0, at(50, 3), at(0, 0)},
		{"G", key.ModShift, at(50, 3), at(99, 0)},
		{"U", key.ModCtrl, at(50, 3), at(48, 3)},
		{"D", key.ModCtrl, at(98, 3), at(99, 3)},
		{key.NamePageUp, 0, at(3, 1), at(0, 1)},
		{key.NamePageDown, 0, at(50, 1), at(55, 1)},
		{"W", 0, at(90, 0), at(90, 4)},
		{"W", 0, at(90, 4), at(90, 9)},
		{"W", 0, at(90, 9), at(92, 2)},
		{"B", 0, at(92, 2), at(90, 9)},
		{"B", 0, at(90, 6), at(90, 4)},
		{"B", 0, at(93, 5), at(93, 0)},
	} {
		got, ok := motion(key.Event{Name: tc.name, Modifiers: tc.mods}, tc.from, v)
		if !ok || got != tc.to {
			t.Errorf("%v+%s from %+v: %+v %v, want %+v", tc.mods, tc.name, tc.from, got, ok, tc.to)
		}
	}
	if _, ok := motion(key.Event{Name: "Z"}, at(90, 0), v); ok {
		t.Error("Z is a motion")
	}
	for _, tc := range []struct {
		line, top uint64
		want      int
	}{
		{95, 90, -1}, {94, 90, 0}, {90, 90, 0}, {87, 90, 3},
	} {
		if got := revealDelta(tc.line, tc.top, 5); got != tc.want {
			t.Errorf("revealDelta(%d, %d) = %d, want %d", tc.line, tc.top, got, tc.want)
		}
	}
}
