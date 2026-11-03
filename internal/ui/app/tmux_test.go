package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestTmuxFocusWaitsForSplit(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, nav: nav{keys: aide}}
	u.nav.sync(&st)
	u.queueFocus(proto.FocusSession{WorkspaceID: "w7", SessionID: st.Workspaces[0].SessionID, Pane: "teammate"})
	u.applyFocus(&st)
	if u.focusReq == nil {
		t.Fatal("consumed focus before split arrived")
	}
	w := findWorkspace(&st, "w7")
	tab := shownTab(w)
	tab.Layout = layout.Split(tab.Layout, "k", "teammate", layout.Horizontal)
	st.Panes = append(st.Panes, model.Pane{ID: "teammate", WorkspaceID: w.ID})
	u.nav.sync(&st)
	u.applyFocus(&st)
	if u.nav.focused() != "teammate" || u.focusReq != nil {
		t.Fatalf("focus %q, pending %+v", u.nav.focused(), u.focusReq)
	}
}
