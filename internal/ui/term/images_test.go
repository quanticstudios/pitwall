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

// TestImagePixels prints a PNG through the emulator, as kitten icat
// does, and checks the pane draws it in the cells it was placed at,
// scaled to them, and the selection tints it.
func TestImagePixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 40, 20)) // red left half, blue right
	for y := range 20 {
		for x := range 40 {
			c := color.NRGBA{R: 0xff, A: 0xff}
			if x >= 20 {
				c = color.NRGBA{B: 0xff, A: 0xff}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		t.Fatal(err)
	}
	e := vt.New(20, 5, nil)
	e.Write([]byte("hi\r\n  \x1b_Ga=T,f=100,c=4,r=2;" + base64.StdEncoding.EncodeToString(b.Bytes()) + "\x1b\\"))
	g := e.Snapshot()
	if len(g.Images) != 1 {
		t.Fatalf("placements %+v", g.Images)
	}

	w, err := headless.NewWindow(300, 150)
	if err != nil {
		t.Skip(err)
	}
	defer w.Release()
	th := testTheme()
	v := View{Images: e.(interface {
		Image(uint64) (vt.Image, bool)
	}).Image}
	shot := func() *image.RGBA {
		gtx := testContext(image.Pt(300, 150))
		v.Layout(gtx, th, &g, vt.Modes{}, false)
		if err := w.Frame(gtx.Ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 300, 150))
		if err := w.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		return img
	}
	img := shot()
	cell := func(x, y int) color.RGBA {
		return img.RGBAAt(v.pad+x*v.cell.X+v.cell.X/2, v.pad+y*v.cell.Y+v.cell.Y/2)
	}
	red, blue, bg := color.RGBA{R: 0xff, A: 0xff}, color.RGBA{B: 0xff, A: 0xff}, color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{
		{2, 1, red}, {3, 2, red}, {4, 1, blue}, {5, 2, blue}, // the image, 4x2 cells from (2,1)
		{1, 1, bg}, {6, 1, bg}, {2, 3, bg}, // around it
	} {
		if got := cell(c.x, c.y); got != c.want {
			t.Errorf("cell (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}

	v.sel, v.selOn, v.selCols = vt.Selection{A: vt.Pos{Line: 1, Col: 2}, B: vt.Pos{Line: 1, Col: 2}}, true, g.Cols
	img = shot()
	if got := cell(2, 1); got.R == 0xff && got.G == 0 {
		t.Errorf("selected image cell = %v, want it tinted", got)
	}
	if got := cell(3, 1); got != red {
		t.Errorf("unselected image cell = %v, want red", got)
	}
}
