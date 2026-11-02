package sidebar

import (
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// newFoldHarness is orchestrator o, working, with workers w1 working, w2
// asking for approval and w3 done, then tab x; u1 is active.
func newFoldHarness(t *testing.T) *dragHarness {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = model.State{
		Workspaces: []model.Workspace{
			{ID: "u1", Name: "u1"}, {ID: "o", Name: "orch"}, {ID: "x", Name: "x"},
			{ID: "w1", Name: "w1", Parent: "o"}, {ID: "w2", Name: "w2", Parent: "o"}, {ID: "w3", Name: "w3", Parent: "w1"},
		},
		Activities: []model.Activity{
			{PaneID: "po", WorkspaceID: "o", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p2", WorkspaceID: "w2", Provider: model.ProviderCodex, State: model.StatePendingApproval, UpdatedAt: time.Unix(5, 0)},
			{PaneID: "p3", WorkspaceID: "w3", Provider: model.ProviderClaude, State: model.StateCompleted, Unseen: true},
		},
	}
	h.s.Answers = true
	h.frame()
	h.frame()
	return h
}

func (h *dragHarness) keys() []string {
	var got []string
	for _, e := range h.s.elems {
		got = append(got, e.key())
	}
	return got
}

// TestFold: a tab's workers, its workers' workers too, are sub-rows of
// its row, not rows: goto_tab and the digits skip them. Folding leaves
// the one asking for approval, and the worker on screen.
func TestFold(t *testing.T) {
	h := newFoldHarness(t)
	if got, want := h.keys(), []string{"su1", "so", "aw1", "aw2", "aw3", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("laid out %v, want %v", got, want)
	}
	if e := h.elem('a', "w3"); !e.worker || e.parent != "o" {
		t.Fatalf("w3's sub-row %+v, want a worker's under o", e)
	}
	if got := h.s.Rows(&h.st, "", "u1"); !slices.Equal(got, []string{"u1", "o", "x"}) {
		t.Fatalf("Rows %v", got)
	}
	chip := h.at("o", 40).Add(f32.Pt(-100+8+32+6, 0)) // the fold chip on line 2
	h.move(chip)
	if evs := h.click(chip); !h.s.KidsFolded("o") || len(evs) != 0 {
		t.Fatalf("the count did not fold the workers, or sent %v", evs)
	}
	if got, want := h.keys(), []string{"su1", "so", "aw2", "sx"}; !slices.Equal(got, want) {
		t.Fatalf("folded: %v, want the asking worker only", got)
	}
	v := newView(layout.Context{}, nil, &h.st, "", "w3")
	if got := h.s.shownSubs(v, "o"); len(got) != 2 || got[1].id() != "w3" {
		t.Fatalf("folded with w3 on screen: %v, want w2 and w3", got)
	}
	h.click(chip)
	if h.s.KidsFolded("o") || len(h.keys()) != 6 {
		t.Fatalf("a second click did not unfold: %v", h.keys())
	}
}

// TestFoldBubbles: a parent's row shows the most urgent state of it and
// its workers and their unseen turns; it answers only its own prompt; a
// tie keeps its own state.
func TestFoldBubbles(t *testing.T) {
	h := newFoldHarness(t)
	v := newView(layout.Context{}, theme.Dark(), &h.st, "", "u1")
	if a := v.activity["o"]; a == nil || a.State != model.StatePendingApproval || a.WorkspaceID != "w2" {
		t.Fatalf("o shows %+v, want w2's approval", a)
	}
	if u := v.unseen["o"]; u == nil || u.WorkspaceID != "w3" {
		t.Fatalf("o's unseen %+v, want w3's finished turn", u)
	}
	if a := v.ownActivity("o"); a.State != model.StateWorking || h.s.answering(v, "o", false, a) {
		t.Fatalf("o's own activity %+v, or it answers w2 on its row", a)
	}
	if e, _ := v.sub("o", "w2"); !h.s.subAnswering(v, e) {
		t.Fatal("w2's sub-row has no Allow")
	}
	if a := v.activity["x"]; a != nil {
		t.Fatalf("x took %+v", a)
	}
	h.st.Activities[2].State = model.StateWorking // w2 works too
	v = newView(layout.Context{}, theme.Dark(), &h.st, "", "u1")
	if a := v.activity["o"]; a.WorkspaceID != "o" {
		t.Fatalf("o, working over working workers, shows %+v", a)
	}
}

// TestFoldClick: a click on a worker's sub-row shows that worker, Allow
// answers its prompt, and a right click opens its tab menu.
func TestFoldClick(t *testing.T) {
	h := newFoldHarness(t)
	e := h.elem('a', "w1")
	evs := h.click(f32.Pt(150, float32(56+e.top+4)))
	if !slices.Contains(evs, Event(SelectWorkspace{WorkspaceID: "w1", PaneID: "p1"})) {
		t.Fatalf("click on w1's sub-row sent %v", evs)
	}
	e = h.elem('a', "w2")
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	th := theme.Dark()
	line := gtx.Sp(th.Sp(theme.Small) * 1.5)
	y := float32(56 + e.top + 3 + line + 3 + answerHeight(gtx, th)/2)
	r := h.s.sub("w2")
	var at f32.Point
	for x := float32(280); x > 100 && at == (f32.Point{}); x -= 2 {
		h.move(f32.Pt(x, y))
		if r.allow.Hovered() {
			at = f32.Pt(x, y)
		}
	}
	if at == (f32.Point{}) {
		t.Fatal("no Allow on the asking worker's sub-row")
	}
	if evs := h.click(at); !slices.Contains(evs, Event(Answer{PaneID: "p2", At: time.Unix(5, 0).UnixNano(), Allow: true})) {
		t.Fatalf("Allow sent %v", evs)
	}
	p := f32.Pt(150, float32(56+h.elem('a', "w3").top+4))
	h.r.Queue(pointer.Event{Kind: pointer.Press, Position: p, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary})
	h.r.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
	h.frame()
	if h.s.menuWS != "w3" {
		t.Fatalf("right click on w3's sub-row opened %q's menu", h.s.menuWS)
	}
}

// TestFoldCount: a row's count names its agents and workers and how many
// share the most urgent state.
func TestFoldCount(t *testing.T) {
	acts := func(ss ...model.AgentState) []model.Activity {
		var out []model.Activity
		for _, s := range ss {
			out = append(out, model.Activity{State: s})
		}
		return out
	}
	for _, c := range []struct {
		agents, workers int
		acts            []model.Activity
		want            string
	}{
		{2, 2, acts(model.StateWorking, model.StatePendingApproval, model.StateCompleted), "2 agents · 2 workers · 1 approval"},
		{0, 3, acts(model.StatePendingApproval, model.StatePendingApproval, model.StateWorking), "3 workers · 2 approvals"},
		{0, 1, nil, "1 worker"},
		{3, 0, acts(model.StateError), "3 agents · 1 error"},
	} {
		if got := subCount(c.agents, c.workers, c.acts); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
	h := newFoldHarness(t)
	v := newView(layout.Context{}, theme.Dark(), &h.st, "", "u1")
	if got := v.familyCount("o"); got != "3 workers · 1 approval" {
		t.Errorf("o's count %q", got)
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
