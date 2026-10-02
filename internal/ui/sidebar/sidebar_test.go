package sidebar

import (
	"math"
	"testing"
	"time"

	"gioui.org/f32"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Minute:     "just now",
		4 * time.Second:  "just now",
		42 * time.Second: "42s ago",
		59 * time.Minute: "59m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	} {
		if got := relTime(now, now.Add(-d)); got != want {
			t.Errorf("relTime(-%v) = %q, want %q", d, got, want)
		}
	}
	if got := relTime(now, time.Time{}); got != "" {
		t.Errorf("zero time = %q", got)
	}
}

func TestStateColors(t *testing.T) {
	th := &theme.Theme{Sidebar: theme.Hex("#08090c"), Red: theme.Hex("#ff6b6b"), Yellow: theme.Hex("#ffc533"),
		Blue: theme.Hex("#57c1ff"), Purple: theme.Hex("#bd93ff"), Green: theme.Hex("#59d499"),
		Primary: theme.Hex("#2997ff"), SurfaceSecondary: theme.Hex("#1c1f26")}
	for s, want := range map[model.AgentState]any{
		model.StateError: th.Red, model.StatePendingApproval: th.Yellow, model.StateAwaitingInput: th.Yellow,
		model.StateWorking: th.Blue, model.StateConnecting: th.Blue, model.StatePlanReady: th.Purple,
		model.StateCompleted: th.Green, model.StateTerminalRunning: th.Green,
	} {
		if got := stateColor(th, s); got != want {
			t.Errorf("stateColor(%s) = %v, want %v", s, got, want)
		}
	}
	working := &model.Activity{State: model.StateWorking}
	if rowBase(th, working, false, true) != th.Sidebar {
		t.Error("a working row must not take the hover fill")
	}
	if rowBase(th, nil, false, true) != th.SurfaceSecondary {
		t.Error("an idle row takes the hover fill")
	}
	if got, want := rowBase(th, nil, true, false), theme.Mix(th.Sidebar, th.Primary, 0.12); got != want {
		t.Errorf("active row = %v, want %v", got, want)
	}
}

func TestParsePathArc(t *testing.T) {
	// lucide git-branch: "M15 6a9 9 0 0 0-9 9" is a quarter circle around (15, 15).
	segs := parsePath("M15 6a9 9 0 0 0-9 9V3")
	if len(segs) != 3 || segs[1].op != 'A' || segs[2].op != 'L' {
		t.Fatalf("segs = %+v", segs)
	}
	if c := segs[1].pts[0]; c.Sub(f32.Pt(15, 15)).X > 1e-3 || c.Sub(f32.Pt(15, 15)).Y > 1e-3 {
		t.Errorf("center = %v", c)
	}
	if a := segs[1].angle; math.Abs(float64(a)+math.Pi/2) > 1e-3 {
		t.Errorf("sweep = %v, want -pi/2", a)
	}
	if end := segs[2].pts[0]; end != f32.Pt(6, 3) {
		t.Errorf("V3 ended at %v", end)
	}
}
