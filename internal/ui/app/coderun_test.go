package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestShellBeside: Run finds the shell split beside a pane chat, and
// none beside a chat tab, an agent, a running command or an exited shell.
func TestShellBeside(t *testing.T) {
	split := func(ws string, panes ...string) model.Workspace {
		root := &layout.Node{Dir: layout.Horizontal}
		for _, p := range panes {
			root.Children = append(root.Children, &layout.Node{Pane: p})
		}
		return model.Workspace{ID: ws, Tabs: []model.Tab{{ID: ws + "t", Layout: root}}, ActiveTab: ws + "t"}
	}
	st := &model.State{
		Workspaces: []model.Workspace{split("w1", "claude", "codex", "dev", "sh"), split("w2", "solo"), split("w3", "c3", "gone"), {ID: "w4", Chat: "chat1"}},
		Panes: []model.Pane{
			{ID: "claude", WorkspaceID: "w1", Provider: model.ProviderClaude},
			{ID: "codex", WorkspaceID: "w1", Provider: model.ProviderCodex},
			{ID: "dev", WorkspaceID: "w1", Cmd: []string{"npm", "run", "dev"}},
			{ID: "sh", WorkspaceID: "w1", Cmd: []string{"/bin/zsh", "-l"}},
			{ID: "solo", WorkspaceID: "w2", Provider: model.ProviderClaude},
			{ID: "c3", WorkspaceID: "w3", Provider: model.ProviderClaude},
			{ID: "gone", WorkspaceID: "w3", Exited: true},
		},
	}
	for chat, want := range map[string]string{"claude": "sh", "solo": "", "c3": "", "chat1": ""} {
		if got := shellBeside(st, chat); got != want {
			t.Errorf("shellBeside(%s) = %q, want %q", chat, got, want)
		}
	}
	st.Panes[3].Cmd = nil // the user's shell
	if got := shellBeside(st, "claude"); got != "sh" {
		t.Errorf("the user's shell: %q", got)
	}
}
