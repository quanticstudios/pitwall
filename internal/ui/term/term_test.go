package term

import (
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/go-text/typesetting/fontscan"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func testTheme() *theme.Theme {
	if fonts == nil { // the Go fonts only, so tests never depend on the system's
		fonts = fontscan.NewFontMap(nil)
		for _, f := range gofont.Collection() {
			d := opentype.FontToDescription(f.Font)
			fonts.AddFace(f.Face.Face(), fontscan.Location{File: fmt.Sprint(d)}, d)
		}
	}
	th := &theme.Theme{
		MonoFont: gofont.Collection()[0].Font,
		MonoSize: 13,
		TermFg:   color.NRGBA{R: 0xdd, G: 0xdd, B: 0xdd, A: 0xff},
		TermBg:   color.NRGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff},
		TermCur:  color.NRGBA{R: 0xff, A: 0xff},
	}
	th.MonoFont.Typeface = "Go Mono"
	for i := range th.ANSI {
		th.ANSI[i] = color.NRGBA{R: uint8(i), A: 0xff}
	}
	return th
}

func TestResolve(t *testing.T) {
	th := testTheme()
	def := color.NRGBA{R: 1, G: 2, B: 3, A: 4}
	rgb := func(r, g, b uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: 0xff} }
	for _, tc := range []struct {
		c    vt.Color
		want color.NRGBA
	}{
		{0, def},
		{vt.PaletteFlag | 9, th.ANSI[9]},
		{vt.PaletteFlag | 16, rgb(0, 0, 0)},
		{vt.PaletteFlag | 196, rgb(255, 0, 0)},
		{vt.PaletteFlag | 67, rgb(95, 135, 175)},
		{vt.PaletteFlag | 231, rgb(255, 255, 255)},
		{vt.PaletteFlag | 232, rgb(8, 8, 8)},
		{vt.PaletteFlag | 255, rgb(238, 238, 238)},
		{vt.RGBFlag | 0x12abef, rgb(0x12, 0xab, 0xef)},
	} {
		if got := resolve(th, tc.c, def); got != tc.want {
			t.Errorf("resolve(%#x) = %v, want %v", uint32(tc.c), got, tc.want)
		}
	}
}

func grid(lines ...string) *vt.Grid {
	g := &vt.Grid{Cols: 0, Rows: len(lines)}
	for _, l := range lines {
		g.Cols = max(g.Cols, len([]rune(l)))
	}
	for _, l := range lines {
		r := []rune(l)
		for x := range g.Cols {
			c := vt.Cell{Content: " ", Width: 1}
			if x < len(r) {
				c.Content = string(r[x])
			}
			g.Cells = append(g.Cells, c)
		}
	}
	return g
}

func TestSelectionText(t *testing.T) {
	g := grid("hello world", "second line", "third")
	s := selection{a: image.Pt(2, 2), b: image.Pt(6, 0), on: true} // backwards drag
	if got, want := selectionText(g, s), "world\nsecond line\nthi"; got != want {
		t.Errorf("selectionText = %q, want %q", got, want)
	}
	// Wide char: trailing half is skipped, not doubled.
	g = &vt.Grid{Cols: 4, Rows: 1, Cells: []vt.Cell{
		{Content: "a", Width: 1}, {Content: "漢", Width: 2}, {Width: 0}, {Content: "b", Width: 1},
	}}
	if got := selectionText(g, selection{b: image.Pt(3, 0), on: true}); got != "a漢b" {
		t.Errorf("wide = %q", got)
	}
	if x0, x1 := wordAt(grid("ls /tmp/x.go (ok)"), 6, 0); x0 != 3 || x1 != 11 {
		t.Errorf("wordAt = %d,%d", x0, x1)
	}
	s0, s1 := selection{a: image.Pt(3, 1), b: image.Pt(1, 1), on: true}.cols(1, 10)
	if s0 != 1 || s1 != 4 {
		t.Errorf("cols = %d,%d", s0, s1)
	}
}

func TestFit(t *testing.T) {
	cols, rows := fit(image.Pt(805, 340), image.Pt(8, 17))
	if cols != 100 || rows != 20 {
		t.Errorf("fit = %d,%d", cols, rows)
	}
	if cols, rows := fit(image.Pt(3, 3), image.Pt(8, 17)); cols != 1 || rows != 1 {
		t.Errorf("tiny fit = %d,%d", cols, rows)
	}
}

