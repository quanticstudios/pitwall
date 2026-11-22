package mobile

import (
	"image"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
)

// TestMobileLayoutInsets verifies layoutFrame shifts and shrinks the app's
// layout inside app.Insets (Android 15 edge-to-edge safe areas).
func TestMobileLayoutInsets(t *testing.T) {
	th := Theme()
	a := &App{th: th, dir: t.TempDir()}
	a.machines = true
	m := phone.Machine{ID: "m1", Name: "desk", Server: "https://example.com"}
	a.fleet = phone.NewFleet([]phone.Machine{m})
	a.links = map[string]*link{"m1": {m: m, online: true}}

	var r input.Router
	var ops op.Ops
	insets := app.Insets{Top: 60, Bottom: 40, Left: 20, Right: 20}
	frame := func() {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(400, 800)),
			Now:         time.Unix(1e9, 0),
		}
		a.layoutFrame(gtx, insets)
		r.Frame(&ops)
	}
	frame()

	// Tapping inside the top status-bar inset (y=20) must NOT hit the Back button.
	r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: f32.Pt(40, 20)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 1, Position: f32.Pt(40, 20)},
	)
	frame()
	if !a.machines {
		t.Fatal("tap inside top inset triggered Back button")
	}

	// Tapping shifted down by Insets.Top and right by Insets.Left hits the Back button.
	r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 2, Position: f32.Pt(20+24, 60+24)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 2, Position: f32.Pt(20+24, 60+24)},
	)
	frame()
	if a.machines {
		t.Fatal("tap inside safe-area Back button did not leave Settings")
	}
}

// TestMobileSpinnerThrottleAndReduceMotion verifies spinner throttles to 30fps
// and stops animating when reduce_motion is on.
func TestMobileSpinnerThrottleAndReduceMotion(t *testing.T) {
	prev := anim.Reduced()
	anim.SetReduced(false)
	t.Cleanup(func() { anim.SetReduced(prev) })

	th := Theme()
	now := time.Unix(1e9, 0)
	var r input.Router
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(64, 64)), Now: now}
	spinner(gtx, 18, th.Muted)
	r.Frame(&ops)
	wake, ok := r.WakeupTime()
	if !ok {
		t.Fatal("spinner scheduled no wakeup")
	}
	if d := wake.Sub(now); d < 25*time.Millisecond || d > 45*time.Millisecond {
		t.Fatalf("spinner wakeup delta = %v, want ~33ms (30fps)", d)
	}

	anim.SetReduced(true)
	var r2 input.Router
	ops.Reset()
	gtx = layout.Context{Ops: &ops, Source: r2.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(64, 64)), Now: now}
	spinner(gtx, 18, th.Muted)
	r2.Frame(&ops)
	if wake, ok := r2.WakeupTime(); ok {
		t.Fatalf("reduce_motion spinner scheduled wakeup at %v", wake)
	}
}

// TestMobileToolDetabCache verifies expanded tool Detab output is cached on
// entryRow and refreshes when e.Out changes.
func TestMobileToolDetabCache(t *testing.T) {
	th := Theme()
	c := newChat("p1")
	r := c.row(1)
	r.open = true
	e := flow.Entry{Seq: 1, Kind: flow.EntryTool, Tool: "Bash", Arg: "ls", Out: "a\tb", Done: true}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 400))}
	c.tool(gtx, th, r, e)
	if !r.toolCached || r.toolDetab != "ls\n\na    b" {
		t.Fatalf("cached=%v detab=%q", r.toolCached, r.toolDetab)
	}
	// Mutate cached output to prove second frame reuses cache when entry is unchanged.
	r.toolDetab = "sentinel"
	ops.Reset()
	c.tool(gtx, th, r, e)
	if r.toolDetab != "sentinel" {
		t.Fatalf("expected cache reuse, got %q", r.toolDetab)
	}
	// Changing Out refreshes the cache.
	e.Out = "x\ty"
	ops.Reset()
	c.tool(gtx, th, r, e)
	if r.toolDetab != "ls\n\nx    y" {
		t.Fatalf("updated detab = %q, want %q", r.toolDetab, "ls\n\nx    y")
	}
}

// TestMobileMachineRenameCommitsOnBack verifies leaving Settings while a
// machine name editor is still focused commits the rename instead of discarding it.
func TestMobileMachineRenameCommitsOnBack(t *testing.T) {
	dir := t.TempDir()
	m := phone.Machine{ID: "m1", Name: "old-name", Server: "https://example.com", Token: "tok"}
	if err := phone.SaveMachines(dir, []phone.Machine{m}); err != nil {
		t.Fatal(err)
	}
	a := &App{th: Theme(), dir: dir, machines: true}
	a.fleet = phone.NewFleet([]phone.Machine{m})
	a.links = map[string]*link{"m1": {m: m, online: true}}

	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 800))}
		a.machinesLayout(gtx)
		r.Frame(&ops)
	}
	frame()
	ed := a.mach.names["m1"]
	ed.SetText("new-name")
	// Focus the editor, then press Escape/Back without blurring first.
	r.Queue(key.FocusEvent{Focus: true})
	ops.Reset()
	gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 800))}
	gtx.Execute(key.FocusCmd{Tag: ed})
	a.machinesLayout(gtx)
	r.Frame(&ops)

	r.Queue(key.Event{Name: key.NameBack, State: key.Press})
	frame()
	if a.machines {
		t.Fatal("Back did not leave Settings")
	}
	if got := a.fleet.Machines[0].Name; got != "new-name" {
		t.Fatalf("machine name = %q, want new-name", got)
	}
	saved, err := phone.LoadMachines(dir)
	if err != nil || len(saved) != 1 || saved[0].Name != "new-name" {
		t.Fatalf("saved machines = %+v, err = %v", saved, err)
	}
}

func TestMobileOfflineStatusShowsReason(t *testing.T) {
	now := time.Unix(100, 0)
	got := offlineStatus("tls: bad certificate\nmore detail", now.Add(3200*time.Millisecond), now)
	want := "Offline, reconnecting in 4 s · tls: bad certificate"
	if got != want {
		t.Fatalf("offlineStatus = %q, want %q", got, want)
	}
	if empty := offlineStatus("", time.Time{}, now); empty != "Offline, reconnecting" {
		t.Fatalf("empty reason = %q", empty)
	}
}
