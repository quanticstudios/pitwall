package sidebar

import (
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestProgramInbox puts a shell tab whose program reports in the inbox
// like an agent's: a failure waits on you in Active, a run sits in
// Working, and the tab is a shell again once the reports end. Its row
// gets a line for the program.
func TestProgramInbox(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := model.State{
		Workspaces: []model.Workspace{{ID: "fail", Tabs: []model.Tab{{}}}, {ID: "run", Tabs: []model.Tab{{}}}, {ID: "quiet", Tabs: []model.Tab{{}}}},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "fail", Program: &model.Program{App: "make", State: "error", Progress: -1}},
			// The tab's most urgent program wins.
			{ID: "p2", WorkspaceID: "run", Program: &model.Program{App: "cargo", State: "working", Progress: 40}},
			{ID: "p4", WorkspaceID: "run", Program: &model.Program{App: "lint", State: "done", Progress: -1}},
			{ID: "p3", WorkspaceID: "quiet"},
		},
		// What the daemon's attended makes of them.
		Activities: []model.Activity{
			{PaneID: "p1", WorkspaceID: "fail", Provider: model.ProviderTerminal, State: model.StateError, UpdatedAt: now},
			{PaneID: "p2", WorkspaceID: "run", Provider: model.ProviderTerminal, State: model.StateWorking, UpdatedAt: now},
		},
	}
	s := Sidebar{Inbox: true, WorkingShelf: true}
	gtx := layout.Context{Now: now, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	v := s.viewOf(gtx, theme.Dark(), &st, "", "")
	for id, want := range map[string]section{"fail": secActive, "run": secWorking, "quiet": secShells} {
		if v.secOf[id] != want {
			t.Errorf("%s in %s, want %s", id, sectionTitle[v.secOf[id]], sectionTitle[want])
		}
	}
	if p := v.program["run"]; p == nil || p.App != "lint" {
		t.Errorf("run shows %+v, want lint's done over cargo's working", p)
	}
	if v.linesHeight(gtx, "fail") == 0 || v.linesHeight(gtx, "quiet") != 0 {
		t.Error("a program line is missing or a quiet tab has one")
	}
	if got := ProgramChip(*st.Panes[1].Program); got != "cargo 40%" {
		t.Errorf("chip %q", got)
	}
}

func TestProgramWorkerInbox(t *testing.T) {
	now := time.Now()
	st := model.State{
		Workspaces: []model.Workspace{{ID: "parent"}, {ID: "worker", Parent: "parent"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "worker", Program: &model.Program{State: "error", Progress: -1}}},
		Activities: []model.Activity{{PaneID: "p", WorkspaceID: "worker", Provider: model.ProviderTerminal, State: model.StateError, UpdatedAt: now}},
	}
	s := Sidebar{Inbox: true}
	v := s.viewOf(layout.Context{Now: now}, theme.Dark(), &st, "", "")
	if v.secOf["parent"] != secActive {
		t.Fatalf("parent in %v, want waiting on you", v.secOf["parent"])
	}
	if _, ok := v.secOf["worker"]; ok {
		t.Fatal("worker has its own inbox row")
	}
}

// A report from the pane's own agent, as Codex sends naming itself, is left
// to the agent's mark and pill; a build the agent runs still shows.
func TestAgentReport(t *testing.T) {
	codex := &model.Program{App: "codex", State: "done"}
	build := &model.Program{App: "make", State: "working"}
	st := &model.State{
		Panes: []model.Pane{
			{ID: "a", WorkspaceID: "w", Provider: model.ProviderCodex, Program: codex},
			{ID: "b", WorkspaceID: "w", Provider: model.ProviderTerminal, Program: codex},
			{ID: "c", WorkspaceID: "x", Provider: model.ProviderCodex, Program: build},
		},
		Activities: []model.Activity{{PaneID: "b", WorkspaceID: "w", Provider: model.ProviderCodex}},
	}
	for i, want := range []bool{true, true, false} {
		if got := agentReport(st, st.Panes[i]); got != want {
			t.Errorf("pane %s: %v, want %v", st.Panes[i].ID, got, want)
		}
	}
	if p := tabProgram(st, model.Workspace{ID: "w"}, nil); p != nil {
		t.Errorf("tab w shows %+v, want no program", *p)
	}
	if p := tabProgram(st, model.Workspace{ID: "x"}, nil); p != build {
		t.Errorf("tab x shows %v, want the build", p)
	}
}
