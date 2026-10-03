// Command pwdemo-term shows the term renderer on a hand-built grid. With
// -bench it streams a changing 250x70 grid and logs frame times; with
// -scrolled it shows the scrollbar, which the wheel then moves.
package main

import (
	"flag"
	"fmt"
	"image/color"
	"log"
	"math"
	"os"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

var (
	bench = flag.Bool("bench", false, "stream a changing 250x70 grid and log frame times")
	back  = flag.Int("scrolled", 0, "start this many lines back in a fake 500-line scrollback")
	size  = flag.Float64("size", 13, "font size in sp")
)

func main() {
	flag.Parse()
	th := loadTheme()
	go func() {
		w := new(app.Window)
		w.Option(app.Title("pwdemo-term"), app.Size(unit.Dp(1580), unit.Dp(980)))
		if err := run(w, th); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window, th *theme.Theme) error {
	var (
		ops   op.Ops
		v     term.View
		g     = fixture()
		frame int
		last  = time.Now()
		cost  time.Duration
		n     int
		off   = *back
	)
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			if *bench {
				g = dense(250, 70, frame)
				frame++
				gtx.Execute(op.InvalidateCmd{})
			}
			t := time.Now()
			// The grid stays put; only the scrollbar shows the offset.
			off = min(max(off+v.ScrollDelta(), 0), 500)
			v.SetScroll(off, 500)
			_, _, cols, rows := v.Layout(gtx, th, g, vt.Modes{}, true)
			e.Frame(gtx.Ops)
			cost += time.Since(t)
			n++
			if d := time.Since(last); d > 2*time.Second {
				log.Printf("%d frames in %v: %.1f fps, Layout+Frame %v/frame, fits %dx%d", n, d.Round(time.Millisecond),
					float64(n)/d.Seconds(), (cost / time.Duration(n)).Round(time.Microsecond), cols, rows)
				last, cost, n = time.Now(), 0, 0
			}
		}
	}
}

func loadTheme() *theme.Theme {
	var faces []font.FontFace
	for _, style := range []string{"Regular", "Bold", "Italic", "BoldItalic"} {
		b, err := os.ReadFile("/usr/share/fonts/TTF/JetBrainsMonoNerdFont-" + style + ".ttf")
		if err != nil {
			log.Fatal(err)
		}
		f, err := opentype.Parse(b)
		if err != nil {
			log.Fatal(err)
		}
		faces = append(faces, font.FontFace{Font: f.Font(), Face: f})
	}
	hex := func(x uint32) color.NRGBA {
		return color.NRGBA{R: uint8(x >> 16), G: uint8(x >> 8), B: uint8(x), A: 0xff}
	}
	// aide's xterm palette from TerminalPane.tsx.
	ansi := []uint32{
		0x0d0d0d, 0xff6161, 0x59d499, 0xffc533, 0x57c1ff, 0xbb9af7, 0x7dcfff, 0xcdcdcd,
		0x242728, 0xff6161, 0x59d499, 0xffc533, 0x57c1ff, 0xbb9af7, 0x7dcfff, 0xffffff,
	}
	th := &theme.Theme{
		// System fonts stay enabled for emoji and CJK fallback.
		Shaper:   text.NewShaper(text.WithCollection(faces)),
		MonoFont: faces[0].Font,
		MonoSize: unit.Sp(*size),
		TermFg:   hex(0xf4f4f6),
		TermBg:   hex(0x07080a),
		TermCur:  hex(0xffffff),
	}
	for i, c := range ansi {
		th.ANSI[i] = hex(c)
	}
	return th
}

type writer struct {
	g    *vt.Grid
	x, y int
	fg   vt.Color
	bg   vt.Color
	at   vt.Attr
}

func (w *writer) put(s string) *writer {
	for _, r := range s {
		width := 1
		if wide(r) {
			width = 2
		}
		if w.x+width > w.g.Cols {
			break
		}
		i := w.y*w.g.Cols + w.x
		w.g.Cells[i] = vt.Cell{Content: string(r), Width: uint8(width), FG: w.fg, BG: w.bg, Attrs: w.at}
		if width == 2 {
			w.g.Cells[i+1] = vt.Cell{BG: w.bg, Attrs: w.at}
		}
		w.x += width
	}
	return w
}

func (w *writer) style(fg, bg vt.Color, at vt.Attr) *writer {
	w.fg, w.bg, w.at = fg, bg, at
	return w
}

func (w *writer) nl() *writer {
	w.x, w.y = 0, w.y+1
	return w.style(0, 0, 0)
}

func wide(r rune) bool {
	return r >= 0x1100 && r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 ||
		r >= 0xf900 && r <= 0xfaff || r >= 0xff00 && r <= 0xff60 || r >= 0x1f300 && r <= 0x1faff
}

func pal(i int) vt.Color { return vt.PaletteFlag | vt.Color(i) }
func rgb(r, g, b float64) vt.Color {
	return vt.RGBFlag | vt.Color(uint8(r*255))<<16 | vt.Color(uint8(g*255))<<8 | vt.Color(uint8(b*255))
}

