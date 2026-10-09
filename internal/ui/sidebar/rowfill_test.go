package sidebar

import (
	"image/color"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
)

// TestRowFill: a hover eases the row's fill over 80ms, a state change over
// 160ms.
func TestRowFill(t *testing.T) {
	t0 := time.Unix(1000, 0)
	rest, hover, state := color.NRGBA{A: 255}, color.NRGBA{R: 100, A: 255}, color.NRGBA{G: 100, A: 255}
	var r rowState
	at := func(d time.Duration, target color.NRGBA, hovered bool) color.NRGBA {
		return r.fill(layout.Context{Ops: new(op.Ops), Now: t0.Add(d)}, target, hovered)
	}
	if got := at(0, rest, false); got != rest {
		t.Fatalf("first fill %v", got)
	}
	at(time.Millisecond, hover, true)
	if got := at(90*time.Millisecond, hover, true); got != hover {
		t.Errorf("hover fill after 90ms: %v, want %v", got, hover)
	}
	at(100*time.Millisecond, state, true)
	if got := at(190*time.Millisecond, state, true); got == state {
		t.Error("a state change finished within 90ms")
	}
	if got := at(300*time.Millisecond, state, true); got != state {
		t.Errorf("state fill after 200ms: %v", got)
	}
}
