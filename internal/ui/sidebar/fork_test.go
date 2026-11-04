package sidebar

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestForkPaneSelection(t *testing.T) {
	ws := model.Workspace{ID: "w", Tabs: []model.Tab{{Layout: &layout.Node{Children: []*layout.Node{{Pane: "first"}, {Pane: "second"}}}}}}
	for _, tc := range []struct {
		name, focus, session string
		provider             model.Provider
		want                 string
	}{
		{"first agent", "", "s", model.ProviderClaude, "first"},
		{"focused agent", "second", "s", model.ProviderClaude, "second"},
		{"unsupported", "first", "s", model.ProviderPi, ""},
		{"no session", "first", "", model.ProviderCodex, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := model.State{Workspaces: []model.Workspace{ws}, Panes: []model.Pane{
				{ID: "first", WorkspaceID: "w", Provider: tc.provider, SessionID: tc.session},
				{ID: "second", WorkspaceID: "w", Provider: model.ProviderCodex, SessionID: "second-session"},
			}}
			s := Sidebar{Pane: tc.focus}
			if got := s.forkPane(&st, ws); got != tc.want {
				t.Fatalf("fork pane = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestForkMenuWiring(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capable  bool
		provider model.Provider
		session  string
		shown    bool
	}{
		{"ready", true, model.ProviderClaude, "s", true}, {"no session", true, model.ProviderCodex, "", false}, {"unsupported", true, model.ProviderPi, "s", false}, {"old daemon", false, model.ProviderClaude, "s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDragHarness(t)
			h.st.Panes = []model.Pane{{ID: "p", WorkspaceID: "u1", Provider: tc.provider, SessionID: tc.session}}
			h.st.Workspaces[0].Tabs = []model.Tab{{Layout: layout.Leaf("p")}}
			h.s.Forks = tc.capable
			h.s.Pane = "p"
			h.s.cur.on = true
			h.s.menuWS = "u1"
			h.frame()
			if shown := slices.Contains(h.s.cur.items, &h.s.menuItem[actFork]); shown != tc.shown {
				t.Fatalf("Fork shown %v", shown)
			}
			h.s.menuItem[actFork].Click()
			evs := h.frame()
			if sent := slices.Contains(evs, Event(ForkPane{Pane: "p"})); sent != tc.shown {
				t.Fatalf("events %v", evs)
			}
		})
	}
}
