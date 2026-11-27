package mobile

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
)

// TestUserEntryMarkdown draws the user's message as markdown when it has
// some and as typed when not, and writes before (as typed) and after PNGs
// to PITWALL_PROMPT_SHOTS.
func TestUserEntryMarkdown(t *testing.T) {
	const md = "Fix the **resize** in `image.go`:\n\n1. keep the ratio\n2. cap at `2048` px\n\n```go\nimg = fit(img, 2048)\n```\n\nSee [the spec](https://example.com/spec)."
	const plain = "plain words and /home/u/foo_bar_baz.go:12"
	th := Theme()
	c := newChat("p")
	win, err := headless.NewWindow(420, 560)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	draw := func(name string, w layout.Widget) {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(420, 560))}
		paint.Fill(gtx.Ops, th.Bg)
		gtx.Constraints.Min = image.Point{}
		layout.UniformInset(12).Layout(gtx, w)
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		dir := os.Getenv("PITWALL_PROMPT_SHOTS")
		if dir == "" {
			return
		}
		img := image.NewRGBA(image.Rect(0, 0, 420, 560))
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
	draw("phone-prompt-before.png", func(gtx C) D { return prompt(gtx, th, md, 1) })
	draw("phone-prompt-after.png", func(gtx C) D { return c.entry(gtx, th, flow.Entry{Seq: 1, Kind: flow.EntryUser, Text: md}) })
	draw("phone-prompt-plain.png", func(gtx C) D { return c.entry(gtx, th, flow.Entry{Seq: 2, Kind: flow.EntryUser, Text: plain}) })
	if r := c.entryRows[1]; r == nil || r.flat || len(r.doc.Blocks()) < 4 {
		t.Fatalf("markdown message drew as typed: %+v", r)
	}
	if r := c.entryRows[2]; r == nil || !r.flat {
		t.Fatalf("plain message drew as markdown")
	}
}
