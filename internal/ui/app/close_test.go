package app

import (
	"image"
	"reflect"
	"testing"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func closeState() model.State {
	return model.State{
		Workspaces: []model.Workspace{{ID: "tab", Label: "Fix retries"}, {ID: "other"}},
		Panes: []model.Pane{
			{ID: "a", WorkspaceID: "tab", Provider: model.ProviderClaude},
			{ID: "b", WorkspaceID: "tab", Provider: model.ProviderCodex},
			{ID: "c", WorkspaceID: "other", Provider: model.ProviderCodex},
		},
		Activities: []model.Activity{
			{PaneID: "a", WorkspaceID: "tab", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "b", WorkspaceID: "tab", Provider: model.ProviderCodex, State: model.StateWorking},
			{PaneID: "c", WorkspaceID: "other", Provider: model.ProviderCodex, State: model.StateWorking},
		},
	}
}

func TestCloseDecisionStates(t *testing.T) {
	for _, tc := range []struct {
		state model.AgentState
		ask   bool
	}{
		{model.StateWorking, true},
		{model.StatePendingApproval, true},
		{model.StateAwaitingInput, true},
		{model.StatePlanReady, true},
		{model.StateConnecting, false},
		{model.StateCompleted, false},
		{model.StateError, false},
		{model.StateTerminalRunning, false},
		{model.StateWatching, false},
		{model.StatePROpen, false},
		{"", false},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			st := closeState()
			st.Activities[0].State = tc.state
			_, agents := runningClose(&st, proto.ClosePane{Pane: "a"})
			if (len(agents) > 0) != tc.ask {
				t.Fatalf("agents %v, want ask %v", agents, tc.ask)
			}
		})
	}
}

func TestCloseChat(t *testing.T) {
	for _, state := range []model.AgentState{model.StateWorking, model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady, model.StateCompleted, model.StateError, model.StateConnecting, ""} {
		t.Run(string(state), func(t *testing.T) {
			st := model.State{
				Workspaces: []model.Workspace{{ID: "tab", Chat: "chat"}},
				Activities: []model.Activity{
					{PaneID: "other", Provider: model.ProviderCodex, State: model.StateWorking},
					{PaneID: "chat", WorkspaceID: "tab", Provider: model.ProviderClaude, State: state},
				},
			}
			b := NewFakeBackend()
			u := &ui{b: b, cfg: config.Settings{ConfirmClose: true}}
			msg := proto.CloseTab{WorkspaceID: "tab"}
			u.sendOrAsk(&st, msg)
			ask := state == model.StateWorking || state == model.StatePendingApproval || state == model.StateAwaitingInput || state == model.StatePlanReady
			if ask {
				if u.modal.kind != modalClose || u.modal.ws != "tab" || !reflect.DeepEqual(u.modal.agents, []string{"Claude"}) || len(b.Sent()) != 0 {
					t.Fatalf("chat close: modal %v, agents %v, sent %#v", u.modal.kind, u.modal.agents, b.Sent())
				}
				u.confirmModal(&st)
			}
			if u.modal.kind != modalNone || !reflect.DeepEqual(b.Sent(), []any{msg}) {
				t.Fatalf("chat close: modal %v, sent %#v", u.modal.kind, b.Sent())
			}
		})
	}
}

