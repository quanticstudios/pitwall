package app

import (
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestSidebarKeys drives focus_sidebar through Gio's router as the window
// does: the mode swallows keys, Enter opens a tab and gives its pane the
// keyboard, Space opens one and keeps it, Esc goes back, and a hidden
// sidebar shows only while the mode lasts.
func TestSidebarKeys(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	cs := key.ModCtrl | key.ModShift
	focus := press("H", cs)
	if got := keys(focus); got != "" || !u.sidebar.Focused() {
		t.Fatalf("Ctrl+Shift+H: focused %v, pane got %q", u.sidebar.Focused(), got)
	}
	// Ctrl+T reaches the shell in conventional, but not from the sidebar.
	if got := keys(press(key.NameDownArrow, 0), press("T", key.ModCtrl), press("J", 0)); got != "" {
		t.Fatalf("keys in the sidebar reached the pane: %q", got)
	}
	if u.nav.workspace != "w1" {
		t.Fatalf("moving the cursor switched to %s", u.nav.workspace)
	}
	if keys(press(key.NameSpace, 0)); u.nav.workspace != "w1c" || !u.sidebar.Focused() {
		t.Fatalf("Space: at %s, focused %v; want w1c, still focused", u.nav.workspace, u.sidebar.Focused())
	}
	if keys(press("K", 0), press(key.NameReturn, 0)); u.nav.workspace != "w1b" || u.sidebar.Focused() {
		t.Fatalf("Enter: at %s, focused %v; want w1b and the pane focused", u.nav.workspace, u.sidebar.Focused())
	}
	if got := keys(press("T", key.ModCtrl)); got != "\x14" {
		t.Fatalf("after Enter the pane got %q, want Ctrl+T", got)
	}

	keys(press("B", cs)) // hide the sidebar; keys returns all the pane got so far
	if keys(focus); !u.sidebar.Focused() || u.nav.sidebarHidden {
		t.Fatalf("focus with the sidebar hidden: focused %v, hidden %v", u.sidebar.Focused(), u.nav.sidebarHidden)
	}
	if got := keys(press(key.NameUpArrow, 0), press(key.NameEscape, 0)); got != "\x14" || u.sidebar.Focused() || !u.nav.sidebarHidden || u.nav.workspace != "w1b" {
		t.Fatalf("Esc: focused %v, hidden %v, at %s, pane got %q", u.sidebar.Focused(), u.nav.sidebarHidden, u.nav.workspace, got)
	}
	if got := keys(press("T", key.ModCtrl)); got != "\x14\x14" {
		t.Fatalf("after Esc the pane got %q", got)
	}

	keys(press("B", cs))
	keys(focus)
	if keys(focus); u.sidebar.Focused() {
		t.Fatal("focus_sidebar again did not give the keyboard back")
	}
	keys(focus)
	if keys(press(key.NameDeleteForward, 0)); u.modal.kind != modalDelete || u.modal.ws != "w1b" || u.sidebar.Focused() {
		t.Fatalf("Delete: dialog %v for %s, focused %v", u.modal.kind, u.modal.ws, u.sidebar.Focused())
	}
}

// TestSidebarMenuKeys: Shift+F10 opens the cursor's row menu, Down and
// Enter run an entry (Rename tab), and the rename field gets the keys.
func TestSidebarMenuKeys(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	keys(press("H", key.ModCtrl|key.ModShift), press(key.NameF10, key.ModShift))
	if u.sidebar.Editing() {
		t.Fatal("renaming before the menu ran")
	}
	if got := keys(press(key.NameDownArrow, 0), press(key.NameReturn, 0)); got != "" || !u.sidebar.Editing() || !u.sidebar.Focused() {
		t.Fatalf("Down Enter in the menu: renaming %v, focused %v, pane got %q", u.sidebar.Editing(), u.sidebar.Focused(), got)
	}
	keys(press(key.NameEscape, 0)) // the rename field's
	if u.sidebar.Editing() || !u.sidebar.Focused() {
		t.Fatalf("Esc in the rename field: renaming %v, focused %v", u.sidebar.Editing(), u.sidebar.Focused())
	}
}

// TestSidebarAnswer: allow_prompt answers the prompt of the cursor's row,
// a tab in a collapsed group, not the focused pane's.
func TestSidebarAnswer(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	cs := key.ModCtrl | key.ModShift
	// End is the last group's header; Up, then Right twice opens group
	// billing and steps onto brave-ant, whose Claude asks.
	keys(press("H", cs), press(key.NameEnd, 0), press(key.NameUpArrow, 0), press(key.NameRightArrow, 0), press(key.NameRightArrow, 0))
	keys(press("Y", cs))
	var got []proto.Answer
	for _, m := range u.b.(*FakeBackend).Sent() {
		if a, ok := m.(proto.Answer); ok {
			got = append(got, a)
		}
	}
	if len(got) != 1 || got[0].Pane != "g" || !got[0].Allow || !u.sidebar.Focused() {
		t.Fatalf("Ctrl+Shift+Y on brave-ant sent %+v, focused %v", got, u.sidebar.Focused())
	}
}
