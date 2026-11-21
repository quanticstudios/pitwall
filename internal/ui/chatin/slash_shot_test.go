package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestSlashShots draws the composer with its / menu open into
// PITWALL_SLASH_SHOTS: the whole list, a filtered one, a terminal-only
// row highlighted, and the light theme.
func TestSlashShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SLASH_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SLASH_SHOTS is not set")
	}
	cmds := append([]session.Command{
		{Name: "ship", Description: "Push the branch and open the pull request", Source: "user"},
		{Name: "frontend:component", Description: "Scaffold a component with its test", Source: "project"},
	}, claude.Commands("", "")...)
	light, _ := config.Builtin("aide-light")
	lt, _ := theme.New(light, config.Font{})
	for _, s := range []struct {
		name, text string
		downs      int
		th         *theme.Theme
	}{
		{"slash-open.png", "/", 0, theme.Dark()},
		{"slash-filtered.png", "/co", 0, theme.Dark()},
		{"slash-terminal.png", "/co", 3, theme.Dark()},
		{"slash-light.png", "/re", 0, lt},
	} {
		shootComposer(t, filepath.Join(dir, s.name), s.th, cmds, s.text, s.downs)
	}
}

// shootComposer draws a composer at the foot of a 900x560 dp window at
// scale 2 with text typed and Down pressed downs times, and writes it.
func shootComposer(t *testing.T, path string, th *theme.Theme, cmds []session.Command, text string, downs int) {
	t.Helper()
	const scale = 2
	size := image.Pt(900*scale, 560*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	var c Composer
	snap := session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Provider: "claude", Commands: cmds}
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	frame := func() {
		ops.Reset()
		now = now.Add(time.Second)
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now,
			Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
		gtx.Constraints.Min.Y = 0
		m := op.Record(gtx.Ops)
		d := c.Layout(gtx, th, Env{Chat: "c1", Snap: &snap, Width: 720, Focus: true, Branch: "slash-menu"})
		call := m.Stop()
		o := op.Offset(image.Pt(0, size.Y-d.Size.Y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		r.Frame(&ops)
	}
	frame()
	frame()
	c.ed.Insert(text)
	frame()
	for range downs {
		r.Queue(key.Event{Name: key.NameDownArrow, State: key.Press})
		frame()
	}
	frame()
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