func TestCloseLastPanePopup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		split bool
		pane  string
		want  []string
	}{
		{"last pane", false, "shell", []string{"Claude"}},
		{"split pane", true, "shell", nil},
		{"popup", false, "popup", []string{"Claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := model.State{
				Workspaces: []model.Workspace{{ID: "tab", Popup: &model.Popup{Pane: "popup"}, Tabs: []model.Tab{{ID: "layout", Layout: layout.Leaf("shell")}}}},
				Panes: []model.Pane{
					{ID: "shell", WorkspaceID: "tab", Provider: model.ProviderTerminal},
					{ID: "popup", WorkspaceID: "tab", Provider: model.ProviderClaude},
				},
				Activities: []model.Activity{{PaneID: "popup", WorkspaceID: "tab", Provider: model.ProviderClaude, State: model.StateWorking}},
			}
			if tc.split {
				st.Panes = append(st.Panes, model.Pane{ID: "sibling", WorkspaceID: "tab"})
				st.Workspaces[0].Tabs[0].Layout = layout.Split(st.Workspaces[0].Tabs[0].Layout, "shell", "sibling", layout.Horizontal)
			}
			b := NewFakeBackend()
			u := &ui{b: b, cfg: config.Settings{ConfirmClose: true}}
			msg := proto.ClosePane{Pane: tc.pane}
			u.sendOrAsk(&st, msg)
			if len(tc.want) > 0 {
				if u.modal.kind != modalClose || !reflect.DeepEqual(u.modal.agents, tc.want) || len(b.Sent()) != 0 {
					t.Fatalf("popup close: modal %v, agents %v, sent %#v", u.modal.kind, u.modal.agents, b.Sent())
				}
				u.confirmModal(&st)
			}
			if u.modal.kind != modalNone || !reflect.DeepEqual(b.Sent(), []any{msg}) {
				t.Fatalf("popup close: modal %v, sent %#v", u.modal.kind, b.Sent())
			}
		})
	}
}

func TestDetachedKillNoCloseDialog(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		st := closeState()
		st.Workspaces[0].Detached, st.Workspaces[0].Pinned = true, pinned
		b := NewFakeBackend()
		u := &ui{b: b, cfg: config.Settings{ConfirmClose: true}}
		u.sidebarEvent(&st, sidebar.KillSession{WorkspaceID: "tab"})
		var want []any
		if pinned {
			if u.modal.kind != modalPinned || len(b.Sent()) != 0 {
				t.Fatalf("pinned kill: modal %v, sent %#v", u.modal.kind, b.Sent())
			}
			u.confirmModal(&st)
			want = append(want, proto.SetTabOptions{WorkspaceID: "tab"})
		}
		want = append(want, proto.KillSession{WorkspaceID: "tab"})
		if u.modal.kind != modalNone || !reflect.DeepEqual(b.Sent(), want) {
			t.Fatalf("armed kill: modal %v, sent %#v", u.modal.kind, b.Sent())
		}
	}
}

func TestCloseDecisionTargets(t *testing.T) {
	for _, tc := range []struct {
		msg  any
		want []string
	}{
		{proto.CloseTab{WorkspaceID: "tab"}, []string{"Claude", "Codex"}},
		{proto.KillSession{WorkspaceID: "tab"}, []string{"Claude", "Codex"}},
		{proto.ClosePane{Pane: "a"}, []string{"Claude"}},
		{proto.ClosePane{Pane: "c"}, []string{"Codex"}},
		{proto.ClosePane{Pane: "missing"}, nil},
		{proto.CloseTab{WorkspaceID: "missing"}, nil},
		{proto.RenameTab{WorkspaceID: "tab"}, nil},
	} {
		st := closeState()
		_, got := runningClose(&st, tc.msg)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%#v: agents %v, want %v", tc.msg, got, tc.want)
		}
	}
	st := closeState()
	st.Activities = st.Activities[:1]
	if _, agents := runningClose(&st, proto.ClosePane{Pane: "b"}); len(agents) != 0 {
		t.Fatal("a sibling's turn protected an idle pane")
	}
	st.Panes[0].Exited = true
	if _, agents := runningClose(&st, proto.ClosePane{Pane: "a"}); len(agents) != 0 {
		t.Fatal("an exited agent asked")
	}
	st.Panes[0].Exited = false
	st.Panes[0].Provider, st.Activities[0].Provider = model.ProviderTerminal, model.ProviderTerminal
	if _, agents := runningClose(&st, proto.ClosePane{Pane: "a"}); len(agents) != 0 {
		t.Fatal("a terminal asked")
	}
	st.Activities[0].Provider = "agent:Test agent"
	if _, agents := runningClose(&st, proto.ClosePane{Pane: "a"}); !reflect.DeepEqual(agents, []string{"Test agent"}) {
		t.Fatalf("self-reported agent: %v", agents)
	}
}

