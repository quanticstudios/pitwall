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
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestJoinSteerShots renders the composer mid-turn for the PR: a message
// Enter just queued, shown before the chat lists it, each mode's
// placeholder and hint, and the note for an agent that can't steer. Set
// PITWALL_QUEUE_SHOTS to a folder.
func TestJoinSteerShots(t *testing.T) {
	dir := os.Getenv("PITWALL_QUEUE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_QUEUE_SHOTS is not set")
	}
	at := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)
	running := func(qs ...session.QueuedInput) session.Snapshot {
		return session.Snapshot{SessionID: "c1", SessionState: session.StateRunning, QueuedInputs: qs}
	}
	first := session.QueuedInput{ID: "a", Text: "Also run the linter before you push"}
	for _, s := range []struct {
		name string
		snap session.Snapshot
		env  func(*Env)
		prep func(*Composer, Env)
	}{
		{"queued-at-once", running(first), nil, func(c *Composer, e Env) {
			c.queuedNow(proto.ChatSend{Text: "And update the CHANGELOG line"}, at)
		}},
		{"queue-empty", running(), nil, nil},
		{"steer-mode", running(first), func(e *Env) { e.Steer = true }, nil},
		{"cant-steer", running(first), func(e *Env) { e.Steers = false }, func(c *Composer, e Env) {
			c.note, c.noteOK = "This agent takes nothing mid-turn: queued, it goes when the turn ends", true
		}},
	} {
		env := Env{Chat: "c1", Snap: &s.snap, Width: 720, Now: true, Queue: true, Separate: true, Steers: true, Join: true}
		if s.env != nil {
			s.env(&env)
		}
		shootSteer(t, env, at, s.prep, filepath.Join(dir, "steer-"+s.name+".png"))
	}
}

func shootSteer(t *testing.T, env Env, at time.Time, prep func(*Composer, Env), path string) {
	t.Helper()
	const scale = 2
	size := image.Pt(720*scale, 200*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var c Composer
	if prep != nil {
		prep(&c, env)
	}
	var r input.Router
	var ops op.Ops
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: at,
			Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		paint.Fill(gtx.Ops, theme.Dark().Bg)
		gtx.Constraints.Min.Y = 0
		c.Layout(gtx, theme.Dark(), env)
		r.Frame(&ops)
	}
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
