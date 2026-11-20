package panel

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The prompt's hover buttons take real pointer events: hovering the row
// shows them, and a click on Copy copies and one on Edit asks for the
// composer.
func TestPromptToolsClick(t *testing.T) {
	th := theme.Dark()
	turn := "t1"
	s := &session.Snapshot{Content: []session.Item{
		{ID: "p", Kind: session.KindUserPrompt, Text: "fix the bug", TurnID: &turn},
		{ID: "a", Kind: session.KindAssistantText, Text: "done", TurnID: &turn},
	}}
	var tr Transcript
	var copied string
	tr.Copy = func(_ layout.Context, text string) { copied = text }
	var r input.Router
	var ops op.Ops
	now := time.Now()
	frame := func(evs ...pointer.Event) {
		for _, e := range evs {
			r.Queue(e)
		}
		ops.Reset()
		now = now.Add(time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now, Source: r.Source()}
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
	}
	move := func(x, y int) pointer.Event {
		return pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(x), float32(y))}
	}
	frame()
	frame(move(990, 30))
	frame(move(991, 31))
	// Find the Copy button along the row's bottom.
	at := image.Point{-1, -1}
	for x := 0; x < 1000 && at.X < 0; x += 4 {
		for _, y := range []int{30, 40, 50} {
			frame(move(x, y))
			if c := tr.clicks["msg:copy:p"]; c != nil && c.Hovered() {
				at = image.Pt(x, y)
				break
			}
		}
	}
	if at.X < 0 {
		t.Fatal("no Copy button under the pointer anywhere on the row")
	}
	p := f32.Pt(float32(at.X), float32(at.Y))
	frame(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
	frame(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	frame()
	if copied != "fix the bug" || !tr.isCopied("prompt:p") {
		t.Errorf("Copy at %v copied %q, shows Copied %v", at, copied, tr.isCopied("prompt:p"))
	}
}
