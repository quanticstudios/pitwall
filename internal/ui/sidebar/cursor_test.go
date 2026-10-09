package sidebar

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	pwlayout "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// cursorState is u1, then tab m with three agents (p2 asking for
// approval) beside a shell, group g1 with a and b, and group g2 with c.
func cursorState() model.State {
	split := &pwlayout.Node{Children: []*pwlayout.Node{{Pane: "p1"}, {Pane: "p2"}, {Pane: "sh"}, {Pane: "p3"}}}
	return model.State{
		Projects: []model.Project{{ID: "g1", Name: "one"}, {ID: "g2", Name: "two"}},
		Workspaces: []model.Workspace{
			{ID: "u1", Name: "u1"},
			{ID: "m", Name: "m", Tabs: []model.Tab{{Layout: split}}},
			{ID: "a", Name: "a", ProjectID: "g1"}, {ID: "b", Name: "b", ProjectID: "g1"},
			{ID: "c", Name: "c", ProjectID: "g2"},
		},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "m", Provider: model.ProviderClaude},
			{ID: "p2", WorkspaceID: "m", Provider: model.ProviderCodex},
			{ID: "sh", WorkspaceID: "m"},
			{ID: "p3", WorkspaceID: "m", Provider: model.ProviderClaude},
		},
		Activities: []model.Activity{
			{PaneID: "p1", WorkspaceID: "m", Provider: model.ProviderClaude, State: model.StateWorking},
			{PaneID: "p2", WorkspaceID: "m", Provider: model.ProviderCodex, State: model.StatePendingApproval, UpdatedAt: time.Unix(5, 0)},
			{PaneID: "p3", WorkspaceID: "m", Provider: model.ProviderClaude, State: model.StateCompleted},
		},
	}
}

func cursorAt(s *Sidebar) string { return fmt.Sprintf("%c:%s", s.cur.kind, s.cur.id) }

// TestCursorMoves walks the cursor through headers, tabs and sub-rows:
// Up and Down in drawn order, Left and Right folding and moving between a
// row and its children, Home and End.
func TestCursorMoves(t *testing.T) {
	st := cursorState()
	var s Sidebar
	s.expanded = map[string]bool{"g1": true} // g2 starts collapsed
	_, rows := s.cursorView(&st, "", "m")
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%c:%s", r.kind, r.id))
	}
	if want := "s:u1 s:m a:p1 a:p2 a:p3 g:g1 s:a s:b g:g2"; strings.Join(got, " ") != want {
		t.Fatalf("rows %v, want %s", got, want)
	}
	s.Focus(&st, "", "m")
	if cursorAt(&s) != "a:p2" {
		t.Fatalf("starts on %s, want the asking sub-row a:p2", cursorAt(&s))
	}
	if a := s.Asking(&st, "", "m"); a == nil || a.PaneID != "p2" {
		t.Fatalf("Asking on p2 = %+v", a)
	}
	for i, step := range []struct {
		k    key.Name
		want string
	}{
		{key.NameUpArrow, "a:p1"},
		{key.NameLeftArrow, "s:m"},   // a sub-row's parent
		{key.NameLeftArrow, "s:m"},   // folds m's sub-rows
		{key.NameDownArrow, "g:g1"},  // the sub-rows are gone
		{key.NameRightArrow, "s:a"},  // an open group's first tab
		{"H", "g:g1"},                // a tab's group
		{"H", "g:g1"},                // collapses g1
		{"J", "g:g2"},                // past g1's hidden tabs
		{key.NameRightArrow, "g:g2"}, // expands g2
		{"L", "s:c"},
		{key.NameEnd, "s:c"},
		{key.NameHome, "s:u1"},
		{"K", "s:u1"}, // the top stays put
		{key.NameDownArrow, "s:m"},
		{key.NameRightArrow, "s:m"}, // unfolds m's sub-rows
		{key.NameRightArrow, "a:p1"},
		{key.NameRightArrow, "a:p1"}, // a sub-row has no children
		{key.NamePageDown, "s:c"},
		{key.NamePageUp, "s:u1"},
	} {
		if _, _, used := s.Key(&st, "", "m", key.Event{Name: step.k, State: key.Press}); !used {
			t.Fatalf("step %d: %s not used", i, step.k)
		}
		if cursorAt(&s) != step.want {
			t.Fatalf("step %d: %s put the cursor on %s, want %s", i, step.k, cursorAt(&s), step.want)
		}
	}
	if s.isExpanded("g1") || !s.isExpanded("g2") || s.folded["m"] {
		t.Fatalf("expanded %v, folded %v", s.expanded, s.folded)
	}
	if _, _, used := s.Key(&st, "", "m", key.Event{Name: "T", Modifiers: key.ModCtrl, State: key.Press}); used {
		t.Fatal("the sidebar took Ctrl+T")
	}
}