func TestLayoutFits(t *testing.T) {
	var v View
	th := testTheme()
	gtx := testContext(image.Pt(800, 600))
	cell := v.CellSize(gtx, th)
	if cell.X < 6 || cell.Y < 12 {
		t.Fatalf("cell = %v", cell)
	}
	_, _, cols, rows := v.Layout(gtx, th, denseGrid(20, 5, 0, 216), vt.Modes{}, true)
	pad := gtx.Dp(padding)
	if cols != (800-2*pad)/cell.X || rows != (600-2*pad)/cell.Y {
		t.Errorf("Layout fit %dx%d for cell %v pad %d", cols, rows, cell, pad)
	}
}

// TestFontSizeRefits: a theme with another mono size or line height
// re-measures cells, so Layout reports a new grid size.
func TestFontSizeRefits(t *testing.T) {
	v := new(View)
	gtx := testContext(image.Pt(800, 600))
	th := testTheme()
	_, _, cols, rows := v.Layout(gtx, th, denseGrid(20, 5, 0, 7), vt.Modes{}, true)
	big := *th
	big.MonoSize = 26
	_, _, c2, r2 := v.Layout(testContext(image.Pt(800, 600)), &big, denseGrid(20, 5, 0, 7), vt.Modes{}, true)
	if c2 >= cols || r2 >= rows {
		t.Fatalf("mono size 13 -> 26: %dx%d -> %dx%d", cols, rows, c2, r2)
	}
	tall := *th
	tall.LineHeight = 2
	_, _, c3, r3 := v.Layout(testContext(image.Pt(800, 600)), &tall, denseGrid(20, 5, 0, 7), vt.Modes{}, true)
	if c3 != cols || r3 >= rows*6/10 {
		t.Fatalf("line height 2: %dx%d -> %dx%d", cols, rows, c3, r3)
	}
}

// TestNavKeysPassThrough routes keys through a real Gio router: the focused
// pane must not take Alt+H/J/K/L/arrows, so a window filter gets them.
func TestNavKeysPassThrough(t *testing.T) {
	var r input.Router
	v := &View{Keys: config.Preset("aide")}
	pane := keyFilters(v)
	pane = append(pane, key.FocusFilter{Target: v})
	app := []event.Filter{key.Filter{Name: "H", Required: key.ModAlt}, key.Filter{Name: key.NameLeftArrow, Required: key.ModAlt}}
	frame := func() {
		for {
			if _, ok := r.Event(pane...); !ok {
				break
			}
		}
		for {
			if _, ok := r.Event(app...); !ok {
				break
			}
		}
		ops := new(op.Ops)
		event.Op(ops, v)
		r.Frame(ops)
	}
	frame()
	r.Source().Execute(key.FocusCmd{Tag: v})
	frame()
	for _, tc := range []struct {
		e    key.Event
		pane bool
	}{
		{key.Event{Name: "H", Modifiers: key.ModAlt}, false},
		{key.Event{Name: key.NameLeftArrow, Modifiers: key.ModAlt}, false},
		{key.Event{Name: "X", Modifiers: key.ModAlt}, true},
		{key.Event{Name: "C", Modifiers: key.ModCtrl}, true},
		{key.Event{Name: key.NameLeftArrow}, true},
		{key.Event{Name: key.NameTab}, true},
	} {
		r.Queue(tc.e)
		_, gotPane := r.Event(pane...)
		_, gotApp := r.Event(app...)
		if gotPane != tc.pane || gotApp == tc.pane && tc.e.Modifiers == key.ModAlt {
			t.Errorf("%v: pane got %v, app got %v", tc.e, gotPane, gotApp)
		}
		frame()
	}
}

func testContext(size image.Point) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1.5, PxPerSp: 1.5},
		Constraints: layout.Exact(size),
	}
}

