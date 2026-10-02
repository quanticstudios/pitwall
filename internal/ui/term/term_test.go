package term

import (
	"fmt"
	"image"
	"image/color"
	"testing"

	"gioui.org/font/gofont"
	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func testTheme() *theme.Theme {
	th := &theme.Theme{
		Shaper:   text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Collection())),
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

func TestTextRuns(t *testing.T) {
	red, blue := color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}
	st := []style{
		{fg: red}, {fg: red}, {fg: blue, blank: true}, {fg: red}, // blank does not break
		{fg: red, font: 1}, // face change breaks
		{fg: blue}, {fg: blue},
		{blank: true},
	}
	got := fmt.Sprint(textRuns(st))
	if want := "[[0 4] [4 5] [5 7]]"; got != want {
		t.Errorf("textRuns = %s, want %s", got, want)
	}
	if r := textRuns([]style{{blank: true}}); r != nil {
		t.Errorf("all blank = %v", r)
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
	if cols != 800/cell.X || rows != 600/cell.Y {
		t.Errorf("Layout fit %dx%d for cell %v", cols, rows, cell)
	}
}

// TestNavKeysPassThrough routes keys through a real Gio router: the focused
// pane must not take Alt+H/J/K/L/arrows, so a window filter gets them.
func TestNavKeysPassThrough(t *testing.T) {
	var r input.Router
	v := new(View)
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
