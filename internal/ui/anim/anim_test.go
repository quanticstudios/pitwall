package anim

import (
	"image/color"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
)

func TestValue(t *testing.T) {
	t0 := time.Unix(1000, 0)
	v := Value{From: 0, To: 10, Start: t0, Dur: 100 * time.Millisecond}
	for _, c := range []struct {
		at      time.Duration
		want    float32
		running bool
	}{
		{0, 0, true},
		{50 * time.Millisecond, 8.75, true}, // ease-out: 1-(0.5)^3
		{100 * time.Millisecond, 10, false},
		{time.Second, 10, false},
	} {
		got, running := v.Eval(t0.Add(c.at))
		if got < c.want-0.01 || got > c.want+0.01 || running != c.running {
			t.Errorf("at %v: %v %v, want %v %v", c.at, got, running, c.want, c.running)
		}
	}

	// Set starts from where the value is, toward the new target.
	v.Set(t0.Add(50*time.Millisecond), 0, 100*time.Millisecond)
	if got, _ := v.Eval(t0.Add(50 * time.Millisecond)); got < 8.7 || got > 8.8 {
		t.Errorf("retargeted value jumped to %v", got)
	}
	if got, _ := v.Eval(t0.Add(200 * time.Millisecond)); got != 0 {
		t.Errorf("retargeted value ends at %v", got)
	}

	// The first target shows at once.
	var w Value
	w.Set(t0, 1, time.Second)
	if got, running := w.Eval(t0); got != 1 || running {
		t.Errorf("first Set: %v %v", got, running)
	}
}

func TestReduced(t *testing.T) {
	SetReduced(true)
	defer SetReduced(false)
	t0 := time.Unix(1000, 0)
	v := Value{From: 0, To: 1, Start: t0, Dur: time.Second}
	if got, running := v.Eval(t0); got != 1 || running {
		t.Errorf("reduced motion: %v %v, want the end at once", got, running)
	}
}

func TestColorAndInvalidate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: t0}
	black, white := color.NRGBA{A: 255}, color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	var c Color
	if got := c.Get(gtx, black, 100*time.Millisecond); got != black {
		t.Fatalf("first color %v", got)
	}
	c.Get(gtx, white, 100*time.Millisecond)
	gtx.Now = t0.Add(50 * time.Millisecond)
	if got := c.Get(gtx, white, 100*time.Millisecond); got.R < 200 || got.R > 230 {
		t.Errorf("halfway color %v", got)
	}
	gtx.Now = t0.Add(time.Second)
	if got := c.Get(gtx, white, 100*time.Millisecond); got != white {
		t.Errorf("end color %v", got)
	}
}

// TestPulse: the dot breathes between 0.35 and 1 over 1.4s, and holds at
// 1 under reduce_motion.
func TestPulse(t *testing.T) {
	t0 := time.UnixMilli(1400 * 1000)
	if p := pulseAt(t0); p < 0.999 {
		t.Errorf("start = %v, want 1", p)
	}
	if p := pulseAt(t0.Add(700 * time.Millisecond)); p > 0.351 || p < 0.349 {
		t.Errorf("half way = %v, want 0.35", p)
	}
	SetReduced(true)
	defer SetReduced(false)
	if p := Pulse(layout.Context{Now: t0.Add(700 * time.Millisecond)}); p != 1 {
		t.Errorf("reduced = %v, want 1", p)
	}
}