// denseGrid fills every cell with a printable char and a color that changes
// every five cells, cycling through colors palette entries (216: every run
// is a new color; 7: syntax-highlighted code). seed shifts the content so
// successive frames differ.
func denseGrid(cols, rows, seed, colors int) *vt.Grid {
	g := &vt.Grid{Cols: cols, Rows: rows, Cells: make([]vt.Cell, cols*rows)}
	for i := range g.Cells {
		n := i + seed
		g.Cells[i] = vt.Cell{
			Content: string(rune('!' + n%94)),
			Width:   1,
			FG:      vt.PaletteFlag | vt.Color(16+(n/5)%colors),
			Attrs:   vt.Attr((n / 40) % 2), // Bold every other 40 cells
		}
		if (n/17)%3 == 0 {
			g.Cells[i].BG = vt.RGBFlag | vt.Color(n*2654435761)&0xffffff
		}
	}
	g.Cursor = vt.Cursor{X: 3, Y: 3, Visible: true}
	return g
}

func benchLayout(b *testing.B, changing bool, colors int) {
	var v View
	th := testTheme()
	gtx := testContext(image.Pt(1, 1))
	cell := v.CellSize(gtx, th)
	gtx.Constraints = layout.Exact(image.Pt(250*cell.X, 70*cell.Y))
	frames := []*vt.Grid{denseGrid(250, 70, 0, colors)}
	if changing {
		for i := 1; i < 64; i++ {
			frames = append(frames, denseGrid(250, 70, i*7, colors))
		}
	}
	// Warm the glyph cache, as a running pane would be.
	for _, g := range frames {
		gtx.Ops.Reset()
		v.Layout(gtx, th, g, vt.Modes{}, true)
	}
	b.ResetTimer()
	for i := range b.N {
		gtx.Ops.Reset()
		v.Layout(gtx, th, frames[i%len(frames)], vt.Modes{}, true)
	}
}

// BenchmarkLayoutStatic is a 250x70 dense colored grid redrawn unchanged
// (cursor blink, focus change): every row comes from the cache.
func BenchmarkLayoutStatic(b *testing.B) { benchLayout(b, false, 216) }

// BenchmarkLayoutAllRowsChanged redraws a 250x70 grid where every row
// differs from the previous frame, the worst case for the row cache.
func BenchmarkLayoutAllRowsChanged(b *testing.B) { benchLayout(b, true, 216) }

// BenchmarkGPU renders full frames through Gio's headless GPU backend and
// reads back one pixel row, so the time includes path rasterization.
func BenchmarkGPU(b *testing.B) {
	for _, tc := range []struct {
		changing bool
		colors   int
	}{{false, 216}, {true, 216}, {false, 7}, {true, 7}} {
		changing, colors := tc.changing, tc.colors
		b.Run(fmt.Sprintf("changing=%v/colors=%d", changing, colors), func(b *testing.B) {
			var v View
			th := testTheme()
			gtx := testContext(image.Pt(1, 1))
			cell := v.CellSize(gtx, th)
			size := image.Pt(250*cell.X, 70*cell.Y)
			w, err := headless.NewWindow(size.X, size.Y)
			if err != nil {
				b.Skip(err)
			}
			defer w.Release()
			gtx.Constraints = layout.Exact(size)
			frames := []*vt.Grid{denseGrid(250, 70, 0, colors)}
			if changing {
				for i := 1; i < 16; i++ {
					frames = append(frames, denseGrid(250, 70, i*7, colors))
				}
			}
			img := image.NewRGBA(image.Rect(0, 0, 1, 1)) // forces the GPU to finish
			draw := func(i int) {
				gtx.Ops.Reset()
				v.Layout(gtx, th, frames[i%len(frames)], vt.Modes{}, true)
				if err := w.Frame(gtx.Ops); err != nil {
					b.Fatal(err)
				}
				if err := w.Screenshot(img); err != nil {
					b.Fatal(err)
				}
			}
			for i := range 2 * len(frames) {
				draw(i)
			}
			b.ResetTimer()
			for i := range b.N {
				draw(i)
			}
		})
	}
}