func TestSendOrAskClose(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, msg := range []any{proto.CloseTab{WorkspaceID: "tab"}, proto.KillSession{WorkspaceID: "tab"}, proto.ClosePane{Pane: "a"}} {
				st := closeState()
				// One pane also covers closing the last pane of a pinned tab.
				if _, ok := msg.(proto.ClosePane); ok {
					st.Panes = st.Panes[:1]
				}
				st.Workspaces[0].Pinned = pinned
				b := NewFakeBackend()
				u := &ui{b: b, cfg: config.Settings{ConfirmClose: enabled}}
				u.sendOrAsk(&st, msg)
				want := modalNone
				if pinned {
					want = modalPinned
				} else if _, armed := msg.(proto.KillSession); enabled && !armed {
					want = modalClose
				}
				if u.modal.kind != want {
					t.Fatalf("pinned %v enabled %v %T: modal %v, want %v", pinned, enabled, msg, u.modal.kind, want)
				}
				var sent []any
				if want != modalNone {
					if len(b.Sent()) != 0 || u.modal.ws != "tab" {
						t.Fatal("close sent before confirmation or named the wrong tab")
					}
					if pinned {
						sent = append(sent, proto.SetTabOptions{WorkspaceID: "tab"})
					}
					u.confirmModal(&st)
				}
				sent = append(sent, msg)
				if u.modal.kind != modalNone || !reflect.DeepEqual(b.Sent(), sent) {
					t.Fatalf("more than one confirmation or close: modal %v, sent %#v", u.modal.kind, b.Sent())
				}
			}
		}
	}
}

func TestPinnedSplitWorkingClose(t *testing.T) {
	st := closeState()
	st.Workspaces[0].Pinned = true
	b := NewFakeBackend()
	u := &ui{b: b, cfg: config.Settings{ConfirmClose: true}}
	msg := proto.ClosePane{Pane: "a"}
	u.sendOrAsk(&st, msg)
	if u.modal.kind != modalClose || len(b.Sent()) != 0 {
		t.Fatalf("split close: modal %v, sent %#v", u.modal.kind, b.Sent())
	}
	u.confirmModal(&st)
	if !st.Workspaces[0].Pinned || !reflect.DeepEqual(b.Sent(), []any{msg}) {
		t.Fatalf("split close unpinned the tab or changed the target: %#v", b.Sent())
	}
}

func TestCancelWorkingClose(t *testing.T) {
	st := closeState()
	b := NewFakeBackend()
	b.st = st
	u := &ui{b: b, th: theme.Dark(), cfg: config.Settings{ConfirmClose: true}}
	u.sendOrAsk(&st, proto.CloseTab{WorkspaceID: "tab"})
	u.modal.cancel.Click()
	var ops op.Ops
	u.layoutModal(gl.Context{Ops: &ops, Constraints: gl.Exact(image.Pt(800, 600))}, &st)
	st = b.State()
	if u.modal.kind != modalNone || len(b.Sent()) != 0 || st.Workspace("tab") == nil {
		t.Fatal("Cancel closed the tab or left the dialog open")
	}
}

func TestWorkingCloseKey(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	u.cfg.ConfirmClose = true
	b := u.b.(*FakeBackend)
	pane, ws := u.nav.focused(), u.nav.workspace
	b.st.Activities = []model.Activity{{PaneID: pane, WorkspaceID: ws, Provider: model.ProviderClaude, State: model.StateWorking}}
	keys(press("W", key.ModCtrl|key.ModShift))
	if u.modal.kind != modalClose || u.modal.then != (proto.ClosePane{Pane: pane}) {
		t.Fatalf("close key: modal %v, request %#v", u.modal.kind, u.modal.then)
	}
	st := b.State()
	if findPane(&st, pane) == nil {
		t.Fatal("close key stopped the pane before confirmation")
	}
	u.confirmModal(&st)
	st = b.State()
	if u.modal.kind != modalNone || findPane(&st, pane) != nil {
		t.Fatal("confirmation did not close the pane")
	}
}
