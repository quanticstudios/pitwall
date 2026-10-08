package term

import (
	"image"
	"image/color"
	"testing"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestFindHighlight(t *testing.T) {
	v := &View{}
	th := testTheme()
	th.Yellow = color.NRGBA{R: 0xee, G: 0xcc, A: 0xff}
	g := grid("error here", "no match", "Error error")
	draw := func() { v.Layout(testContext(image.Pt(400, 300)), th, g, vt.Modes{}, true) }
	// row is row y's image as drawn with the current match at column cur.
	row := func(y, cur int) *image.RGBA {
		t.Helper()
		cells := g.Cells[y*g.Cols : (y+1)*g.Cols]
		v.found = v.found[:0]
		if v.find != nil {
			v.found = v.find.Row(v.found, 0, cells)
		}
		r := v.prev[v.hashRow(cells, -1, -1, nil, cur)] // drawRows swaps the frame's rows into prev
		if r == nil {
			t.Fatalf("row %d with the current match at %d was not drawn", y, cur)
		}
		return r.img
	}
	bg := func(img *image.RGBA, col int) color.NRGBA {
		c := img.RGBAAt(col*v.cell.X+1, 1)
		return color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A}
	}
	soft := theme.Mix(th.TermBg, th.Yellow, 0.3)

	v.SetFind("error", image.Pt(6, 2))
	draw()
	if c := bg(row(0, -1), 0); c != soft {
		t.Errorf("a match: %v, want %v", c, soft)
	}
	if c := bg(row(0, -1), 6); c != th.TermBg {
		t.Errorf("after a match: %v", c)
	}
	if c := bg(row(2, 6), 0); c != soft {
		t.Errorf("lower case matches Error: %v", c)
	}
	if c := bg(row(2, 6), 6); c != th.Yellow {
		t.Errorf("the current match: %v, want %v", c, th.Yellow)
	}

	v.SetFind("Error", image.Pt(0, -1))
	draw()
	if c := bg(row(2, -1), 6); c != th.TermBg {
		t.Errorf("a capital turns case on: %v", c)
	}

	v.SetFind("", image.Pt(0, -1))
	draw()
	if c := bg(row(0, -1), 0); c != th.TermBg {
		t.Errorf("closed: %v", c)
	}
}
