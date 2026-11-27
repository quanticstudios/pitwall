package main

import (
	"image"
	"image/png"
	"os"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// shot draws the sidebar offscreen at 1px per dp, w by h, clicking each
// of clicks (in dp) between frames, and writes the last frame to path as
// a PNG. No window opens.
func shot(th *theme.Theme, sb *sidebar.Sidebar, st model.State, active, path string, w, h int, clicks []f32.Point) error {
	win, err := headless.NewWindow(w, h)
	if err != nil {
		return err
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	now := time.Now()
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now,
			Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h))}
		paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(w, h)}.Op())
		_, events := sb.Layout(gtx, th, &st, "", active)
		for _, ev := range events {
			apply(&st, &active, ev)
		}
		r.Frame(&ops)
	}
	frame()
	for _, p := range clicks {
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
		frame()
		r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		frame()
		// Move away so no hover shows in the shot.
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(w)+50, float32(h)+50)})
		frame()
	}
	// Past every hover and fold animation, on a whole pulse: dots at full color.
	for range 2 {
		now = now.Add(time.Second)
		frame()
	}
	if err := win.Frame(&ops); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rectangle{Max: image.Pt(w, h)})
	if err := win.Screenshot(img); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
