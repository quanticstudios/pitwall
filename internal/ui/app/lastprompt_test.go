package app

import (
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestJumpLastPrompt(t *testing.T) {
	now := time.Now()
	st := model.State{
		Sessions: []model.Session{{ID: "s1"}, {ID: "s2"}},
		Workspaces: []model.Workspace{
			{ID: "w1", SessionID: "s1", ActiveTab: "t1", Tabs: []model.Tab{{ID: "t1", Layout: &layout.Node{Pane: "a"}}}},
			{ID: "w2", SessionID: "s2", ActiveTab: "t2", Tabs: []model.Tab{{ID: "t2", Layout: &layout.Node{Children: []*layout.Node{{Pane: "b"}, {Pane: "c"}}}}}},
		},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w1", LastPromptAt: now}, {ID: "b", WorkspaceID: "w2", LastPromptAt: now.Add(time.Second), Exited: true}, {ID: "c", WorkspaceID: "w2"}},
	}
	for _, preset := range []string{"conventional", "aide", "mac"} {
		t.Run(preset, func(t *testing.T) {
			s := st
			n := nav{keys: config.Preset(preset)}
			n.sync(&s)
			n.selectWorkspace(&s, "w1", "a")
			chord := n.keys.Global["jump_last_prompt"][0]
			n.key(&s, key.Event{Name: chord.Name, Modifiers: chord.Mods, State: key.Press})
			if n.session != "s2" || n.workspace != "w2" || n.focused() != "b" {
				t.Fatalf("jump at %s/%s/%s", n.session, n.workspace, n.focused())
			}
			n.key(&s, key.Event{Name: chord.Name, Modifiers: chord.Mods, State: key.Press})
			if n.focused() != "b" {
				t.Fatal("repeated jump cycled")
			}
			s.Panes = []model.Pane{s.Panes[0], s.Panes[2]}
			n.globalOp(&s, "jump_last_prompt")
			if n.session != "s1" || n.focused() != "a" {
				t.Fatal("closed pane did not fall back")
			}
			s.Panes[0].LastPromptAt = time.Time{}
			n.globalOp(&s, "jump_last_prompt")
			if !n.noLastPrompt || n.focused() != "a" {
				t.Fatal("empty history did not stay with notice")
			}
		})
	}
}

func TestJumpLastPromptPopup(t *testing.T) {
	now := time.Now()
	st := model.State{
		Sessions: []model.Session{{ID: "s1"}, {ID: "s2"}},
		Workspaces: []model.Workspace{
			{ID: "w1", SessionID: "s1", ActiveTab: "t1", Tabs: []model.Tab{{ID: "t1", Layout: &layout.Node{Pane: "a"}}}},
			{ID: "w2", SessionID: "s2", ActiveTab: "t2", Tabs: []model.Tab{{ID: "t2", Layout: &layout.Node{Pane: "b"}}}, Popup: &model.Popup{Pane: "pop"}},
		},
		Panes: []model.Pane{
			{ID: "a", WorkspaceID: "w1", LastPromptAt: now},
			{ID: "b", WorkspaceID: "w2"},
			{ID: "pop", WorkspaceID: "w2", Provider: model.ProviderClaude, LastPromptAt: now.Add(time.Second)},
		},
	}
	n := nav{keys: config.Preset("conventional")}
	n.sync(&st)
	n.selectWorkspace(&st, "w1", "a")
	for range 2 {
		n.globalOp(&st, "jump_last_prompt")
		if n.session != "s2" || n.workspace != "w2" || n.focused() != "pop" || n.noLastPrompt {
			t.Fatalf("popup jump at %s/%s/%s, empty=%v", n.session, n.workspace, n.focused(), n.noLastPrompt)
		}
	}
	st.Workspaces[1].Detached = true
	n.globalOp(&st, "jump_last_prompt")
	if n.focused() != "a" {
		t.Fatal("detached popup did not fall back")
	}
	st.Workspaces[1].Detached = false
	st.Workspaces[1].Popup = nil
	n.globalOp(&st, "jump_last_prompt")
	if n.focused() != "a" {
		t.Fatal("pane outside the layout and popup did not fall back")
	}
	st.Workspaces[1].Popup = &model.Popup{Pane: "pop"}
	st.Panes = st.Panes[:2]
	n.globalOp(&st, "jump_last_prompt")
	if n.focused() != "a" {
		t.Fatal("closed popup did not fall back")
	}
}