// TestCursorKeys: Enter opens a tab or focuses a sub-row's pane and gives
// the keyboard back, Space opens without giving it back, Enter folds a
// group, Esc goes back, Delete asks to delete the tab, and Shift+F10 opens
// the row's menu.
func TestCursorKeys(t *testing.T) {
	st := cursorState()
	var s Sidebar
	s.expanded = map[string]bool{"g1": true}
	s.Focus(&st, "", "u1")
	press := func(n key.Name, m key.Modifiers) ([]Event, bool) {
		t.Helper()
		evs, back, used := s.Key(&st, "", "u1", key.Event{Name: n, Modifiers: m, State: key.Press})
		if !used {
			t.Fatalf("%v+%s not used", m, n)
		}
		return evs, back
	}
	press(key.NameDownArrow, 0)
	if evs, back := press(key.NameSpace, 0); back || len(evs) != 1 || evs[0] != (SelectWorkspace{WorkspaceID: "m", PaneID: "p2"}) {
		t.Fatalf("Space on m: %v, back %v", evs, back)
	}
	if evs, back := press(key.NameReturn, 0); !back || len(evs) != 1 || evs[0].(SelectWorkspace).WorkspaceID != "m" {
		t.Fatalf("Enter on m: %v, back %v", evs, back)
	}
	press(key.NameDownArrow, 0)
	press(key.NameDownArrow, 0)
	if evs, back := press(key.NameReturn, 0); !back || len(evs) != 1 || evs[0] != (SelectWorkspace{WorkspaceID: "m", PaneID: "p2"}) {
		t.Fatalf("Enter on p2: %v, back %v", evs, back)
	}
	if evs, _ := press(key.NameDeleteForward, 0); len(evs) != 1 || evs[0] != (DeleteWorkspace{WorkspaceID: "m"}) {
		t.Fatalf("Delete on p2: %v", evs)
	}
	press(key.NameF10, key.ModShift)
	if s.menuWS != "m" {
		t.Fatalf("Shift+F10 on p2 opened %q, want m's menu", s.menuWS)
	}
	press(key.NameEscape, 0)
	if s.menuWS != "" || !s.Focused() {
		t.Fatalf("Esc left menu %q, focused %v", s.menuWS, s.Focused())
	}
	press(key.NameDownArrow, 0)
	press(key.NameDownArrow, 0)
	if evs, back := press(key.NameReturn, 0); back || len(evs) != 0 || s.isExpanded("g1") {
		t.Fatalf("Enter on g1: %v, back %v, expanded %v", evs, back, s.isExpanded("g1"))
	}
	press(MenuKey, 0)
	if s.groupMenu != "g1" {
		t.Fatalf("Menu on g1 opened %q", s.groupMenu)
	}
	press(key.NameEscape, 0)
	if _, back := press(key.NameEscape, 0); !back {
		t.Fatal("Esc did not give the keyboard back")
	}
	if a := s.Asking(&st, "", "u1"); a != nil {
		t.Fatalf("Asking on a header = %+v", a)
	}
	s.Blur()
	if s.Focused() {
		t.Fatal("Blur left the sidebar focused")
	}
}
