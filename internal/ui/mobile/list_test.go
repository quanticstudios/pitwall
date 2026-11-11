package mobile

import (
	"fmt"
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestWaitingFolds keeps Waiting on you to one row until it is opened:
// five waiting tabs took half a phone's screen above the tree.
func TestWaitingFolds(t *testing.T) {
	a := &App{th: theme.Dark()}
	a.st.Sessions = []model.Session{{ID: "s", Name: "main"}}
	for i := range 5 {
		id := fmt.Sprint("w", i)
		a.st.Workspaces = append(a.st.Workspaces, model.Workspace{ID: id, SessionID: "s", Name: id})
		a.st.Activities = append(a.st.Activities, model.Activity{WorkspaceID: id, PaneID: "p" + id, State: model.StatePendingApproval})
	}
	height := func() int {
		gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(400, 2000)}}
		return a.waiting(gtx).Size.Y
	}
	if h := height(); h >= 2*int(rowH) {
		t.Fatalf("five waiting tabs, folded, take %d px; want under two rows (%d)", h, 2*int(rowH))
	}
	a.list.waitOpen = true
	if h := height(); h < 6*int(rowH) {
		t.Fatalf("five waiting tabs, open, take %d px; want a header and five rows (%d)", h, 6*int(rowH))
	}
	a.st.Activities = a.st.Activities[:1]
	a.list.waitOpen = false
	if h := height(); h >= 2*int(rowH) {
		t.Fatalf("one waiting tab takes %d px; want one row", h)
	}
}
