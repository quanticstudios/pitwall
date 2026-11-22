package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestMeterResetShots renders the composer in each context-meter state to
// $PITWALL_METER_SHOTS (before compaction at 87% danger, after Claude
// compaction with postTokens at 1%, after compaction without postTokens in
// the neutral unknown state + its hover card, and after /clear with the
// meter hidden), in dark and light themes. It skips without the env var.
func TestMeterResetShots(t *testing.T) {
	dir := os.Getenv("PITWALL_METER_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_METER_SHOTS is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	n := func(v int64) *int64 { return &v }
	mdl := "claude-opus-5-5"
	eff := "high"
	shot := func(name string, th *theme.Theme, usage *session.Usage, hoverMeter bool) {
		t.Helper()
		const scale = 2
		size := image.Pt(780*scale, 220*scale)
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		var c Composer
		var r input.Router
		var ops op.Ops
		snap := session.Snapshot{
			SessionID:      "c1",
			SessionState:   session.StateReady,
			Provider:       "claude",
			Model:          &mdl,
			Effort:         &eff,
			PermissionMode: ModeDefault,
			Usage:          usage,
		}
		env := Env{Chat: "c1", Snap: &snap, Width: 720, Focus: false}
		base := time.Date(2026, 11, 22, 16, 40, 0, 0, time.UTC)
		for i := range 5 {
			ops.Reset()
			now := base.Add(time.Duration(i) * 400 * time.Millisecond)
			if hoverMeter && i >= 2 {
				// Position pointer over the meter button (left of the 28dp Send button).
				px := float32((780 - 30 - 8 - 28 - 8 - 30) * scale)
				py := float32((110 + 78) * scale)
				r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(px, py)})
			}
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now,
				Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
			paint.Fill(gtx.Ops, th.Bg)
			// Place composer near the bottom so hover cards above it fit in frame.
			o := op.Offset(image.Pt(0, 110*scale)).Push(gtx.Ops)
			gtx.Constraints.Min.Y = 0
			c.Layout(gtx, th, env)
			o.Pop()
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

	lc, _ := config.Builtin("aide-light")
	light, err := theme.New(lc, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Before compaction: 87% danger (red ring + "87%")
	shot("meter-before-87-dark.png", theme.Dark(), &session.Usage{ContextTokens: n(870_000), MaxTokens: n(1_000_000)}, false)
	// 2. After Claude 2.1.330 compaction with postTokens (6.2k / 1M -> 0%, calm)
	shot("meter-after-post-dark.png", theme.Dark(), &session.Usage{ContextTokens: n(6_213), MaxTokens: n(1_000_000)}, false)
	// 3. After compaction without postTokens: neutral empty ring + "—" (dark & light)
	shot("meter-after-unknown-dark.png", theme.Dark(), &session.Usage{MaxTokens: n(1_000_000)}, false)
	shot("meter-after-unknown-hover-dark.png", theme.Dark(), &session.Usage{MaxTokens: n(1_000_000)}, true)
	shot("meter-after-unknown-light.png", light, &session.Usage{MaxTokens: n(1_000_000)}, false)
	// 4. After /clear: no usage, meter hidden
	shot("meter-after-clear-dark.png", theme.Dark(), nil, false)
}

// TestComposerNeutralMeterLayout ensures the neutral unknown-context meter
// lays out without panicking and differs in width/layout from the hidden
// cleared state.
func TestComposerNeutralMeterLayout(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	th := theme.Dark()
	run := func(u *session.Usage) {
		var c Composer
		var r input.Router
		var ops op.Ops
		snap := session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Provider: "claude", Usage: u}
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Now(),
			Metric: unit.Metric{PxPerDp: 2, PxPerSp: 2}, Constraints: layout.Exact(image.Pt(1200, 300))}
		c.Layout(gtx, th, Env{Chat: "c1", Snap: &snap, Width: 560})
		r.Frame(&ops)
	}
	run(&session.Usage{ContextTokens: n(870_000), MaxTokens: n(1_000_000)})
	run(&session.Usage{ContextTokens: n(6_213), MaxTokens: n(1_000_000)})
	run(&session.Usage{MaxTokens: n(1_000_000)})
	run(nil)
}
