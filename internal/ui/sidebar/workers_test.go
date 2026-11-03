package sidebar

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// newWorkersHarness is orchestrator o with workers w1 and w2, then tab x,
// in the tree or the inbox, workers folded or separate.
func newWorkersHarness(t *testing.T, inbox, separate bool) *dragHarness {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = model.State{
		Workspaces: []model.Workspace{
			{ID: "o", Name: "orch"}, {ID: "x", Name: "x"},
			{ID: "w1", Name: "w1", Parent: "o"}, {ID: "w2", Name: "w2", Parent: "o"},
		},
		Activities: []model.Activity{
			{PaneID: "po", WorkspaceID: "o", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p2", WorkspaceID: "w2", Provider: model.ProviderCodex, State: model.StateWorking},
			{PaneID: "px", WorkspaceID: "x", Provider: model.ProviderClaude, State: model.StateWorking},
		},
	}
	h.s.Inbox, h.s.SeparateWorkers = inbox, separate
	h.frame()
	h.frame()
	return h
}

// tabElems is the laid out rows and sub-rows, without section headers.
func (h *dragHarness) tabElems() []string {
	return slices.DeleteFunc(h.keys(), func(k string) bool { return !strings.HasPrefix(k, "s") && !strings.HasPrefix(k, "a") })
}

// TestWorkerRows: folded, a parent's two workers are sub-rows of its row
// and no rows of their own, in the tree and the inbox; separate, each is
// a row: in the tree under the parent once its strip opens, in the inbox
// on its own.
func TestWorkerRows(t *testing.T) {
	for _, c := range []struct {
		name            string
		inbox, separate bool
		rows, elems     []string
	}{
		{"folded tree", false, false, []string{"o", "x"}, []string{"so", "aw1", "aw2", "sx"}},
		{"folded inbox", true, false, []string{"o", "x"}, []string{"so", "aw1", "aw2", "sx"}},
		{"separate tree", false, true, []string{"o", "x"}, []string{"so", "sx"}},
		{"separate inbox", true, true, []string{"o", "w1", "w2", "x"}, []string{"so", "sw1", "sw2", "sx"}},
	} {
		h := newWorkersHarness(t, c.inbox, c.separate)
		if got := h.s.Rows(&h.st, "", "x"); !slices.Equal(got, c.rows) {
			t.Errorf("%s: Rows %v, want %v", c.name, got, c.rows)
		}
		if got := h.tabElems(); !slices.Equal(got, c.elems) {
			t.Errorf("%s: laid out %v, want %v", c.name, got, c.elems)
		}
		if got := h.s.KidsFolded("o"); got != (c.separate && !c.inbox) {
			t.Errorf("%s: KidsFolded %v", c.name, got)
		}
	}
	h := newWorkersHarness(t, false, true)
	h.s.setKidsOpen("o", true)
	h.frame()
	if got, want := h.s.Rows(&h.st, "", "x"), []string{"o", "w1", "w2", "x"}; !slices.Equal(got, want) {
		t.Errorf("separate tree, strip open: Rows %v, want %v", got, want)
	}
	if got, want := h.tabElems(), []string{"so", "sw1", "sw2", "sx"}; !slices.Equal(got, want) {
		t.Errorf("separate tree, strip open: laid out %v, want %v", got, want)
	}
}

// TestFleet: separate, tabs u1 opened start folded into a strip on u1's
// row; a click on the strip hangs them under it, kidIndent in, and opens no
// tab.
func TestFleet(t *testing.T) {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.s.SeparateWorkers = true
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
	if got, want := h.keys(), []string{"su1", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("a fleet starts folded, laid out %v, want %v", got, want)
	}
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	th := theme.Dark()
	strip := h.at("u1", rowHeight(gtx, th)+4)
	h.move(strip)
	if evs := h.click(strip); h.s.KidsFolded("u1") || len(evs) != 0 {
		t.Fatalf("the strip did not unfold the workers, or sent %v", evs)
	}
	if got, want := h.keys(), []string{"su1", "sw1", "sw2", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("unfolded: %v, want %v", got, want)
	}
	root, kid := h.elem('s', "u1"), h.elem('s', "w1")
	if root.bot-root.top != rowHeight(gtx, th)+fleetHeight(gtx, th) || kid.x != int(kidIndent) || kid.parent != "u1" {
		t.Fatalf("root %+v, worker %+v", root, kid)
	}
	if a := newView(gtx, th, &h.st, "", "").activity["u1"]; a == nil || a.WorkspaceID != "w2" {
		t.Fatalf("folded, u1 shows %+v, want w2's approval", a)
	}
	if a := buildView(gtx, th, &h.st, "", "", true).activity["u1"]; a != nil {
		t.Fatalf("separate, u1 took its worker's %+v", a)
	}
	if h.click(strip); !h.s.KidsFolded("u1") {
		t.Fatal("a second click did not fold the workers again")
	}
	if got := h.s.Rows(&h.st, "", "w2"); !slices.Equal(got, []string{"u1", "w2", "x"}) {
		t.Fatalf("folded with worker w2 active: Rows %v, want the active worker listed", got)
	}
	h.s.Focus(&h.st, "", "w2")
	if cursorAt(&h.s) != "s:w2" {
		t.Fatalf("focused from w2 the cursor is on %s, want its row", cursorAt(&h.s))
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