func blank(cols, rows int) *vt.Grid {
	g := &vt.Grid{Cols: cols, Rows: rows, Cells: make([]vt.Cell, cols*rows)}
	for i := range g.Cells {
		g.Cells[i] = vt.Cell{Content: " ", Width: 1}
	}
	return g
}

func fixture() *vt.Grid {
	g := blank(120, 40)
	w := &writer{g: g}
	prompt := func() {
		w.style(pal(2), 0, vt.Bold).put("dev@workstation").style(0, 0, 0).put(":")
		w.style(pal(4), 0, vt.Bold).put("~/src/acme-api").style(pal(5), 0, 0).put("  rate-limit")
		w.style(pal(3), 0, 0).put(" ✚1").style(pal(2), 0, vt.Bold).put(" ❯ ").style(0, 0, 0)
	}
	prompt()
	w.put("ls -la --color").nl()
	w.style(pal(4), 0, vt.Bold).put("cmd").style(0, 0, 0).put("  ")
	w.style(pal(4), 0, vt.Bold).put("internal").style(0, 0, 0).put("  go.mod  go.sum  ")
	w.style(pal(2), 0, vt.Bold).put("acme-api").style(0, 0, 0).put("  AGENTS.md  ")
	w.style(pal(6), 0, 0).put("README -> AGENTS.md").nl().nl()

	for _, row := range []string{
		"┌──────────────┬──────────┬─────────┬──────────────────┐",
		"│ agent        │ status   │ tokens  │ branch           │",
		"├──────────────┼──────────┼─────────┼──────────────────┤",
		"│ claude       │ busy     │ 12.4k   │ rate-limit       │",
		"│ codex        │ idle     │ 3.1k    │ key-rotation     │",
		"└──────────────┴──────────┴─────────┴──────────────────┘",
	} {
		w.style(pal(8+7), 0, 0).put(row).nl()
	}
	w.y-- // color the status column
	for y, c := range map[int]vt.Color{w.y - 2: pal(3), w.y - 1: pal(8)} {
		for x := 17; x < 25; x++ {
			g.Cells[y*g.Cols+x].FG = c
		}
	}
	w.nl().nl()

	for _, s := range []struct {
		t  string
		at vt.Attr
	}{
		{"bold", vt.Bold}, {"italic", vt.Italic}, {"bold italic", vt.Bold | vt.Italic},
		{"faint", vt.Faint}, {"underline", vt.Underline}, {"strike", vt.Strike},
		{"reverse", vt.Reverse}, {"invisible", vt.Invisible},
	} {
		w.style(0, 0, s.at).put(s.t).style(0, 0, 0).put("  ")
	}
	w.style(pal(1), 0, vt.Underline).put("red underline").nl().nl()

	for i := range 16 {
		fg := pal(0)
		if i == 0 || i == 8 {
			fg = pal(15)
		}
		w.style(fg, pal(i), 0).put(fmt.Sprintf(" %2d ", i))
	}
	w.nl()
	for i := 16; i < 256; i++ {
		if (i-16)%108 == 0 && i > 16 {
			w.nl()
		}
		w.style(0, pal(i), 0).put(" ")
	}
	w.nl().nl()
	for x := range g.Cols {
		h := float64(x) / float64(g.Cols)
		r, gg, b := hsv(h)
		r2, g2, b2 := hsv(math.Mod(h+0.5, 1))
		w.style(rgb(r2, g2, b2), rgb(r, gg, b), 0).put("▀")
	}
	w.nl().nl()

	w.put("CJK: ").put("漢字テスト 한국어").put("   emoji: ").put("🚀🎉🦀😀").put("   nerd: ")
	w.style(pal(5), 0, 0).put("  \U000f02a2   ").nl()
	w.style(pal(4), 0, 0).put(" main ").style(pal(4), pal(0), 0).put("").style(0, 0, 0).put(" powerline ")
	w.style(pal(3), 0, vt.Italic).put("// ligatures off: -> => != ===").nl().nl()

	prompt()
	w.put("go test ./...")
	g.Cursor = vt.Cursor{X: w.x, Y: w.y, Visible: true}
	return g
}

func hsv(h float64) (r, g, b float64) {
	i, f := math.Floor(h*6), h*6-math.Floor(h*6)
	switch int(i) % 6 {
	case 0:
		return 1, f, 0
	case 1:
		return 1 - f, 1, 0
	case 2:
		return 0, 1, f
	case 3:
		return 0, 1 - f, 1
	case 4:
		return f, 0, 1
	}
	return 1, 0, 1 - f
}

// dense is a 250x70 screen of colored code-like text that scrolls one line
// per frame and rewrites one cell per row, so most rows miss the cache.
func dense(cols, rows, frame int) *vt.Grid {
	g := blank(cols, rows)
	for y := range rows {
		for x := range cols {
			n := (y+frame)*cols + x
			c := &g.Cells[y*cols+x]
			if n%7 != 6 {
				c.Content = string(rune('a' + (n*31+frame*(y%3))%26))
			}
			c.FG = pal(1 + (n/7)%7)
			if (n/7)%11 == 0 {
				c.Attrs = vt.Bold
			}
		}
	}
	g.Cursor = vt.Cursor{Visible: true}
	return g
}
