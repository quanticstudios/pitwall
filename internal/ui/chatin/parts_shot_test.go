package chatin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestContextShots draws the composer with each kind of context to
// $PITWALL_CONTEXT_SHOTS: an image, a quote with its comment, a terminal
// excerpt and a pasted file, in both themes; the question removing a
// named image asks; and a quote's comment field. It skips without it.
func TestContextShots(t *testing.T) {
	dir := os.Getenv("PITWALL_CONTEXT_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_CONTEXT_SHOTS is not set")
	}
	fill := func(c *Composer) {
		c.SetText("")
		c.AddImage(gradient(320, 200))
		c.ed.Insert("this layout, ")
		c.AddQuote("The check compared against w-1, so the cursor wrapped one column early.", Source{Item: "a1"})
		c.draft.Part(PartQuote, 1).Comment = "does this also hit tabs?"
		c.ed.Insert("and the failure ")
		n := c.AddTerminal("--- FAIL: TestWrap (0.00s)\n    wrap_test.go:41: cursor at 79, want 80\nFAIL\tgithub.com/x/vt\t0.012s", "")
		c.LabelTerminal(n, "go test ./internal/vt")
		c.ed.Insert("with the log ")
		c.files = true
		c.foldPaste("2026-11-21T19:02:11Z INFO starting worker pool size=8\n" + strings.Repeat("2026-11-21T19:02:12Z DEBUG tick\n", 1400))
	}
	shot := func(name string, th *theme.Theme, edit func(c *Composer)) {
		const scale = 2
		size := image.Pt(900*scale, 330*scale)
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		var c Composer
		var r input.Router
		var ops op.Ops
		snap := session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Provider: "claude"}
		env := Env{Chat: "c1", Snap: &snap, Width: 860, Focus: true, Images: true, TextFiles: true}
		for i := range 4 {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Date(2026, 11, 21, 14, 0, 0, i, time.UTC),
				Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
			paint.Fill(gtx.Ops, th.Bg)
			gtx.Constraints.Min.Y = 0
			if i == 1 {
				fill(&c)
				if edit != nil {
					edit(&c)
				}
			}
			c.Layout(gtx, th, env)
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	shot("composer-context-dark.png", theme.Dark(), nil)
	lc, _ := config.Builtin("aide-light")
	light, err := theme.New(lc, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	shot("composer-context-light.png", light, nil)
	shot("composer-remove-asks.png", theme.Dark(), func(c *Composer) { c.askRemove(1) })
	shot("composer-quote-comment.png", theme.Dark(), func(c *Composer) {
		c.editComment("[Quote #1]")
		c.comment.SetText("does this also hit tabs?")
	})
}

// gradient is a w by h PNG of a blue to purple sweep, a picture that
// reads as one in a thumbnail.
func gradient(w, h int) []byte {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.Set(x, y, color.NRGBA{uint8(60 + 140*x/w), uint8(90 + 60*y/h), 220, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	return b.Bytes()
}
