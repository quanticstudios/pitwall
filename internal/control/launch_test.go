package control

import (
	"fmt"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestLaunch(t *testing.T) {
	s := config.Settings{Presets: []config.AgentPreset{
		{Name: "safe", Agent: "claude", Args: []string{"--x"}, Env: []string{"A=1"}, Guard: true},
		{Name: "plain", Agent: "codex"},
	}}
	for _, tc := range []struct {
		preset  string
		guarded bool
		rest    []string
		want    string
	}{
		{"", false, []string{"sh"}, "[sh] []"},
		{"", true, []string{"claude"}, "[claude] [PITWALL_GUARD=1]"},
		{"", true, nil, "error"},
		{"safe", false, []string{"fix it"}, "[claude --x fix it] [A=1 PITWALL_GUARD=1]"},
		{"plain", true, nil, "[codex] [PITWALL_GUARD=1]"},
		{"nope", false, nil, "error"},
	} {
		cmd, vars, err := Launch(s, tc.preset, tc.guarded, tc.rest)
		got := fmt.Sprint(cmd, " ", vars)
		if err != nil {
			got = "error"
		}
		if got != tc.want {
			t.Errorf("Launch(%q, %v, %q) = %s, want %s", tc.preset, tc.guarded, tc.rest, got, tc.want)
		}
	}
	if _, err := WithModel(nil, "opus"); err == nil {
		t.Error("a model for a shell")
	}
}

func TestWorkerGuard(t *testing.T) {
	on := config.Settings{GuardWorkers: true}
	for _, tc := range []struct {
		s      config.Settings
		pane   string
		cmd    []string
		preset string
		want   bool
	}{
		{on, "p1", []string{"claude"}, "", true},
		{on, "p1", nil, "safe", true},
		{on, "p1", nil, "", false}, // a shell
		{on, "", []string{"claude"}, "", false},
		{config.Settings{}, "p1", []string{"claude"}, "", false},
	} {
		if got := WorkerGuard(tc.s, Caller{Pane: tc.pane}, tc.cmd, tc.preset); got != tc.want {
			t.Errorf("WorkerGuard(%v, %q, %q, %q) = %v", tc.s.GuardWorkers, tc.pane, tc.cmd, tc.preset, got)
		}
	}
}

func TestGuarded(t *testing.T) {
	st := model.State{Panes: []model.Pane{
		{ID: "a", WorkspaceID: "w1"},
		{ID: "b", WorkspaceID: "w1", Env: []string{"X=1", "PITWALL_GUARD=1"}},
		{ID: "c", WorkspaceID: "w2", Env: []string{"PITWALL_GUARD="}},
	}}
	if !Guarded(st, "w1") || Guarded(st, "w2") || Guarded(st, "w3") {
		t.Error("Guarded")
	}
}
