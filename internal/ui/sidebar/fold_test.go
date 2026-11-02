package sidebar

import (
	"maps"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
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

// TestFoldClick: a click on a parent's row focuses its own pane, one on a
// worker's sub-row shows that worker, Allow answers its prompt, a right
// click opens its tab menu, and Ctrl and Shift select workers.
func TestFoldClick(t *testing.T) {
	h := newFoldHarness(t)
	if evs := h.click(h.at("o", 10)); !slices.Contains(evs, Event(SelectWorkspace{WorkspaceID: "o", PaneID: "po"})) {
		t.Fatalf("click on o's row sent %v, want its own pane focused", evs)
	}
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
	h.s.menuWS = ""
	h.frame()
	// Ctrl and Shift select the worker, not its parent, so the menu acts
	// on it.
	h.clickMods(f32.Pt(150, float32(56+h.elem('a', "w3").top+4)), key.ModShortcut)
	if !h.s.selected["w3"] || h.s.selected["o"] {
		t.Fatalf("Ctrl+click on w3 selected %v", h.s.selected)
	}
	h.clickMods(f32.Pt(150, float32(56+h.elem('a', "w2").top+4)), key.ModShift)
	if got := slices.Sorted(maps.Keys(h.s.selected)); !slices.Equal(got, []string{"w2", "w3"}) {
		t.Fatalf("Shift+click from w3 to w2 selected %v", got)
	}
	if got := h.s.targets(newView(layout.Context{}, nil, &h.st, "", "u1"), "w2"); !slices.Equal(got, []string{"w2", "w3"}) {
		t.Fatalf("the menu acts on %v", got)
	}
}

// clickMods clicks at p with mods held.
func (h *dragHarness) clickMods(p f32.Point, mods key.Modifiers) {
	for _, k := range []pointer.Kind{pointer.Press, pointer.Release} {
		ev := pointer.Event{Kind: k, Position: p, Source: pointer.Mouse, Modifiers: mods}
		if k == pointer.Press {
			ev.Buttons = pointer.ButtonPrimary
		}
		h.r.Queue(ev)
		h.frame()
	}
}

// TestFoldFocus: focusing the sidebar from a worker puts the cursor on
// its sub-row.
func TestFoldFocus(t *testing.T) {
	h := newFoldHarness(t)
	h.s.Focus(&h.st, "", "w1")
	if cursorAt(&h.s) != "a:w1" || h.s.cur.parent != "o" {
		t.Fatalf("focused from w1 the cursor is on %s under %q", cursorAt(&h.s), h.s.cur.parent)
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

// TestFoldAsksBehindError: a worker whose row shows one agent's error
// while another asks for approval stays shown folded, with Allow and Deny
// for the one asking.
func TestFoldAsksBehindError(t *testing.T) {
	h := newFoldHarness(t)
	h.st.Activities = append(h.st.Activities, model.Activity{PaneID: "p2e", WorkspaceID: "w2", Provider: model.ProviderClaude, State: model.StateError, UpdatedAt: time.Unix(6, 0)})
	h.s.folded = map[string]bool{"o": true}
	v := newView(layout.Context{}, theme.Dark(), &h.st, "", "u1")
	if a := v.activity["w2"]; a == nil || a.State != model.StateError {
		t.Fatalf("w2's row shows %+v, want its error", a)
	}
	got := h.s.shownSubs(v, "o")
	if len(got) != 1 || got[0].id() != "w2" {
		t.Fatalf("folded o shows %v, want w2", got)
	}
	if !h.s.subAnswering(v, got[0]) || v.prompt(got[0]).PaneID != "p2" {
		t.Fatalf("w2's sub-row answers %+v", v.prompt(got[0]))
	}
}

// TestFoldPlan: an agent sub-row with a plan has Request changes…, folded
// too, and it opens the field for the reply to that pane.
func TestFoldPlan(t *testing.T) {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = cursorState()
	h.st.Activities[2].State = model.StatePlanReady // p3
	h.s.Answers = true
	h.s.folded = map[string]bool{"m": true}
	h.frame()
	h.frame()
	if got, want := h.keys(), []string{"su1", "sm", "ap2", "ap3"}; !slices.Equal(got[:4], want) {
		t.Fatalf("laid out %v, want the plan shown folded", got)
	}
	e := h.elem('a', "p3")
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	th := theme.Dark()
	y := float32(56 + e.top + 3 + gtx.Sp(th.Sp(theme.Small)*1.5) + 3 + answerHeight(gtx, th)/2)
	r := h.s.sub("p3")
	var at f32.Point
	for x := float32(280); x > 100 && at == (f32.Point{}); x -= 2 {
		h.move(f32.Pt(x, y))
		if r.plan.Hovered() {
			at = f32.Pt(x, y)
		}
	}
	if at == (f32.Point{}) {
		t.Fatal("no Request changes… on p3's sub-row")
	}
	h.click(at)
	h.frame()
	if h.s.replying != "p3" || h.s.replyPane != "p3" {
		t.Fatalf("Request changes… opened the reply on %q for %q", h.s.replying, h.s.replyPane)
	}
}

// TestFoldReshelve: snoozing a tab snoozes its workers, which keep their
// pins, and the undo puts back each one's own snooze.
func TestFoldReshelve(t *testing.T) {
	h := newFoldHarness(t)
	now, wake := h.now, h.now.Add(time.Hour)
	own := now.Add(48 * time.Hour)
	h.st.Workspace("w1").Pinned = true
	h.st.Workspace("w2").SnoozedUntil = own
	h.s.now = now
	v := newView(layout.Context{Now: now}, theme.Dark(), &h.st, "", "u1")
	h.s.events = nil
	h.s.reshelve(v, []string{"o"}, "Snoozed", func(_ string, sh shelf) shelf { return shelf{pinned: sh.pinned, until: wake} })
	want := []Event{SetInbox{WorkspaceID: "o", SnoozedUntil: wake}, SetInbox{WorkspaceID: "w1", SnoozedUntil: wake},
		SetInbox{WorkspaceID: "w2", SnoozedUntil: wake}, SetInbox{WorkspaceID: "w3", SnoozedUntil: wake}}
	if !slices.Equal(h.s.events, want) {
		t.Fatalf("snooze sent %v, want %v", h.s.events, want)
	}
	back := []Event{SetInbox{WorkspaceID: "o"}, SetInbox{WorkspaceID: "w1"}, SetInbox{WorkspaceID: "w2", SnoozedUntil: own}, SetInbox{WorkspaceID: "w3"}}
	if h.s.undo == nil || h.s.undo.text != "Snoozed orch" || !slices.Equal(h.s.undo.events, back) {
		t.Fatalf("undo %+v, want %v", h.s.undo, back)
	}
}
