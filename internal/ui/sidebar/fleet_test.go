package sidebar

import (
	"slices"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestFleet: tabs u1 opened start folded into a strip on u1's row; a click
// on the strip hangs them under it, kidIndent in, and opens no tab.
func TestFleet(t *testing.T) {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = model.State{
		Workspaces: []model.Workspace{
			{ID: "u1", Name: "orch"}, {ID: "x", Name: "x"},
			{ID: "w1", Name: "w1", Parent: "u1"}, {ID: "w2", Name: "w2", Parent: "u1"},
		},
		Activities: []model.Activity{
			{WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking},
			{WorkspaceID: "w2", Provider: model.ProviderCodex, State: model.StatePendingApproval},
		},
	}
	h.frame()
	h.frame()
	keys := func() []string {
		var got []string
		for _, e := range h.s.elems {
			got = append(got, e.key())
		}
		return got
	}
	if got, want := keys(), []string{"su1", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("a fleet starts folded, laid out %v, want %v", got, want)
	}
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	th := theme.Dark()
	strip := h.at("u1", rowHeight(gtx, th)+4)
	h.move(strip)
	if evs := h.click(strip); h.s.KidsFolded("u1") || len(evs) != 0 {
		t.Fatalf("the strip did not unfold the workers, or sent %v", evs)
	}
	if got, want := keys(), []string{"su1", "sw1", "sw2", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("unfolded: %v, want %v", got, want)
	}
	if got := h.s.Rows(&h.st, "", "u1"); !slices.Equal(got, []string{"u1", "w1", "w2", "x"}) {
		t.Fatalf("Rows %v", got)
	}
	root, kid := h.elem('s', "u1"), h.elem('s', "w1")
	if root.bot-root.top != rowHeight(gtx, th)+fleetHeight(gtx, th) || kid.x != int(kidIndent) || kid.parent != "u1" {
		t.Fatalf("root %+v, worker %+v", root, kid)
	}
	if h.click(strip); !h.s.KidsFolded("u1") {
		t.Fatal("a second click did not fold the workers again")
	}
}

func TestFleetState(t *testing.T) {
	for st, want := range map[model.AgentState]int{
		model.StateError: fleetError, model.StatePendingApproval: fleetNeed, model.StateAwaitingInput: fleetNeed,
		model.StatePlanReady: fleetNeed, model.StateWorking: fleetWorking, model.StateCompleted: fleetDone,
	} {
		if got := fleetState(&model.Activity{Provider: model.ProviderClaude, State: st}); got != want {
			t.Errorf("%s counts as %d, want %d", st, got, want)
		}
	}
	if fleetState(nil) != fleetIdle {
		t.Error("no activity is not idle")
	}
}

func TestFleetRootWatching(t *testing.T) {
	st := model.State{
		Workspaces: []model.Workspace{{ID: "o", Name: "orch"}, {ID: "w", Name: "w", Parent: "o"}},
		Activities: []model.Activity{
			{WorkspaceID: "o", Provider: model.ProviderClaude, State: model.StateCompleted, Unseen: true},
			{WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateWorking},
		},
	}
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Unix(1e9, 0)}
	v := newView(gtx, theme.Dark(), &st, "", "")
	if a := v.activity["o"]; a == nil || a.State != model.StateWatching || v.unseen["o"] != nil {
		t.Fatalf("a done orchestrator over a working worker shows %+v, unseen %v", a, v.unseen["o"])
	}
	st.Activities[1].State = model.StateCompleted
	if a := newView(gtx, theme.Dark(), &st, "", "").activity["o"]; a.State != model.StateCompleted {
		t.Fatalf("with every worker done the orchestrator shows %s", a.State)
	}
}
