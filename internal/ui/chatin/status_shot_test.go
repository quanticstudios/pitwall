package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestStatusLineShots renders the Ledger composer with the agent's spinner
// row, and with none, in its status line to $PITWALL_TOOL_SHOTS. It skips
// without the env var.
func TestStatusLineShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	mdl, eff := "claude-opus-5-5", "high"
	for name, live := range map[string][]session.Item{
		"status-spinner.png": {spin("t1", "Flibbertigibbeting… 11s · ↓ 789 tokens · thinking")},
		"status-working.png": nil,
	} {
		const scale = 2
		size := image.Pt(780*scale, 150*scale)
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		th := theme.Dark()
		var c Composer
		var ops op.Ops
		snap := session.Snapshot{SessionID: "c1", SessionState: session.StateRunning, Running: true, Provider: "claude",
			Model: &mdl, Effort: &eff, PermissionMode: ModeDefault, Live: live}
		env := Env{Chat: "c1", Snap: &snap, Width: 720, Ledger: true, Commands: true}
		for i := range 3 {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Now: time.Date(2026, 11, 27, 13, 0, i, 0, time.UTC),
				Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
			paint.Fill(gtx.Ops, th.Bg)
			gtx.Constraints.Min.Y = 0
			c.Layout(gtx, th, env)
			if err := win.Frame(&ops); err != nil {
				t.Fatal(err)
			}
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
