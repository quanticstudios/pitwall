package sidebar

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Claude Code's changes go to Codex and Codex's to Claude Code; another
// agent's user picks either. The reviewed pane is the first agent to
// open, never the reviewer beside it.
func TestReviewers(t *testing.T) {
	ws := model.Workspace{ID: "w"}
	both := []model.Provider{model.ProviderClaude, model.ProviderCodex}
	for _, c := range []struct {
		panes []model.Pane
		want  []model.Provider
		why   string // with the reviewer installed
	}{
		{nil, both, "no agent"},
		{[]model.Pane{{ID: "s", WorkspaceID: "w", Provider: model.ProviderTerminal}}, both, "no agent"},
		{[]model.Pane{{ID: "a", WorkspaceID: "w", Provider: model.ProviderClaude}, {ID: "r", WorkspaceID: "w", Provider: model.ProviderCodex}}, []model.Provider{model.ProviderCodex}, ""},
		{[]model.Pane{{ID: "a", WorkspaceID: "w", Provider: model.ProviderCodex}}, []model.Provider{model.ProviderClaude}, ""},
		{[]model.Pane{{ID: "x", WorkspaceID: "w", Provider: model.ProviderCodex, Exited: true}, {ID: "a", WorkspaceID: "w", Provider: model.ProviderPi}}, both, ""},
		{[]model.Pane{{ID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, HooksMissing: true}}, []model.Provider{model.ProviderCodex}, "no hooks"},
		{[]model.Pane{{ID: "a", WorkspaceID: "w", Provider: model.ProviderAider}}, both, "no hooks"},
	} {
		st := &model.State{Panes: c.panes}
		if got := Reviewers(st, ws); !slices.Equal(got, c.want) {
			t.Errorf("%v: reviewers %v, want %v", c.panes, got, c.want)
		}
		if got := OpinionBlocked(st, ws, true); got != c.why {
			t.Errorf("%v: blocked %q, want %q", c.panes, got, c.why)
		}
		if c.why == "" && OpinionBlocked(st, ws, false) != "not installed" {
			t.Errorf("%v: a missing reviewer is not blocked", c.panes)
		}
	}
}