func TestBlockRect(t *testing.T) {
	for s, want := range map[string]image.Rectangle{
		"▀": image.Rect(0, 0, 8, 4), "▄": image.Rect(0, 4, 8, 8), "█": image.Rect(0, 0, 8, 8),
		"▏": image.Rect(0, 0, 1, 8), "▐": image.Rect(4, 0, 8, 8),
	} {
		if got, ok := blockRect(s); !ok || got != want {
			t.Errorf("blockRect(%q) = %v %v, want %v", s, got, ok, want)
		}
	}
	for _, s := range []string{"░", "a", "▀▀", ""} {
		if _, ok := blockRect(s); ok {
			t.Errorf("blockRect(%q) ok", s)
		}
	}
}

// TestScroll drives wheel, touchpad and Shift+PageUp/PageDown through a
// real router and checks the lines ScrollDelta reports, plus the padding
// in the fit.
func TestScroll(t *testing.T) {
	var r input.Router
	v := new(View)
	th := testTheme()
	g := denseGrid(20, 5, 0, 7)
	frame := func(m vt.Modes) (rows int) {
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		_, _, _, rows = v.Layout(gtx, th, g, m, true)
		r.Frame(gtx.Ops)
		return rows
	}
	rows := frame(vt.Modes{})
	frame(vt.Modes{})
	ch := float32(v.cell.Y)
	at := f32.Pt(100, 100)
	for _, tc := range []struct {
		name string
		evs  []event.Event
		m    vt.Modes
		want int
	}{
		{"wheel up 3 lines", []event.Event{pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, -3*ch)}}, vt.Modes{}, 3},
		{"touchpad halves add up", []event.Event{
			pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, ch/2)},
			pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, ch/4)},
			pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, ch/4)},
		}, vt.Modes{}, -1},
		{"program owns the mouse", []event.Event{pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(0, -3*ch)}}, vt.Modes{Mouse: vt.MouseNormal}, 0},
		{"shift wheel arrives as X", []event.Event{pointer.Event{Kind: pointer.Scroll, Position: at, Scroll: f32.Pt(-2*ch, 0), Modifiers: key.ModShift}}, vt.Modes{Mouse: vt.MouseNormal}, 2},
		{"shift+pageup", []event.Event{key.Event{Name: key.NamePageUp, Modifiers: key.ModShift, State: key.Press}}, vt.Modes{}, rows},
		{"shift+pagedown", []event.Event{key.Event{Name: key.NamePageDown, Modifiers: key.ModShift, State: key.Press}}, vt.Modes{}, -rows},
	} {
		r.Queue(tc.evs...)
		frame(tc.m)
		if got := v.ScrollDelta(); got != tc.want {
			t.Errorf("%s: ScrollDelta = %d, want %d", tc.name, got, tc.want)
		}
	}
	if v.ScrollDelta() != 0 {
		t.Error("ScrollDelta does not clear")
	}
}

// TestPadding checks the pointer maps through the padding to cells.
func TestPadding(t *testing.T) {
	var v View
	th := testTheme()
	gtx := testContext(image.Pt(400, 300))
	v.Layout(gtx, th, grid("abcdef", "ghijkl"), vt.Modes{}, true)
	p := float32(gtx.Dp(padding))
	g := grid("abcdef", "ghijkl")
	press := func(x, y float32) image.Point {
		v.pointer(pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)}, g, vt.Modes{}, true)
		return v.sel.a
	}
	if c := press(p+1, p+1); c != image.Pt(0, 0) {
		t.Errorf("first cell = %v", c)
	}
	if c := press(p+float32(2*v.cell.X)+1, p+float32(v.cell.Y)+1); c != image.Pt(2, 1) {
		t.Errorf("cell (2,1) = %v", c)
	}
	if c := press(1, 1); c != image.Pt(0, 0) {
		t.Errorf("padding clamps to %v", c)
	}
}

