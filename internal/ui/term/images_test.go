package term

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestImagePixels prints a PNG through the emulator, as kitten icat does,
// and checks the pane draws it where it was placed with its aspect ratio:
// at its own size with no c= or r=, fitted inside the cells with them.
// Then the selection tints it, and DropImages lets go of it.
func TestImagePixels(t *testing.T) {
	const size = 400
	w, err := headless.NewWindow(size, size/2)
	if err != nil {
		t.Skip(err)
	}
	defer w.Release()
	th := testTheme()
	var v View
	cell := v.CellSize(testContext(image.Pt(size, size/2)), th)
	cw, ch := cell.X, cell.Y

	// 3.5 cells wide, 1.5 high: red left half, blue right half.
	iw, ih := 3*cw+cw/2, ch+ch/2
	src := image.NewNRGBA(image.Rect(0, 0, iw, ih))
	for y := range ih {
		for x := range iw {
			c := color.NRGBA{R: 0xff, A: 0xff}
			if x >= iw/2 {
				c = color.NRGBA{B: 0xff, A: 0xff}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(b.Bytes())
	e := vt.New(30, 6, nil)
	e.(interface{ SetCellSize(w, h int) }).SetCellSize(cw, ch)
	// At (2,1) natural, 4x2 cells; at (0,3) in 8x1 cells, so a third
	// smaller and 2.3 cells wide.
	e.Write([]byte("hi\r\n  \x1b_Ga=T,f=100;" + enc + "\x1b\\\r\n\x1b_Ga=T,f=100,c=8,r=1,C=1;" + enc + "\x1b\\"))
	g := e.Snapshot()
	if len(g.Images) != 2 || !g.Images[0].Natural || g.Images[1].Natural || g.Images[1].Y != 3 {
		t.Fatalf("placements %+v", g.Images)
	}
	v.Images = e.(interface {
		Image(uint64) (vt.Image, bool)
	}).Image
	var img *image.RGBA
	shot := func() {
		gtx := testContext(image.Pt(size, size/2))
		v.Layout(gtx, th, &g, vt.Modes{}, false)
		if err := w.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		img = image.NewRGBA(image.Rect(0, 0, size, size/2))
		if err := w.Screenshot(img); err != nil {
			t.Fatal(err)
		}
	}
	// at is the pixel dx, dy from cell (col, row)'s top left.
	at := func(col, row, dx, dy int) color.RGBA {
		return img.RGBAAt(v.pad+col*cw+dx, v.pad+row*ch+dy)
	}
	shot()
	red, blue, bg := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{B: 0xff, A: 0xff}, color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	small := iw * 2 / 3 // the fitted image's width
	for _, c := range []struct {
		name             string
		col, row, dx, dy int
		want             color.RGBA
	}{
		{"natural, top left", 2, 1, 2, 2, red},
		{"natural, bottom right", 2, 1, iw - 3, ih - 3, blue},
		{"natural, right of it in its cells", 2, 1, iw + 2, 2, bg},
		{"natural, below it in its cells", 2, 1, 2, ih + 2, bg},
		{"left of the image", 1, 1, cw / 2, 2, bg},
		{"fitted, top left", 0, 3, 2, 2, red},
		{"fitted, right end", 0, 3, small - 3, ch - 3, blue},
		{"fitted, right of it in its cells", 0, 3, small + 3, ch / 2, bg},
	} {
		if got := at(c.col, c.row, c.dx, c.dy); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	v.sel, v.selOn, v.selCols = vt.Selection{A: vt.Pos{Line: 1, Col: 2}, B: vt.Pos{Line: 1, Col: 2}}, true, g.Cols
	shot()
	if got := at(2, 1, 2, 2); got == red {
		t.Errorf("selected image cell = %v, want it tinted", got)
	}
	if got := at(3, 1, 2, 2); got != red {
		t.Errorf("unselected image cell = %v, want red", got)
	}

	if len(v.pics) != 2 {
		t.Fatalf("%d image ops for two images", len(v.pics))
	}
	v.DropImages()
	if len(v.pics) != 0 {
		t.Error("DropImages kept the image ops")
	}
}
