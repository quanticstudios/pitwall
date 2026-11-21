package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestKeysShots draws the composer's new states offscreen, for review:
// PITWALL_KEYS_SHOTS=<dir> go test -run TestKeysShots ./internal/ui/chatin
func TestKeysShots(t *testing.T) {
	dir := os.Getenv("PITWALL_KEYS_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_KEYS_SHOTS is not set")
	}
	pic := pngOf(160, 120)
	lc, _ := config.Builtin("aide-light")
	light, err := theme.New(lc, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	for _, th := range []struct {
		name string
		th   *theme.Theme
	}{{"dark", theme.Dark()}, {"light", light}} {
		// The stash menu over three drafts, the newest lit.
		h := newHarness(t)
		h.now = at
		h.c.SetStashes([]Stash{
			{Text: "Rename the option to queue_file and update docs/config.md", At: at.Add(-2 * time.Minute)},
			{Text: "Look at these two [Image #1] [Image #2] and match the spacing", Attachments: []session.Attachment{imageAtt(1, pic), imageAtt(2, pic)}, At: at.Add(-3 * time.Hour)},
			{Text: "Add a test for the refused send coming back into the box", At: at.Add(-50 * time.Hour)},
		})
		h.frame()
		h.press("S", key.ModCtrl)
		h.shoot(filepath.Join(dir, "stash-menu-"+th.name+".png"), th.th, 360)

		// Alt+Up editing the newest queued message: its row lit, Save.
		h = newHarness(t)
		h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "Also run the linter before you push"}, session.QueuedInput{ID: "b", Text: "And update the CHANGELOG line"})
		h.snap.Model = ptr("claude-opus-5-5")
		h.typ("my draft")
		h.c.ed.SetCaret(0, 0)
		h.frame()
		h.press(key.NameUpArrow, key.ModAlt)
		h.typ(" for v0.3.2")
		h.shoot(filepath.Join(dir, "queue-edit-"+th.name+".png"), th.th, 260)

		// follow_up = "steer": Steer while the agent works, Stop beside it.
		h = newHarness(t)
		h.opts = func(e *Env) { e.Steer, e.Steers = true, true }
		h.snap.SessionState = session.StateRunning
		h.snap.Model = ptr("claude-opus-5-5")
		h.typ("Use the existing helper instead")
		h.shoot(filepath.Join(dir, "steer-"+th.name+".png"), th.th, 160)

		// Up on an empty box: the last prompt back, with its image, and the
		// stash chip.
		h = newHarness(t)
		h.c.SetStashes([]Stash{{Text: "later", At: at}, {Text: "much later", At: at}})
		h.opts = func(e *Env) {
			e.History = func() []Sent {
				return []Sent{{ID: "p", Text: "Why does the header jump here? [Image #1]", Attachments: []session.Attachment{imageAtt(1, pic)}}}
			}
		}
		h.snap.Model = ptr("claude-opus-5-5")
		h.frame()
		h.press(key.NameUpArrow, 0)
		h.shoot(filepath.Join(dir, "recall-"+th.name+".png"), th.th, 240)
	}
}

// shoot draws the composer at 2x, at the bottom of a w by hgt dp area as
// a chat draws it, with what it shows above the card, to path.
func (h *harness) shoot(path string, th *theme.Theme, hgt int) {
	h.t.Helper()
	const scale, w = 2, 760
	size := image.Pt(w*scale, hgt*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		h.t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var ops op.Ops
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: h.r.Source(), Now: h.now, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size)}
		paint.Fill(gtx.Ops, th.Bg)
		gtx.Constraints.Min.Y = 0
		env := h.env()
		env.Width = w
		m := op.Record(gtx.Ops)
		d := h.c.Layout(gtx, th, env)
		call := m.Stop()
		o := op.Offset(image.Pt(0, size.Y-d.Size.Y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		h.r.Frame(&ops)
		h.c.Messages()
	}
	if err := win.Frame(&ops); err != nil {
		h.t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		h.t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		h.t.Fatal(err)
	}
}
