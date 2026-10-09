package sidebar

import (
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestRadarCard checks the mark's color, the card's radar lines with the
// other tab's name, and the merge order: fewer conflicts first, then not
// behind the default branch, then fewer changed lines.
func TestRadarCard(t *testing.T) {
	th := theme.Dark()
	st := cardState()
	st.Workspaces = append(st.Workspaces, model.Workspace{ID: "api", Name: "acme-api", NameSet: true, Branch: "acme-api"})
	st.Overlaps = map[string][]model.Overlap{
		"agent": {{WorkspaceID: "api", Branch: "acme-api", Files: []string{"go.mod"}}},
		"api":   {{WorkspaceID: "agent", Branch: "fix/login", Files: []string{"go.mod"}}, {WorkspaceID: "gone", Branch: "old", Files: []string{"x"}, Conflicts: 1}},
	}
	st.Stats["api"] = model.BranchStats{Additions: 1}
	v := newView(layout.Context{}, th, st, "", "")
	if c, _ := radarColor(th, st.Overlaps["agent"]); c != th.Yellow {
		t.Error("an overlap without conflicts is not yellow")
	}
	if c, _ := radarColor(th, st.Overlaps["api"]); c != th.Red {
		t.Error("an overlap with a conflict is not red")
	}
	if _, ok := radarColor(th, nil); ok || radarMark(v, "shell") != nil {
		t.Error("a mark without overlaps")
	}
	c := cardFor(v, st.Workspaces[0])
	if got := c.lines(); got != "tfbxosdp" {
		t.Errorf("lines %q, want tfbrosdp", got)
	}
	// api conflicts with a tab gone from view, so the agent tab goes first.
	if len(c.radar) != 1 || c.radar[0].name != "acme-api" || c.radar[0].first != "this tab" {
		t.Fatalf("radar %+v", c.radar)
	}
	if c := cardFor(v, st.Workspaces[2]); len(c.radar) != 2 || c.radar[0].first != "fix-login" || c.radar[1].name != "old" {
		t.Fatalf("api radar %+v", c.radar)
	}
	st.Overlaps["api"] = st.Overlaps["api"][:1]
	if got := mergeFirst(st, "agent", "api"); got != 1 {
		t.Errorf("with no conflicts, fewer lines: %d, want api first", got)
	}
	st.Stats["api"] = model.BranchStats{Additions: 1, Behind: 3}
	if got := mergeFirst(st, "agent", "api"); got != -1 {
		t.Errorf("api behind its default branch: %d, want agent first", got)
	}
	if got := mergeFirst(st, "shell", "nothing"); got != 0 {
		t.Errorf("nothing to tell apart: %d", got)
	}
}

// TestRadarCardInput checks the card stays open with the pointer on it,
// past the grace that closes others, and a click on a file asks for its
// diff in the card's tab and closes the card.
func TestRadarCardInput(t *testing.T) {
	h := newDragHarness(t)
	h.st.Overlaps = map[string][]model.Overlap{"u2": {{WorkspaceID: "u1", Branch: "u1", Files: []string{"router.ts", "go.mod"}, Conflicts: 1}}}
	move := func(p f32.Point) []Event {
		h.r.Queue(pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse})
		return h.frame()
	}
	move(h.at("u2", 20))
	h.now = h.now.Add(hoverDelay)
	h.frame()
	if h.s.hover.shown != "u2" {
		t.Fatalf("resting on u2 opened %q", h.s.hover.shown)
	}
	onCard := f32.Pt(288+6+20, float32(h.s.cardY+10))
	move(onCard)
	h.now = h.now.Add(2 * hoverGrace)
	h.frame()
	if h.s.hover.shown != "u2" {
		t.Fatal("the card closed with the pointer on it")
	}

	// Down the text column until a file button answers.
	var got []Event
	for y := h.s.cardY; y < h.s.cardY+200 && len(got) == 0; y += 2 {
		p := f32.Pt(288+6+40, float32(y))
		h.pointer(pointer.Press, p)
		for _, e := range h.pointer(pointer.Release, p) {
			if _, ok := e.(ViewFileDiff); ok {
				got = append(got, e)
			}
		}
	}
	if len(got) != 1 || got[0] != (ViewFileDiff{WorkspaceID: "u2", Path: "router.ts"}) {
		t.Fatalf("clicking down the card: %v, want router.ts's diff in u2", got)
	}
	if h.s.hover.shown != "" {
		t.Error("the card stayed open after a click on a file")
	}
}
