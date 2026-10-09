package sidebar

import (
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	pwlayout "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// newSubHarness is u1, then tab m with three agents (p1 working, p2
// asking for approval, p3 done) beside a shell, then u2 with one asking
// agent.
func newSubHarness(t *testing.T) *dragHarness {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	split := &pwlayout.Node{Children: []*pwlayout.Node{{Pane: "p1"}, {Pane: "p2"}, {Pane: "sh"}, {Pane: "p3"}}}
	h.st = model.State{
		Workspaces: []model.Workspace{
			{ID: "u1", Name: "u1"},
			{ID: "m", Name: "m", Tabs: []model.Tab{{Layout: split}}},
			{ID: "u2", Name: "u2"},
		},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "m", Provider: model.ProviderClaude, Title: "Fix the parser"},
			{ID: "p2", WorkspaceID: "m", Provider: model.ProviderCodex},
			{ID: "sh", WorkspaceID: "m"},
			{ID: "p3", WorkspaceID: "m", Provider: model.ProviderClaude, Prompt: "write the docs"},
			{ID: "q", WorkspaceID: "u2", Provider: model.ProviderClaude},
		},
		Activities: []model.Activity{
			{PaneID: "p1", WorkspaceID: "m", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p2", WorkspaceID: "m", Provider: model.ProviderCodex, State: model.StatePendingApproval, Detail: "rm -rf build", UpdatedAt: time.Unix(5, 0)},
			{PaneID: "p3", WorkspaceID: "m", Provider: model.ProviderClaude, State: model.StateCompleted},
			{PaneID: "q", WorkspaceID: "u2", Provider: model.ProviderClaude, State: model.StatePendingApproval, UpdatedAt: time.Unix(7, 0)},
		},
	}
	h.s.Answers = true
	h.frame()
	h.frame()
	return h
}

func (h *dragHarness) elem(kind byte, id string) elem {
	for _, e := range h.s.elems {
		if e.kind == kind && e.id == id {
			return e
		}
	}
	h.t.Fatalf("no element %c%s", kind, id)
	return elem{}
}

// move puts the pointer at p with no button held.
func (h *dragHarness) move(p f32.Point) {
	h.r.Queue(pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse})
	h.frame()
}

func (h *dragHarness) click(p f32.Point) []Event {
	return append(h.pointer(pointer.Press, p), h.pointer(pointer.Release, p)...)
}