// TestCursorBlink renders the block cursor on, off after 530 ms, on again
// after input, and hollow and steady when unfocused.
func TestCursorBlink(t *testing.T) {
	var v View
	th := testTheme()
	w, err := headless.NewWindow(200, 100)
	if err != nil {
		t.Skip(err)
	}
	defer w.Release()
	g := grid("ab  ")
	g.Cursor = vt.Cursor{X: 2, Y: 0, Visible: true}
	t0 := time.Unix(1000, 0)
	center := func(now time.Time, focused bool) color.RGBA {
		gtx := testContext(image.Pt(200, 100))
		gtx.Now = now
		v.Layout(gtx, th, g, vt.Modes{}, focused)
		if err := w.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 200, 100))
		if err := w.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		p := gtx.Dp(padding)
		return img.RGBAAt(p+2*v.cell.X+v.cell.X/2, p+v.cell.Y/2)
	}
	on, off := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	for _, tc := range []struct {
		name    string
		at      time.Duration
		focused bool
		want    color.RGBA
	}{
		{"focus shows it", 0, true, on},
		{"first half-period", 500 * time.Millisecond, true, on},
		{"blinks off", 600 * time.Millisecond, true, off},
		{"back on", 1100 * time.Millisecond, true, on},
		{"unfocused is hollow", 1700 * time.Millisecond, false, off},
	} {
		if got := center(t0.Add(tc.at), tc.focused); got != tc.want {
			t.Errorf("%s: center = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// routedPane lays a View out through a real Gio router. Each call queues
// evs, draws one frame and returns the PTY bytes it produced. Two empty
// frames first register the handler and take key focus.
func routedPane(t *testing.T) (*View, func(m vt.Modes, focused bool, evs ...event.Event) string) {
	t.Helper()
	var r input.Router
	v := new(View)
	th := testTheme()
	g := denseGrid(20, 5, 0, 7)
	frame := func(m vt.Modes, focused bool, evs ...event.Event) string {
		r.Queue(evs...)
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		_, in, _, _ := v.Layout(gtx, th, g, m, focused)
		r.Frame(gtx.Ops)
		return string(in)
	}
	frame(vt.Modes{}, true)
	frame(vt.Modes{}, true)
	return v, frame
}

// TestKittyReportAllText checks committed text in kitty report-all mode:
// a plain key's duplicate text event is dropped, while IME and compose
// commits with no matching key press reach the program once.
func TestKittyReportAllText(t *testing.T) {
	_, frame := routedPane(t)
	m := vt.Modes{KittyKeyboard: 8}
	a := key.Event{Name: "A", State: key.Press}
	got := frame(m, true, a, key.EditEvent{Text: "a"}, key.EditEvent{Text: "日本語"}, key.EditEvent{Text: "é"})
	if want := "\x1b[97;1u日本語é"; got != want {
		t.Errorf("report-all = %q, want %q", got, want)
	}
	if got := frame(vt.Modes{}, true, a, key.EditEvent{Text: "a"}, key.EditEvent{Text: "日本語"}); got != "a日本語" {
		t.Errorf("legacy = %q", got)
	}
}

// TestMouseButtonChange checks SGR reports name the button that changed:
// Gio's release event carries the buttons still held, not the released one.
func TestMouseButtonChange(t *testing.T) {
	v, frame := routedPane(t)
	m := vt.Modes{Mouse: vt.MouseNormal, MouseSGR: true}
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	at := f32.Pt(pad+float32(2*v.cell.X)+1, pad+float32(v.cell.Y)+1) // cell (2,1)
	for _, tc := range []struct {
		e    pointer.Event
		want string
	}{
		{pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonSecondary, Position: at}, "\x1b[<2;3;2M"},
		{pointer.Event{Kind: pointer.Release, Position: at}, "\x1b[<2;3;2m"},
		{pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: at}, "\x1b[<0;3;2M"},
		{pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary | pointer.ButtonTertiary, Position: at}, "\x1b[<1;3;2M"},
		{pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonPrimary, Position: at}, "\x1b[<1;3;2m"},
		{pointer.Event{Kind: pointer.Release, Position: at}, "\x1b[<0;3;2m"},
	} {
		if got := frame(m, true, tc.e); got != tc.want {
			t.Errorf("%v %v: got %q, want %q", tc.e.Kind, tc.e.Buttons, got, tc.want)
		}
	}
}

