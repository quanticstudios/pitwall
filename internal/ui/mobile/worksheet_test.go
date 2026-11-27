package mobile

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestWorkTitle(t *testing.T) {
	if got, want := workTitle(demoWorkers(time.Unix(0, 0))), "Work · 2 for you · 3 moving · 1 done"; got != want {
		t.Errorf("workTitle = %q, want %q", got, want)
	}
	if got := workTitle([]model.Worker{{Group: model.WorkBlocked}}); got != "Work · 1 blocked" {
		t.Errorf("workTitle = %q", got)
	}
}

// TestWorkSheetShots writes the chat at 360dp with the Work chip, and
// with the sheet open, to $PITWALL_SHEET_SHOTS; it skips without it.
func TestWorkSheetShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SHEET_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SHEET_SHOTS is not set")
	}
	th := Theme()
	const scale = 2
	for _, state := range []string{"following", "work", "work-sheet"} {
		c, env, _ := demoChat(state)
		size := image.Pt(360*scale, 760*scale)
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		for range 3 {
			ops.Reset()
			c.layout(layout.Context{Ops: &ops, Source: r.Source(), Now: env.Now, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}, th, env)
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, "phone-"+state+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
