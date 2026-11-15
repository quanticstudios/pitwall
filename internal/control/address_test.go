package control

import (
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// cliState has two sessions: main, the most recently used, with three tabs
// and a group, and api with one tab.
func cliState() model.State {
	return model.State{
		Sessions: []model.Session{
			{ID: "m", Name: "main", Order: []string{"g", "c"}, UsedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
			{ID: "s2", Name: "api", UsedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Projects: []model.Project{{ID: "g", SessionID: "m", Name: "agents"}},
		Workspaces: []model.Workspace{
			{ID: "a", SessionID: "m", Label: "fix login", Path: "/work/a", ProjectID: "g", Tabs: []model.Tab{{ID: "ta"}}},
			{ID: "b", SessionID: "m", Name: "alpine", NameSet: true, Label: "alpine", Path: "/work/b", Detached: true, Tabs: []model.Tab{{ID: "tc"}}},
			{ID: "c", SessionID: "m", Label: "~", Path: "/work/c"},
			{ID: "z", SessionID: "s2", Label: "server", Path: "/work/z"},
		},
		Panes: []model.Pane{{ID: "pa", WorkspaceID: "a"}, {ID: "pb", WorkspaceID: "b"}, {ID: "pz", WorkspaceID: "z"}},
		Activities: []model.Activity{
			{WorkspaceID: "a", Provider: model.ProviderClaude, State: model.StateWorking},
			{WorkspaceID: "a", Provider: model.ProviderCodex, State: model.StateError},
		},
	}
}

func TestResolvePane(t *testing.T) {
	st := driveState(model.ProviderCodex, "", false, 0)
	st.Workspaces[0].Tabs[0].Layout = &layout.Node{Children: []*layout.Node{{Pane: "shell"}, {Pane: "p"}, {Pane: "q"}}}
	st.Panes = append(st.Panes, model.Pane{ID: "shell", WorkspaceID: "w"}, model.Pane{ID: "q", WorkspaceID: "w"})
	// A tab whose title ends in .N is that tab, not a pane of tab "v1".
	st.Workspaces = append(st.Workspaces, model.Workspace{ID: "v", SessionID: "m", Name: "v1.2", NameSet: true, Tabs: []model.Tab{{ID: "tv", Layout: &layout.Node{Pane: "vp"}}}})
	st.Sessions[0].Order = []string{"g", "v"}
	st.Panes = append(st.Panes, model.Pane{ID: "vp", WorkspaceID: "v"})
	for _, tc := range []struct{ addr, want, err string }{
		{"build", "p", ""}, // the main pane: the agent, not the first pane
		{"1", "p", ""},
		{"#1", "p", ""},
		{"#1.1", "shell", ""},
		{"1.3", "q", ""},
		{"bui.2", "p", ""},
		{"build.3", "q", ""},
		{"v1.2", "vp", ""},
		{"v1.2.1", "vp", ""},
		{"#2.1", "vp", ""},
		{"1.4", "", "no pane 4; it has 3"},
		{"1.0", "", "no pane 0"},
		{"#3.1", "", "no tab #3"},
		{"nope.1", "", "no tab matches"},
	} {
		_, p, err := ResolvePane(st, Caller{}, "m", tc.addr)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: %v %v, want an error with %q", tc.addr, p.ID, err, tc.err)
			}
			continue
		}
		if err != nil || p.ID != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.addr, p.ID, err, tc.want)
		}
	}
}

