// Package anim eases values over time for the window's transitions.
package anim

import (
	"image/color"
	"math"
	"sync/atomic"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
)

// The window's durations. Nothing runs past 200ms.
const (
	Hover   = 80 * time.Millisecond  // a row's or button's hover fill
	Fade    = 100 * time.Millisecond // a hover card fading in
	Focus   = 120 * time.Millisecond // a pane's focus border
	Menu    = 120 * time.Millisecond // a menu opening
	Slide   = 140 * time.Millisecond // rows sliding apart or into place
	Dialog  = 150 * time.Millisecond // a dialog opening
	State   = 160 * time.Millisecond // a row's fill after its state changes
	Expand  = 160 * time.Millisecond // a group's rows showing
	Overlay = 170 * time.Millisecond // the palette and the session switcher opening
	Long    = 200 * time.Millisecond // the sidebar and panel sliding, a notice fading out
)

var reduce atomic.Bool

// SetReduced turns every animation into a jump to its end, for
// [appearance] reduce_motion.
func SetReduced(on bool) { reduce.Store(on) }

// Reduced reports whether reduce_motion is on.
func Reduced() bool { return reduce.Load() }

// Ease is cubic ease-out: fast at first, settling at the end.
func Ease(t float32) float32 {
	t = min(max(t, 0), 1)
	return 1 - (1-t)*(1-t)*(1-t)
}

// Progress is how far a transition of dur that started at start has run
// at now, eased, from 0 to 1, and whether it still runs. A zero start has
// ended.
func Progress(start, now time.Time, dur time.Duration) (float32, bool) {
	if start.IsZero() || dur <= 0 || Reduced() {
		return 1, false
	}
	el := now.Sub(start)
	if el >= dur {
		return 1, false
	}
	return Ease(float32(el) / float32(dur)), true
}

// At is Progress at gtx.Now, asking for the next frame while it runs.
func At(gtx layout.Context, start time.Time, dur time.Duration) float32 {
	t, running := Progress(start, gtx.Now, dur)
	if running {
		gtx.Execute(op.InvalidateCmd{})
	}
	return t
}

// Value eases from From to To over Dur from Start.
type Value struct {
	From, To float32
	Start    time.Time
	Dur      time.Duration
}

// Eval is the value at now and whether it still moves.
func (v Value) Eval(now time.Time) (float32, bool) {
	t, running := Progress(v.Start, now, v.Dur)
	return v.From + (v.To-v.From)*t, running
}

// Get is the value at gtx.Now, asking for the next frame while it moves.
func (v Value) Get(gtx layout.Context) float32 {
	x, running := v.Eval(gtx.Now)
	if running {
		gtx.Execute(op.InvalidateCmd{})
	}
	return x
}

// Set starts a move to to over dur from where the value is now. Setting
// the target it already has changes nothing.
func (v *Value) Set(now time.Time, to float32, dur time.Duration) {
	if v.To == to && !v.Start.IsZero() {
		return
	}
	if v.Start.IsZero() { // the first target is where it starts
		*v = Value{From: to, To: to, Start: now}
		return
	}
	cur, _ := v.Eval(now)
	*v = Value{From: cur, To: to, Start: now, Dur: dur}
}

// Color eases a color to each new target it is given.
type Color struct {
	from, to color.NRGBA
	v        Value
}

// Get returns the color on its way to target, starting a dur move when
// target changed; the first target shows at once.
func (c *Color) Get(gtx layout.Context, target color.NRGBA, dur time.Duration) color.NRGBA {
	if c.v.Start.IsZero() {
		c.from, c.to, c.v = target, target, Value{From: 1, To: 1, Start: gtx.Now}
	} else if target != c.to {
		cur := c.at(gtx.Now)
		c.from, c.to, c.v = cur, target, Value{From: 0, To: 1, Start: gtx.Now, Dur: dur}
	}
	t := c.v.Get(gtx)
	return lerp(c.from, c.to, t)
}

func (c *Color) at(now time.Time) color.NRGBA {
	t, _ := c.v.Eval(now)
	return lerp(c.from, c.to, t)
}

func lerp(a, b color.NRGBA, t float32) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t + 0.5) }
	return color.NRGBA{R: l(a.R, b.R), G: l(a.G, b.G), B: l(a.B, b.B), A: l(a.A, b.A)}
}

// Pulse is a dot's breathing opacity at gtx.Now: 1 down to 0.35 and back
// every 1.4s, asking for frames at 30 a second. Under reduce_motion it is
// 1 and asks for none.
func Pulse(gtx layout.Context) float32 {
	if Reduced() {
		return 1
	}
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	return pulseAt(gtx.Now)
}

func pulseAt(now time.Time) float32 {
	t := float64(now.UnixMilli()%1400) / 1400
	return float32(0.675 + 0.325*math.Cos(t*2*math.Pi))
}
