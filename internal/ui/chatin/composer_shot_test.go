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

// TestComposerShots renders the composer, Ledger and Column, at 1440 dp
// wide in the dark theme: empty, typing, one and three queued, and the
// agent working, to $PITWALL_TOOL_SHOTS. It skips without the env var.
func TestComposerShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	mdl, eff := "claude-opus-5-5", "high"
	spinner := []session.Item{spin("t1", "Flibbertigibbeting… 11s · ↓ 789 tokens · thinking")}
	three := []session.QueuedInput{
		{ID: "q1", Text: "Then add a test for a refresh exactly at expiry."},
		{ID: "q2", Text: "After that, note the clock skew in AUTH.md."},
		{ID: "q3", Text: "Run the whole suite once more before you stop."},
	}
	for _, st := range []struct {
		name   string
		busy   bool
		typing string
		queue  []session.QueuedInput
		drag   float32 // the first row held this far down
	}{
		{"empty", false, "", nil, 0},
		{"typing", false, "Refresh the token a minute before it expires, and log the skew", nil, 0},
		{"working", true, "", nil, 0},
		{"queued-1", true, "", three[:1], 0},
		{"queued-3", true, "", three, 0},
		{"dragging", true, "", three, 42},
	} {
		for _, ledger := range []bool{true, false} {
			const scale = 1
			size := image.Pt(1440*scale, 260*scale)
			win, err := headless.NewWindow(size.X, size.Y)
			if err != nil {
				t.Skip("no headless GPU:", err)
			}
			th := theme.Dark()
			var c Composer
			if st.drag != 0 {
				c.q.drag, c.q.dy = st.queue[0].ID, st.drag
			}
			if st.typing != "" {
				c.SetText(st.typing)
			}
			var ops op.Ops
			snap := session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Provider: "claude",
				Model: &mdl, Effort: &eff, PermissionMode: ModeDefault, QueuedInputs: st.queue}
			if st.busy {
				snap.SessionState, snap.Running, snap.Live = session.StateRunning, true, spinner
			}
			env := Env{Chat: "c1", Snap: &snap, Width: 760, Ledger: ledger, Commands: true, Queue: true, Steers: true, Join: true}
			var h int
			for i := range 3 {
				ops.Reset()
				gtx := layout.Context{Ops: &ops, Now: time.Date(2026, 11, 27, 13, 0, i, 0, time.UTC),
					Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
				paint.Fill(gtx.Ops, th.Bg)
				gtx.Constraints.Min.Y = 0
				h = c.Layout(gtx, th, env).Size.Y
				if err := win.Frame(&ops); err != nil {
					t.Fatal(err)
				}
			}
			img := image.NewRGBA(image.Rectangle{Max: size})
			if err := win.Screenshot(img); err != nil {
				t.Fatal(err)
			}
			win.Release()
			name := "composer-" + st.name
			if ledger {
				name += "-ledger"
			}
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img.SubImage(image.Rect(0, 0, size.X, min(size.Y, h+8)))); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