func TestResolveTab(t *testing.T) {
	state := cliState()
	c := Caller{Pane: "pb"}
	for _, tc := range []struct{ name, want, error string }{
		{"1", "a", ""}, {"#2", "c", ""}, {"3", "b", ""}, {"", "b", ""},
		{"fix login", "a", ""}, {"fix", "a", ""}, {"alp", "b", ""},
		{"4", "", "no tab #4 (see pitwall ls)"},
		{"absent", "", "no tab matches \"absent\" (see pitwall ls)"},
		{"@a", "a", ""}, {"@pb", "b", ""},
		{"@gone", "", "no pane @gone: it closed (see pitwall ls --json)"},
	} {
		w, err := ResolveTab(state, c, "m", tc.name)
		if tc.error != "" {
			if err == nil || err.Error() != tc.error {
				t.Fatalf("%q: %v", tc.name, err)
			}
		} else if err != nil || w.ID != tc.want {
			t.Fatalf("%q: %+v, %v", tc.name, w, err)
		}
	}
	// An exact title beats a prefix; two equal titles are ambiguous.
	state.Workspaces = append(state.Workspaces, model.Workspace{ID: "d", SessionID: "m", Label: "fix"})
	state.Sessions[0].Order = append(state.Sessions[0].Order, "d")
	if w, err := ResolveTab(state, c, "m", "fix"); err != nil || w.ID != "d" {
		t.Fatalf("exact title: %+v %v", w, err)
	}
	state.Workspaces = append(state.Workspaces, model.Workspace{ID: "e", SessionID: "m", Label: "fix"})
	if _, err := ResolveTab(state, c, "m", "fix"); err == nil || err.Error() != "ambiguous tab \"fix\"; matches #3, #4" {
		t.Fatalf("equal titles: %v", err)
	}
	if _, err := ResolveTab(state, Caller{}, "m", ""); err == nil {
		t.Fatal("resolved no name outside pane")
	}
	// #N counts in the session asked for.
	if w, err := ResolveTab(state, c, "s2", "1"); err != nil || w.ID != "z" {
		t.Fatalf("#1 of api: %+v %v", w, err)
	}
}

// Tab commands act on the calling pane's session, else the most recently
// used one; -s picks one by name or unique prefix.
func TestCurrentSession(t *testing.T) {
	state := cliState()
	for _, tc := range []struct{ pane, flag, want, error string }{
		{"", "", "m", ""},
		{"pz", "", "s2", ""},
		{"pz", "main", "m", ""},
		{"", "ap", "s2", ""},
		{"gone", "", "m", ""},
		{"", "x", "", "no session matches \"x\" (see pitwall session ls)"},
	} {
		s, err := CurrentSession(state, Caller{Session: tc.flag, Pane: tc.pane})
		if tc.error != "" {
			if err == nil || err.Error() != tc.error {
				t.Errorf("%+v: %v", tc, err)
			}
		} else if err != nil || s.ID != tc.want {
			t.Errorf("%+v: %+v %v", tc, s, err)
		}
	}
	state.Sessions = append(state.Sessions, model.Session{ID: "s3", Name: "apex"})
	if _, err := ResolveSession(state, "ap"); err == nil || err.Error() != "ambiguous session \"ap\"; matches api, apex" {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := CurrentSession(model.State{}, Caller{}); err == nil {
		t.Fatal("a session out of nothing")
	}
}

// Two callers in different sessions resolve #1 to their own session's tab.
func TestCallersResolveOwnSession(t *testing.T) {
	state := cliState()
	for _, tc := range []struct {
		c    Caller
		want string
	}{
		{Caller{Pane: "pa"}, "a"},
		{Caller{Pane: "pz"}, "z"},
		{Caller{Session: "main"}, "a"},
		{Caller{Session: "api", Pane: "pa"}, "z"}, // a session named beats the pane's
	} {
		s, err := CurrentSession(state, tc.c)
		if err != nil {
			t.Fatalf("%+v: %v", tc.c, err)
		}
		if w, err := ResolveTab(state, tc.c, s.ID, "#1"); err != nil || w.ID != tc.want {
			t.Errorf("%+v: #1 is %q %v, want %q", tc.c, w.ID, err, tc.want)
		}
	}
}

func TestChildTabs(t *testing.T) {
	state := cliState()
	state.Workspaces[2].Parent = "a"
	if got, err := ChildTabs(state, Caller{Pane: "pa"}); err != nil || len(got) != 1 || !got["c"] {
		t.Fatalf("children of a: %v %v", got, err)
	}
	if got, err := ChildTabs(state, Caller{Pane: "pz"}); err != nil || len(got) != 0 {
		t.Fatalf("children of z: %v %v", got, err)
	}
	for _, pane := range []string{"", "gone"} {
		if _, err := ChildTabs(state, Caller{Pane: pane}); err == nil {
			t.Errorf("pane %q: children without a calling pane", pane)
		}
	}
}
