package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestDoneListKeepsNewestPerTab(t *testing.T) {
	var ib doneList
	ib.add(model.Activity{WorkspaceID: "a"}, "a")
	ib.add(model.Activity{WorkspaceID: "a"}, "a again")
	if len(ib.items) != 1 || ib.items[0].title != "a again" {
		t.Fatalf("one entry per tab, newest title: %+v", ib.items)
	}
	for _, ws := range []string{"b", "c", "d", "e"} {
		ib.add(model.Activity{WorkspaceID: ws}, ws)
	}
	if len(ib.items) != doneMax || ib.items[0].ws != "b" {
		t.Fatalf("cap at %d, oldest dropped: %+v", doneMax, ib.items)
	}
	ib.drop("c")
	if len(ib.items) != doneMax-1 {
		t.Fatalf("drop removes one tab: %+v", ib.items)
	}
}

// A tab on screen in a window in the background keeps its notice until the
// window has focus: the user has not seen it yet.
func TestDoneKeepsShownTabUntilFocus(t *testing.T) {
	var u ui
	u.nav.workspace = "a"
	u.queueDone(model.Activity{WorkspaceID: "a"}, "a")
	u.applyDone()
	if len(u.doneTabs.items) != 1 {
		t.Fatalf("unfocused window dropped the shown tab: %+v", u.doneTabs.items)
	}
	u.winFocused = true
	u.applyDone()
	if len(u.doneTabs.items) != 0 {
		t.Fatalf("focused window kept the shown tab: %+v", u.doneTabs.items)
	}
}
