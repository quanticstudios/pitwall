package panel

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestRowFillRounded: a row's Hover and Pressed fills keep clear of its
// sides by 2dp and of its corners, and fill the rest.
func TestRowFillRounded(t *testing.T) {
	th := theme.Dark()
	size := image.Pt(200, 28)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	var c widget.Clickable
	mid := f32.Pt(100, 14)
	frame := func() *image.RGBA {
		for range 3 {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(size)}
			paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
			c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				rowFill(gtx, th, &c, size)
				return layout.Dimensions{Size: size}
			})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		return img
	}
	check := func(state string, img *image.RGBA, fill color.NRGBA) {
		t.Helper()
		for _, p := range []struct {
			at   image.Point
			want color.NRGBA
		}{
			{image.Pt(0, 14), th.Bg},   // the side inset
			{image.Pt(2, 0), th.Bg},    // outside the top-left arc
			{image.Pt(197, 27), th.Bg}, // outside the bottom-right arc
			{image.Pt(2, 14), fill},    // the fill's left edge
			{image.Pt(100, 0), fill},   // its top edge
			{image.Pt(100, 14), fill},  // its middle
		} {
			if got := img.RGBAAt(p.at.X, p.at.Y); !near(got, p.want) {
				t.Errorf("%s: pixel %v = %v, want %v", state, p.at, got, p.want)
			}
		}
	}
	check("idle", frame(), th.Bg) // also registers the row's pointer handler
	r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: mid})
	check("hover", frame(), th.Hover)
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: mid})
	check("pressed", frame(), th.Pressed)
}

func near(a color.RGBA, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return x-y < 3 || y-x < 3 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

// TestRoundedHoverShots writes a hovered tool row, a hovered changes card
// row and the ask card's step error to $PITWALL_RENDER_DIR, for a look at
// them; it skips without it.
func TestRoundedHoverShots(t *testing.T) {
	dir := os.Getenv("PITWALL_RENDER_DIR")
	if dir == "" {
		t.Skip("PITWALL_RENDER_DIR is not set")
	}
	s := toolChat()
	tr := &Transcript{Dir: "/repo"}
	tr.init()
	tr.open[logID(s, blocks(s.Content, nil)[1])] = true
	shootPointer(t, tr, s, filepath.Join(dir, "tool-row-hover.png"), image.Pt(1200, 600), 1.5, image.Pt(500, 210))
	shootPointer(t, tr, s, filepath.Join(dir, "card-row-hover.png"), image.Pt(1200, 600), 1.5, image.Pt(500, 490))

	d := newDesk(t, askSnap(threeQuestions()...))
	d.tr.StepError("i1", `Claude Code is on "│ Codex chat tabs don't exist on a real daemon: configuredProviders never registers them", not "Which accent color should we use?"; stopped without guessing`)
	d.frame()
	d.shot("step-error")
}
