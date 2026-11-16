package control

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// driveState is one session with tab "build" (#1): pane p, running provider
// (none for a shell) in state, or exited with code.
func driveState(provider model.Provider, state model.AgentState, exited bool, code int) model.State {
	s := model.State{
		Sessions:   []model.Session{{ID: "m", Name: "main", Order: []string{"w"}}},
		Projects:   []model.Project{{ID: "g", SessionID: "m", Name: "agents"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "m", Name: "build", NameSet: true, ProjectID: "g", Path: "/work/app", Branch: "main", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: "/work/app/src", Provider: provider, Exited: exited, ExitCode: code}},
	}
	if state != "" {
		s.Activities = []model.Activity{{PaneID: "p", WorkspaceID: "w", Provider: provider, State: state, Detail: "Run make?"}}
	}
	return s
}

func TestPaneState(t *testing.T) {
	for _, tc := range []struct {
		pane  model.Pane
		state model.AgentState
		agent string
		want  string
	}{
		{model.Pane{ID: "p"}, "", "", "running"},
		{model.Pane{ID: "p"}, model.StateTerminalRunning, "", "working"},
		{model.Pane{ID: "p", Cmd: []string{"/usr/bin/claude", "fix it"}}, "", "claude", ""},
		{model.Pane{ID: "p", Cmd: []string{"claude"}, Provider: model.ProviderClaude}, "", "claude", "idle"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StateConnecting, "codex", "working"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StatePlanReady, "codex", "blocked"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StateCompleted, "codex", "done"},
		{model.Pane{ID: "p", Provider: model.ProviderPi}, model.StateCompleted, "pi", "done"},
		{model.Pane{ID: "p", Provider: model.ProviderPi}, model.StatePROpen, "pi", "pr-open"},
		{model.Pane{ID: "p", Provider: model.ProviderPi}, model.StateError, "pi", "done"},
		{model.Pane{ID: "p", Provider: model.ProviderClaude}, model.StateParked, "claude", "parked"},
		{model.Pane{ID: "p", Cmd: []string{"/usr/bin/pi", "fix it"}}, "", "pi", ""},
		{model.Pane{ID: "p", Provider: "gemini"}, "", "gemini", "idle"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex, Exited: true}, model.StateWorking, "codex", "exited"},
	} {
		st := model.State{Panes: []model.Pane{tc.pane}}
		if tc.state != "" {
			st.Activities = []model.Activity{{PaneID: "p", State: tc.state}}
		}
		if agent, got, _ := PaneState(st, tc.pane); agent != tc.agent || got != tc.want {
			t.Errorf("%+v %s: %q %q, want %q %q", tc.pane, tc.state, agent, got, tc.agent, tc.want)
		}
	}
}

// A live pane beats an exited one, and an agent beats a shell among each.
func TestMainPane(t *testing.T) {
	deadClaude := model.Pane{ID: "dead", Provider: model.ProviderClaude, Exited: true}
	shell := model.Pane{ID: "shell"}
	codex := model.Pane{ID: "codex", Provider: model.ProviderCodex}
	deadShell := model.Pane{ID: "deadshell", Exited: true}
	for _, tc := range []struct {
		panes []model.Pane
		want  string
	}{
		{[]model.Pane{deadClaude, shell, codex}, "codex"},
		{[]model.Pane{deadClaude, shell}, "shell"},
		{[]model.Pane{deadShell, deadClaude}, "dead"},
		{[]model.Pane{deadShell}, "deadshell"},
		// An agent read from the screen is an agent too.
		{[]model.Pane{shell, {ID: "gemini", Provider: "gemini"}}, "gemini"},
	} {
		if got := MainPane(tc.panes); got == nil || got.ID != tc.want {
			t.Errorf("%v: %v, want %s", tc.panes, got, tc.want)
		}
	}
	if MainPane(nil) != nil {
		t.Error("no panes, a pane")
	}
}

func TestRepeatingSplitPane(t *testing.T) {
	st := driveState(model.ProviderClaude, model.StateWorking, false, 0)
	st.Workspaces[0].Tabs[0].Layout = &layout.Node{Children: []*layout.Node{{Pane: "p"}, {Pane: "q"}}}
	st.Panes = append(st.Panes, model.Pane{ID: "q", WorkspaceID: "w", Provider: model.ProviderCodex})
	st.Activities = append(st.Activities, model.Activity{PaneID: "q", WorkspaceID: "w", State: model.StateWorking, Repeats: 5, Repeating: "magick not found"})
	rows := Tabs(st, "m", nil)
	if len(rows) != 1 || len(rows[0].PaneList) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	p := rows[0].PaneList[1]
	if p.Repeats != 5 || p.Repeating != "magick not found" || rows[0].Repeats != 0 {
		t.Errorf("secondary pane's repeat marker: %+v", rows[0])
	}
}
