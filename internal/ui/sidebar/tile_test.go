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

// TestTileStyle: no tile has an outline. Live tiles fill a step toward
// Fg, finished ones not at all, the selected one is tinted Primary and a
// tab that needs the user amber, or red on an error.
func TestTileStyle(t *testing.T) {
	th := theme.Dark()
	act := func(s model.AgentState) *model.Activity { return &model.Activity{State: s} }
	live := theme.Mix(th.Sidebar, th.Fg, 0.025)

	if f := tileStyle(th, nil, false); f != live {
		t.Errorf("idle tab = %v, want %v", f, live)
	}
	if f := tileStyle(th, act(model.StateWorking), false); f != live {
		t.Errorf("working tab = %v", f)
	}
	if live == th.Sidebar {
		t.Error("a live tile does not show on the sidebar")
	}
	for _, s := range []model.AgentState{model.StateCompleted, model.StatePlanReady, model.StateParked} {
		if f := tileStyle(th, act(s), false); f != th.Sidebar {
			t.Errorf("%s tab = %v, want no fill", s, f)
		}
		if c, bold := titleStyle(th, act(s)); c == th.Fg || bold {
			t.Errorf("%s title stays bright", s)
		}
	}
	if c, bold := titleStyle(th, act(model.StateWorking)); c != th.Fg || !bold {
		t.Error("a live title dims")
	}
	if f := tileStyle(th, act(model.StateCompleted), true); f != theme.Mix(th.Sidebar, th.Primary, 0.13) {
		t.Errorf("selected tab = %v", f)
	}
	amber := theme.Mix(th.Sidebar, th.Yellow, 0.10)
	if f := tileStyle(th, act(model.StatePendingApproval), true); f != amber {
		t.Errorf("selected approval = %v, want amber", f)
	}
	if f := tileStyle(th, act(model.StateAwaitingInput), false); f != amber {
		t.Errorf("input = %v, want amber", f)
	}
	if f := tileStyle(th, act(model.StateError), false); f != theme.Mix(th.Sidebar, th.Red, 0.10) {
		t.Errorf("error = %v, want red", f)
	}
	if whyLine(&model.Activity{State: model.StatePendingApproval, Detail: "Run: go test\nmore"}) != "Run: go test" {
		t.Error("an approval's reason is its detail's first line")
	}
	if whyLine(&model.Activity{State: model.StateWorking, Detail: "x"}) != "" {
		t.Error("a working tab has no reason line")
	}
}