func TestAgentCount(t *testing.T) {
	ap := func(s model.AgentState) model.AgentPane {
		if s == "" {
			return model.AgentPane{}
		}
		return model.AgentPane{Activity: &model.Activity{State: s}}
	}
	for _, c := range []struct {
		aps  []model.AgentPane
		want string
	}{
		{[]model.AgentPane{ap(model.StateWorking), ap(model.StatePendingApproval), ap(model.StateCompleted)}, "3 agents · 1 approval"},
		{[]model.AgentPane{ap(model.StateWorking), ap(model.StateConnecting)}, "2 agents · 2 working"},
		{[]model.AgentPane{ap(model.StateError), ap(model.StateError), ap("")}, "3 agents · 2 errors"},
		{[]model.AgentPane{ap(""), ap("")}, "2 agents"},
	} {
		if got := agentCount(c.aps); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
}

// TestSubRows: only the tab with two or more agents gets sub-rows, in its
// layout's order, shells left out; folding hides them.
func TestSubRows(t *testing.T) {
	h := newSubHarness(t)
	var got []string
	for _, e := range h.s.elems {
		got = append(got, e.key())
	}
	if want := []string{"su1", "sm", "ap1", "ap2", "ap3", "su2"}; !slices.Equal(got, want) {
		t.Fatalf("laid out %v, want %v", got, want)
	}
	if paneTitle(h.s.shownSubs(newView(layout.Context{}, nil, &h.st, "", ""), "m")[2]) != "write the docs" {
		t.Error("an untitled pane is not named by its prompt")
	}
	chip := h.at("m", 40).Add(f32.Pt(-100+8+32+6, 0)) // the fold chip on line 2, past listPad
	h.move(chip)
	if evs := h.click(chip); !h.s.folded["m"] || len(evs) != 0 {
		t.Fatalf("the agent count did not fold the sub-rows, or sent %v", evs)
	}
	got = got[:0]
	for _, e := range h.s.elems {
		got = append(got, e.key())
	}
	if want := []string{"su1", "sm", "su2"}; !slices.Equal(got, want) {
		t.Fatalf("folded: %v, want %v", got, want)
	}
}

// TestSubRowClick: a click on a sub-row shows its tab with that pane
// focused.
func TestSubRowClick(t *testing.T) {
	h := newSubHarness(t)
	e := h.elem('a', "p3")
	evs := h.click(f32.Pt(150, float32(56+(e.top+e.bot)/2)))
	if !slices.Contains(evs, Event(SelectWorkspace{WorkspaceID: "m", PaneID: "p3"})) {
		t.Fatalf("click on p3's sub-row sent %v", evs)
	}
}

// TestSubRowAnswers: with sub-rows, Allow and Deny sit on the asking
// pane's sub-row and answer that pane; the tab row has none. A tab with
// one agent, or with its sub-rows folded, answers on its own row.
func TestSubRowAnswers(t *testing.T) {
	h := newSubHarness(t)
	v := newView(layout.Context{}, theme.Dark(), &h.st, "", "u1")
	if h.s.answering(v, "m", false, v.activity["m"]) {
		t.Error("a tab with sub-rows answers on its own row")
	}
	if !h.s.answering(v, "u2", false, v.activity["u2"]) {
		t.Error("a tab with one asking agent has no Allow on its row")
	}
	ap, _ := v.agentPane("m", "p2")
	if !h.s.subAnswering(ap) {
		t.Fatal("the asking sub-row has no Allow")
	}
	if ap, _ := v.agentPane("m", "p1"); h.s.subAnswering(ap) {
		t.Error("a working sub-row has Allow")
	}
	// Find Allow on p2's second line and click it.
	e := h.elem('a', "p2")
	th := theme.Dark()
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	line := gtx.Sp(th.Sp(theme.Small) * 1.5)
	y := float32(56 + e.top + 3 + line + 3 + answerHeight(gtx, th)/2)
	r := h.s.sub("p2")
	var at f32.Point
	for x := float32(280); x > 100 && at == (f32.Point{}); x -= 2 {
		h.move(f32.Pt(x, y))
		if r.allow.Hovered() {
			at = f32.Pt(x, y)
		}
	}
	if at == (f32.Point{}) {
		t.Fatal("no Allow on the asking sub-row")
	}
	evs := h.click(at)
	if want := (Answer{PaneID: "p2", At: time.Unix(5, 0).UnixNano(), Allow: true}); !slices.Contains(evs, Event(want)) {
		t.Fatalf("Allow sent %v, want %v", evs, want)
	}
	h.s.folded = map[string]bool{"m": true}
	if !h.s.answering(v, "m", false, v.activity["m"]) {
		t.Error("a folded tab does not answer on its row")
	}
}

// TestDragSubRows: pressing a sub-row lifts its tab, the sub-rows go
// with it and the gap fits them all; a drop over a sub-row's lower half
// lands after the whole block.
func TestDragSubRows(t *testing.T) {
	h := newSubHarness(t)
	m, last := h.elem('s', "m"), h.elem('a', "p3")
	p := f32.Pt(150, float32(56+h.elem('a', "p1").top+4))
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, 10)))
	if !h.s.Dragging() || h.s.drag.kind != 's' || h.s.drag.id != "m" {
		t.Fatalf("dragging %c %q", h.s.drag.kind, h.s.drag.id)
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if !h.s.carried(h.elem('a', id)) {
			t.Errorf("%s stays behind", id)
		}
	}
	if got, want := h.s.gapSize(layout.Context{Metric: unit.Metric{PxPerDp: 1}}, h.s.elems, 0), last.bot-m.top+2; got != want {
		t.Errorf("gap %d, want %d: the row and its sub-rows", got, want)
	}
	evs := h.pointer(pointer.Release, h.at("u2", 50))
	if !slices.Equal(evs, []Event{MoveSession{WorkspaceID: "m"}}) {
		t.Fatalf("drop sent %v", evs)
	}

	flow := []elem{
		{kind: 's', id: "t", top: 0, bot: 56},
		{kind: 'a', id: "x", parent: "t", top: 56, bot: 80},
		{kind: 'a', id: "y", parent: "t", top: 80, bot: 104},
		{kind: 's', id: "n", top: 108, bot: 164},
	}
	if d := tabDrop(flow, 95, expandedExcept(), ""); d != (drop{before: "n", at: 3, ok: true}) {
		t.Errorf("lower sub-row: %+v", d)
	}
	if d := tabDrop(flow, 45, expandedExcept(), ""); d != (drop{before: "t", at: 0, ok: true}) {
		t.Errorf("upper half of the block: %+v", d)
	}
	if d := groupDrop(flow, 95); d != (drop{before: "n", at: 3, ok: true}) {
		t.Errorf("group over a sub-row: %+v", d)
	}
}

// TestRowHeightScales: rows and sub-rows grow with [font] ui_size.
func TestRowHeightScales(t *testing.T) {
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	th := theme.Dark()
	if got, want := rowHeight(gtx, th), gtx.Dp(8)+gtx.Dp(19.5)+gtx.Dp(4)+gtx.Dp(16.5)+gtx.Dp(8); got != want {
		t.Errorf("default row %d, want %d as before", got, want)
	}
	tc, _ := config.Builtin("aide-dark")
	big, _ := theme.New(tc, config.Font{UISize: 15})
	if a, b := rowHeight(gtx, th), rowHeight(gtx, big); b <= a+4 {
		t.Errorf("ui_size 15 row %d, default %d", b, a)
	}
	if a, b := subHeight(gtx, th, false), subHeight(gtx, big, false); b <= a {
		t.Errorf("ui_size 15 sub-row %d, default %d", b, a)
	}
	if subHeight(gtx, th, true) <= subHeight(gtx, th, false) {
		t.Error("an asking sub-row has no room for Allow and Deny")
	}
}
