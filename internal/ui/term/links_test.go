package term

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestRowLinks(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string // "x0-x1 url"
	}{
		{"no links in plain text, not even www or http", nil},
		{"see https://example.com/a?b=1#c now", []string{"4-31 https://example.com/a?b=1#c"}},
		{"http://a.test file:///tmp/x.txt", []string{"0-13 http://a.test", "14-31 file:///tmp/x.txt"}},
		{"go to www.example.org.", []string{"6-21 https://www.example.org"}},
		{"(see https://example.com/x), then", []string{"5-26 https://example.com/x"}},
		{"https://en.wikipedia.org/wiki/Go_(game) ok", []string{"0-39 https://en.wikipedia.org/wiki/Go_(game)"}},
		{"[https://a.test/b]", []string{"1-17 https://a.test/b"}},
		{`"https://a.test/q" and 'https://b.test/r', done`, []string{"1-17 https://a.test/q", "24-40 https://b.test/r"}},
		{"https://a.test, https://b.test; ok", []string{"0-14 https://a.test", "16-30 https://b.test"}},
		{"│https://a.test│", []string{"1-15 https://a.test"}},
		{"ftp://a.test javascript:alert(1) https://", nil},
	} {
		var got []string
		ls, _ := rowLinks(nil, nil, grid(tc.line).Cells)
		for _, l := range ls {
			got = append(got, fmt.Sprintf("%d-%d %s", l.x0, l.x1, l.url))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q: got %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestRowLinksOSC8 checks a program's hyperlink covers exactly its cells,
// including the right half of a wide character, wins over URL-looking text
// under it, and is dropped when its scheme may not be opened.
func TestRowLinksOSC8(t *testing.T) {
	g := grid("ab 日  www.c.test javascript")
	set := func(x0, x1 int, u string) {
		for x := x0; x < x1; x++ {
			g.Cells[x].Link = u
		}
	}
	g.Cells[3].Width, g.Cells[4] = 2, vt.Cell{} // 日 and its right half
	set(3, 4, "https://docs.test/")
	set(6, 16, "file:///srv/notes")
	set(17, 27, "javascript:alert(1)")
	ls, _ := rowLinks(nil, nil, g.Cells)
	want := []link{{3, 5, "https://docs.test/", true}, {6, 16, "file:///srv/notes", true}}
	if !slices.Equal(ls, want) {
		t.Errorf("got %+v, want %+v", ls, want)
	}
}

func TestOpenable(t *testing.T) {
	for u, want := range map[string]bool{
		"https://a.test": true, "HTTP://a.test": true, "file:///x": true, "mailto:a@b.test": true,
		"javascript:alert(1)": false, "ftp://a.test": false, "vscode://x": false, "no-scheme": false, "": false,
	} {
		if openable(u) != want {
			t.Errorf("openable(%q) = %v", u, !want)
		}
	}
}

// TestCtrlClickLink drives a pane through a Gio router: Ctrl+click on a
// link asks to open it and leaves the selection and clipboard alone, also
// while the program owns the mouse, which gets nothing. A plain click and a
// Ctrl+click off a link behave as before, and with Links off nothing opens.
func TestCtrlClickLink(t *testing.T) {
	var r input.Router
	v := &View{Links: true, CopyOnSelect: true}
	th := testTheme()
	g := grid(
		"open https://example.com/docs now",
		"plain text here",
	)
	frame := func(m vt.Modes, evs ...event.Event) string {
		r.Queue(evs...)
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		_, in, _, _ := v.Layout(gtx, th, g, m, true)
		r.Frame(gtx.Ops)
		if _, _, ok := r.WriteClipboard(); ok {
			t.Error("clipboard written")
		}
		return string(in)
	}
	frame(vt.Modes{})
	frame(vt.Modes{})
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	at := func(x, y int) f32.Point {
		return f32.Pt(pad+float32(x*v.cell.X)+1, pad+float32(y*v.cell.Y)+1)
	}
	click := func(x, y int, mods key.Modifiers) []event.Event {
		return []event.Event{
			pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: at(x, y), Modifiers: mods},
			pointer.Event{Kind: pointer.Release, Position: at(x, y), Modifiers: mods},
		}
	}
	mouse := vt.Modes{Mouse: vt.MouseNormal, MouseSGR: true}

	if in := frame(vt.Modes{}, click(10, 0, key.ModCtrl)...); in != "" || v.OpenLink() != "https://example.com/docs" || v.sel.on || v.dragging {
		t.Errorf("Ctrl+click: input %q, selection %+v", in, v.sel)
	}
	if in := frame(mouse, click(10, 0, key.ModCtrl)...); in != "" || v.OpenLink() != "https://example.com/docs" {
		t.Errorf("Ctrl+click with mouse reporting: input %q", in)
	}
	if in := frame(mouse, click(10, 0, key.ModCommand)...); in != "" || v.OpenLink() != "https://example.com/docs" {
		t.Errorf("Cmd+click: input %q", in)
	}
	if in := frame(mouse, click(10, 0, 0)...); in != "\x1b[<0;11;1M\x1b[<0;11;1m" || v.OpenLink() != "" {
		t.Errorf("plain click with mouse reporting: input %q", in)
	}
	if in := frame(mouse, click(2, 1, key.ModCtrl)...); in != "\x1b[<16;3;2M\x1b[<16;3;2m" || v.OpenLink() != "" {
		t.Errorf("Ctrl+click off a link with mouse reporting: input %q", in)
	}
	frame(vt.Modes{}, click(10, 0, 0)...)
	if v.OpenLink() != "" || !v.sel.a.Eq(image.Pt(10, 0)) {
		t.Errorf("plain click opened or did not start a selection: %+v", v.sel)
	}
	v.Links = false
	if in := frame(mouse, click(10, 0, key.ModCtrl)...); in == "" || v.OpenLink() != "" {
		t.Errorf("Links off: input %q", in)
	}
}

// TestLinkHover checks Ctrl over a link redraws it in the theme's link color
// with the hand pointer, and only that link: the row key changes, the rest
// of the screen keeps its cached rows, and letting go of Ctrl restores it.
func TestLinkHover(t *testing.T) {
	var r input.Router
	v := &View{Links: true}
	th := testTheme()
	th.Blue = th.TermCur
	g := grid("a https://a.test b", "plain")
	frame := func(evs ...event.Event) {
		r.Queue(evs...)
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		v.Layout(gtx, th, g, vt.Modes{}, true)
		r.Frame(gtx.Ops)
	}
	frame()
	frame()
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	move := func(x int, mods key.Modifiers) pointer.Event {
		return pointer.Event{Kind: pointer.Move, Position: f32.Pt(pad+float32(x*v.cell.X)+1, pad+1), Modifiers: mods}
	}
	keys := func() []uint64 {
		var ks []uint64
		for k := range v.prev { // drawRows swaps the frame's rows into prev
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return ks
	}
	frame(move(5, 0))
	rest := keys()
	if v.hoverOn || r.Cursor() == pointer.CursorPointer {
		t.Fatal("hover without Ctrl")
	}
	frame(move(5, key.ModCtrl))
	if !v.hoverOn || v.hover.url != "https://a.test" || r.Cursor() != pointer.CursorPointer {
		t.Fatalf("Ctrl hover: %v %+v %v", v.hoverOn, v.hover, r.Cursor())
	}
	if hot := keys(); len(hot) != 2 || slices.Equal(hot, rest) || !slices.ContainsFunc(hot, func(k uint64) bool { return slices.Contains(rest, k) }) {
		t.Errorf("row keys at rest %v, hovered %v: want only the link's row changed", rest, hot)
	}
	frame(key.Event{Name: key.NameCtrl, State: key.Release})
	if v.hoverOn || !slices.Equal(keys(), rest) {
		t.Error("releasing Ctrl kept the hover")
	}
	frame(move(0, key.ModCtrl))
	if v.hoverOn {
		t.Error("Ctrl over plain text hovers")
	}
}

// TestCtrlReleaseInOtherPane checks the hovered pane drops its hover when
// Ctrl goes up while another pane has key focus and the pointer stays put.
func TestCtrlReleaseInOtherPane(t *testing.T) {
	var r input.Router
	hov, foc := &View{Links: true}, &View{Links: true}
	th := testTheme()
	g := grid("a https://a.test b")
	frame := func(evs ...event.Event) {
		r.Queue(evs...)
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		hov.Layout(gtx, th, g, vt.Modes{}, false)
		off := op.Offset(image.Pt(0, 300)).Push(gtx.Ops)
		foc.Layout(gtx, th, g, vt.Modes{}, true)
		off.Pop()
		r.Frame(gtx.Ops)
	}
	frame()
	frame()
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	frame(pointer.Event{Kind: pointer.Move, Position: f32.Pt(pad+float32(5*hov.cell.X)+1, pad+1), Modifiers: key.ModCtrl})
	if !hov.hoverOn {
		t.Fatal("no hover with Ctrl")
	}
	frame(key.Event{Name: key.NameCtrl, State: key.Release})
	frame()
	if hov.hoverOn {
		t.Error("hover kept after Ctrl went up in the focused pane")
	}
}
