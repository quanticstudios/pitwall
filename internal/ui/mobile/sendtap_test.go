package mobile

import (
	"context"
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestSendTapSends checks a tap on the send button sends the draft, with
// the Stop check on the same button reading that tap first.
func TestSendTapSends(t *testing.T) {
	c, env, _ := demoChat("ready")
	calls := 0
	env.Do = func(call func(context.Context, chatAPI) error, done func(error)) {
		calls++
		done(call(context.Background(), demoAPI{}))
	}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(360, 800))}
	c.composer(gtx, Theme(), env)
	c.cmp.send.Click()
	c.composer(gtx, Theme(), env)
	if calls != 1 {
		t.Fatalf("the tap made %d calls, want 1 send", calls)
	}
}
