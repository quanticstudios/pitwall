package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestStateMark: the dot takes the state's color, or rings in Muted for
// Background and Watching, pulses for Working and Connecting; the word is
// colored only for Working, Connecting and the three that need the user.
func TestStateMark(t *testing.T) {
	th := theme.Dark()
	cases := []struct {
		a                  model.Activity
		word               string
		loud, ring, pulses bool
	}{
		{model.Activity{State: model.StatePendingApproval}, "Approval", true, false, false},
		{model.Activity{State: model.StateAwaitingInput}, "Input", true, false, false},
		{model.Activity{State: model.StateError}, "Error", true, false, false},
		{model.Activity{State: model.StateWorking}, "Working", true, false, true},
		{model.Activity{State: model.StateWorking, Background: true}, "Background", false, true, false},
		{model.Activity{State: model.StateConnecting}, "Connecting", true, false, true},
		{model.Activity{State: model.StateWatching}, "Watching", false, true, false},
		{model.Activity{State: model.StatePROpen}, "PR open", false, false, false},
		{model.Activity{State: model.StateTerminalRunning}, "Running", false, false, false},
		{model.Activity{State: model.StateTerminalRunning, Detail: "btop"}, "btop", false, false, false},
		{model.Activity{State: model.StatePlanReady}, "Plan Ready", false, false, false},
		{model.Activity{State: model.StateCompleted}, "Done", false, false, false},
		{model.Activity{State: model.StateParked}, "Parked", false, false, false},
	}
	for _, c := range cases {
		m := markOf(th, c.a)
		if m.word != c.word || m.loud != c.loud || m.ring != c.ring || m.pulses != c.pulses {
			t.Errorf("%s: got %q loud=%v ring=%v pulses=%v, want %q %v %v %v", c.a.State, m.word, m.loud, m.ring, m.pulses, c.word, c.loud, c.ring, c.pulses)
		}
		want := StateColor(th, c.a.State)
		if c.a.State == model.StatePROpen {
			want = th.Purple
		}
		if c.ring {
			want = th.Muted
		}
		if m.dot != want {
			t.Errorf("%s: dot %v, want %v", c.a.State, m.dot, want)
		}
	}
}

// TestTileStyle: live tiles fill with Surface, finished ones only outline,
// the selected one fills SurfaceSecondary in a Primary ring, and a tab
// that needs the user rings amber, or red on an error. The outline of a
// finished tile reads against the sidebar.
func TestTileStyle(t *testing.T) {
	th := theme.Dark()
	none := func(c interface{ RGBA() (r, g, b, a uint32) }) bool { _, _, _, a := c.RGBA(); return a == 0 }
	act := func(s model.AgentState) *model.Activity { return &model.Activity{State: s} }

	if f, r := tileStyle(th, nil, false); f != th.Surface || !none(r) {
		t.Errorf("idle tab = %v %v, want Surface with no ring", f, r)
	}
	if f, r := tileStyle(th, act(model.StateWorking), false); f != th.Surface || !none(r) {
		t.Errorf("working tab = %v %v", f, r)
	}
	for _, s := range []model.AgentState{model.StateCompleted, model.StatePlanReady, model.StateParked} {
		if f, r := tileStyle(th, act(s), false); f != th.Sidebar || r != finishedRing(th) {
			t.Errorf("%s tab = %v %v, want no fill in an outline", s, f, r)
		}
		if c, bold := titleStyle(th, act(s)); c == th.Fg || bold {
			t.Errorf("%s title stays bright", s)
		}
	}
	if c, bold := titleStyle(th, act(model.StateWorking)); c != th.Fg || !bold {
		t.Error("a live title dims")
	}
	if c := theme.Contrast(finishedRing(th), th.Sidebar); c < 1.5 {
		t.Errorf("a finished tile's outline is %.2f:1 on the sidebar, want 1.5:1 or more", c)
	}
	if f, r := tileStyle(th, act(model.StateCompleted), true); f != th.SurfaceSecondary || r != th.Primary {
		t.Errorf("selected tab = %v %v", f, r)
	}
	if _, r := tileStyle(th, act(model.StatePendingApproval), true); r != theme.Mix(th.SurfaceSecondary, th.Yellow, 0.6) {
		t.Errorf("selected approval rings %v, want amber", r)
	}
	if _, r := tileStyle(th, act(model.StateAwaitingInput), false); r != theme.Mix(th.Surface, th.Yellow, 0.6) {
		t.Errorf("input rings %v, want amber", r)
	}
	if _, r := tileStyle(th, act(model.StateError), false); r != theme.Mix(th.Surface, th.Red, 0.6) {
		t.Errorf("error rings %v, want red", r)
	}
	if whyLine(&model.Activity{State: model.StatePendingApproval, Detail: "Run: go test\nmore"}) != "Run: go test" {
		t.Error("an approval's reason is its detail's first line")
	}
	if whyLine(&model.Activity{State: model.StateWorking, Detail: "x"}) != "" {
		t.Error("a working tab has no reason line")
	}
}
