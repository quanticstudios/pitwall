package sidebar

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestHoverState(t *testing.T) {
	t0 := time.Unix(1e9, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	var h hoverState
	if wake := h.step(at(0), "a"); h.shown != "" || !wake.Equal(at(hoverDelay)) {
		t.Fatalf("on entering: shown %q, wake %v", h.shown, wake)
	}
	h.step(at(hoverDelay-time.Millisecond), "a")
	if h.shown != "" {
		t.Fatal("opened before the delay")
	}
	h.step(at(hoverDelay), "a")
	if h.shown != "a" || !h.shownAt.Equal(at(hoverDelay)) {
		t.Fatalf("after the delay: %q", h.shown)
	}
	// Off the rows (a gap between two): the card stays through the grace,
	// and the next row takes it over with no second delay or fade.
	off := hoverDelay + time.Second
	if wake := h.step(at(off), ""); h.shown != "a" || !wake.Equal(at(off+hoverGrace)) {
		t.Fatalf("in the grace: %q, wake %v", h.shown, wake)
	}
	h.step(at(off+10*time.Millisecond), "b")
	if h.shown != "b" || !h.shownAt.Equal(at(hoverDelay)) {
		t.Fatalf("switching: %q at %v", h.shown, h.shownAt)
	}
	h.step(at(off+20*time.Millisecond), "")
	h.step(at(off+20*time.Millisecond+hoverGrace), "")
	if h.shown != "" {
		t.Fatal("still open after the grace")
	}
	// Closed again, the next row waits out the delay.
	h.step(at(off+time.Second), "c")
	if h.shown != "" {
		t.Fatal("a closed card reopened without the delay")
	}
	h.step(at(off+time.Second+hoverDelay), "c")
	if h.shown != "c" {
		t.Fatal("no card after the delay")
	}
	// Dismissed, it stays closed on the same row, and another row opens
	// after its own delay.
	h.dismiss()
	h.step(at(off+5*time.Second), "c")
	if h.shown != "" {
		t.Fatal("a dismissed card came back on the same row")
	}
	h.step(at(off+6*time.Second), "d")
	h.step(at(off+6*time.Second+hoverDelay), "d")
	if h.shown != "d" {
		t.Fatal("another row after a dismissal opened no card")
	}
}

// cardState: an agent tab with a question in group g1, on a branch with
// two panes; a plain shell at the top level.
func cardState() *model.State {
	return &model.State{
		Projects: []model.Project{{ID: "g1", Name: "web-app", Color: "sky"}},
		Workspaces: []model.Workspace{
			{ID: "agent", Name: "fix-login", NameSet: true, ProjectID: "g1", Branch: "fix/login", Path: "/srv/demo/web-app"},
			{ID: "shell", Name: "shell", Path: "/srv/demo/notes"},
		},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "agent", Provider: model.ProviderClaude},
			{ID: "p2", WorkspaceID: "agent"},
			{ID: "p3", WorkspaceID: "shell"},
		},
		Activities: []model.Activity{{PaneID: "p1", WorkspaceID: "agent", Provider: model.ProviderClaude,
			State: model.StateAwaitingInput, Detail: "Which database\n\nshould the  tests use?"}},
		Stats: map[string]model.BranchStats{"agent": {Additions: 12, Deletions: 3}},
	}
}

func TestCardLines(t *testing.T) {
	st := cardState()
	v := newView(layout.Context{}, theme.Dark(), st, "", "")
	c := cardFor(v, st.Workspaces[0])
	if got := c.lines(); got != "tfbsdp" {
		t.Errorf("agent tab lines %q, want tfbsdp", got)
	}
	if c.group != "web-app" || c.agent != model.ProviderClaude || c.add != 12 || c.del != 3 || c.panes != 2 ||
		c.detail != "Which database should the tests use?" || stateText(c.state) != "Waiting for input" {
		t.Errorf("agent card %+v", c)
	}
	c = cardFor(v, st.Workspaces[1])
	if got := c.lines(); got != "tf" || c.path != "/srv/demo/notes" || c.group != "" || c.agent != "" {
		t.Errorf("shell card %+v, lines %q", c, got)
	}
	if got := (card{title: "x"}).lines(); got != "t" {
		t.Errorf("bare card lines %q", got)
	}
	// A decision model's advice, with the risk pitwall flags.
	st.Decide = model.DecideInfo{Provider: "jev"}
	st.Activities[0].State, st.Activities[0].Advice, st.Activities[0].AdviceP, st.Activities[0].AdviceRule = model.StatePendingApproval, "allow", 0.96, "sudo"
	v = newView(layout.Context{}, theme.Dark(), st, "", "")
	if c := cardFor(v, st.Workspaces[0]); c.decision != "Jev: allow 96% · sudo" || c.lines() != "tfbsdjp" {
		t.Errorf("advice card %q, lines %q", c.decision, c.lines())
	}
	if c := cardFor(v, st.Workspaces[1]); c.decision != "" {
		t.Errorf("shell card shows %q", c.decision)
	}
}

func TestClampDetail(t *testing.T) {
	long := strings.Repeat("word ", 500)
	got := clampDetail(long)
	if r := []rune(got); len(r) != detailRunes || !strings.HasSuffix(got, "…") {
		t.Errorf("long detail: %d runes, ends %q", len(r), got[len(got)-8:])
	}
	// Laid out, it wraps to three lines at most.
	th := theme.Dark()
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(260, 1000)}}
	one := detailLabel(gtx, th, th.Fg, "short").Size.Y
	three := detailLabel(gtx, th, th.Fg, got).Size.Y
	if three <= 2*one || three > 3*one+2 {
		t.Errorf("long detail is %dpx tall, one line %dpx: want three lines", three, one)
	}
}

// TestGroupIndent: grouped rows lay out indented and loose rows flush; a
// press in the indent is on no row, a press on the row arms its drag; and
// resting on a row opens its card, which a press closes.
func TestGroupIndent(t *testing.T) {
	h := newDragHarness(t)
	for _, e := range h.s.elems {
		want := 0
		if e.kind == 's' && e.group != "" {
			want = 12
		}
		if e.x != want {
			t.Errorf("%s at x %d, want %d", e.key(), e.x, want)
		}
	}
	p := h.at("a", 20)
	h.pointer(pointer.Press, f32.Pt(6, p.Y))
	if h.s.drag.kind != 0 {
		t.Fatalf("a press in the indent armed a drag on %q", h.s.drag.id)
	}
	h.pointer(pointer.Release, f32.Pt(6, p.Y))
	h.pointer(pointer.Press, f32.Pt(16, p.Y))
	if h.s.drag.id != "a" {
		t.Fatalf("a press on the indented row armed %q", h.s.drag.id)
	}
	h.pointer(pointer.Release, f32.Pt(16, p.Y))

	h.r.Queue(pointer.Event{Kind: pointer.Move, Position: h.at("u2", 20), Source: pointer.Mouse})
	h.frame()
	h.now = h.now.Add(hoverDelay)
	h.frame()
	if h.s.hover.shown != "u2" {
		t.Fatalf("resting on u2 opened %q", h.s.hover.shown)
	}
	if want := h.at("u2", 0).Y; float32(h.s.cardY) != want {
		t.Errorf("card at y %d, want %v", h.s.cardY, want)
	}
	h.pointer(pointer.Press, h.at("u2", 20))
	if h.s.hover.shown != "" {
		t.Fatal("a press left the card open")
	}
}
