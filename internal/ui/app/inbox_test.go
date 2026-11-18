package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestInboxKeepsNewestPerTab(t *testing.T) {
	var ib inbox
	ib.add(model.Activity{WorkspaceID: "a"}, "a")
	ib.add(model.Activity{WorkspaceID: "a"}, "a again")
	if len(ib.items) != 1 || ib.items[0].title != "a again" {
		t.Fatalf("one entry per tab, newest title: %+v", ib.items)
	}
	for _, ws := range []string{"b", "c", "d", "e"} {
		ib.add(model.Activity{WorkspaceID: ws}, ws)
	}
	if len(ib.items) != inboxMax || ib.items[0].ws != "b" {
		t.Fatalf("cap at %d, oldest dropped: %+v", inboxMax, ib.items)
	}
	ib.drop("c")
	if len(ib.items) != inboxMax-1 {
		t.Fatalf("drop removes one tab: %+v", ib.items)
	}
}