// TestFocusReports checks mode 1004: the pane losing and regaining focus,
// the window doing the same, and a pane no longer drawn each send CSI O or
// CSI I once, and nothing goes out with the mode off.
func TestFocusReports(t *testing.T) {
	v, frame := routedPane(t)
	on := vt.Modes{FocusEvents: true}
	for _, tc := range []struct {
		name    string
		m       vt.Modes
		focused bool
		evs     []event.Event
		want    string
	}{
		{"pane blur", on, false, nil, "\x1b[O"},
		{"still blurred", on, false, nil, ""},
		{"pane focus", on, true, nil, "\x1b[I"},
		{"window blur", on, true, []event.Event{key.FocusEvent{Focus: false}}, "\x1b[O"},
		{"window focus", on, true, []event.Event{key.FocusEvent{Focus: true}}, "\x1b[I"},
		{"off blur", vt.Modes{}, false, nil, ""},
		{"off focus", vt.Modes{}, true, nil, ""},
	} {
		if got := frame(tc.m, tc.focused, tc.evs...); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	frame(on, true)
	if got := string(v.Blur()); got != "\x1b[O" {
		t.Errorf("Blur = %q", got)
	}
	if got := v.Blur(); got != nil {
		t.Errorf("second Blur = %q", got)
	}
}

// TestCopyOnSelect checks a finished mouse selection reaches the clipboard:
// on drag release and on a double-clicked word, not mid-drag, not on a plain
// click, and not at all with CopyOnSelect off.
func TestCopyOnSelect(t *testing.T) {
	var r input.Router
	v := &View{CopyOnSelect: true}
	th, g := testTheme(), denseGrid(20, 5, 0, 7)
	frame := func(evs ...event.Event) string {
		r.Queue(evs...)
		gtx := testContext(image.Pt(400, 300))
		gtx.Source = r.Source()
		v.Layout(gtx, th, g, vt.Modes{}, true)
		r.Frame(gtx.Ops)
		got := v.Copied()
		if mime, b, ok := r.WriteClipboard(); ok != (got != "") || ok && (mime != "application/text" || string(b) != got) {
			t.Fatalf("clipboard %v %q %q, Copied %q", ok, mime, b, got)
		}
		return got
	}
	frame()
	frame()
	pad := float32(testContext(image.Pt(1, 1)).Dp(padding))
	at := func(x, y int) f32.Point {
		return f32.Pt(pad+float32(x*v.cell.X)+1, pad+float32(y*v.cell.Y)+1)
	}
	var clock time.Duration
	press := func(x, y int) pointer.Event {
		clock += time.Second
		return pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Position: at(x, y), Time: clock}
	}
	release := func(x, y int) pointer.Event {
		return pointer.Event{Kind: pointer.Release, Position: at(x, y), Time: clock}
	}
	// The router turns a move with a button held into a Drag.
	drag := pointer.Event{Kind: pointer.Move, Buttons: pointer.ButtonPrimary, Position: at(6, 1)}

	if got := frame(press(2, 1), release(2, 1)); got != "" {
		t.Errorf("plain click copied %q", got)
	}
	if got := frame(press(2, 1), drag); got != "" {
		t.Errorf("mid-drag copied %q", got)
	}
	if got := frame(release(6, 1)); got != "789:;" {
		t.Errorf("drag release copied %q", got)
	}
	p := press(2, 1)
	q := p
	q.Time += 100 * time.Millisecond
	if got := frame(p, release(2, 1), q, release(2, 1)); got != "56789:" {
		t.Errorf("double-click copied %q", got)
	}
	v.CopyOnSelect = false
	if got := frame(press(2, 1), drag, release(6, 1)); got != "" {
		t.Errorf("off: copied %q", got)
	}
	if got := frame(key.Event{Name: "C", Modifiers: key.ModCtrl | key.ModShift, State: key.Press}); got != "789:;" {
		t.Errorf("off: copy key copied %q", got)
	}
}

// TestSelectedContrast checks the selection shows on every built-in theme's
// terminal background: lighter on dark ones, darker on light ones, and by
// a visible step either way.
func TestSelectedContrast(t *testing.T) {
	luma := func(c color.NRGBA) int { return (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000 }
	for _, name := range config.Themes {
		th, _ := config.Builtin(name)
		bg := color.NRGBA{A: 0xff}
		fmt.Sscanf(string(th.Terminal.Background), "#%02x%02x%02x", &bg.R, &bg.G, &bg.B)
		d := luma(selected(bg)) - luma(bg)
		if light := luma(bg) > 140; light && d > -20 || !light && d < 20 {
			t.Errorf("%s: background %v, selection %v (luma step %d)", name, bg, selected(bg), d)
		}
	}
}
