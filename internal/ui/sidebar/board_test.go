package sidebar

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	lay "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// boardHarness draws the board 800 by 600 against st, n approvals that
// ask for a short command, each in a tab of its own, and a finished tab.
type boardHarness struct {
	s   Sidebar
	st  model.State
	r   input.Router
	ops op.Ops
	now time.Time
}

func newBoardHarness(n int) *boardHarness {
	h := &boardHarness{now: time.Unix(1e9, 0)}
	h.s.Answers = true
	add := func(id string, a model.Activity) {
		h.st.Workspaces = append(h.st.Workspaces, model.Workspace{ID: id, Label: id, Tabs: []model.Tab{{ID: "t" + id, Layout: &lay.Node{Pane: "p" + id}}}, ActiveTab: "t" + id})
		h.st.Panes = append(h.st.Panes, model.Pane{ID: "p" + id, WorkspaceID: id, Provider: model.ProviderClaude})
		a.PaneID, a.WorkspaceID, a.Provider = "p"+id, id, model.ProviderClaude
		h.st.Activities = append(h.st.Activities, a)
	}
	for i := range n {
		add(fmt.Sprintf("a%02d", i), model.Activity{State: model.StatePendingApproval, Command: "ls", UpdatedAt: h.now.Add(time.Duration(i-100) * time.Minute)})
	}
	add("done", model.Activity{State: model.StateCompleted, UpdatedAt: h.now.Add(-time.Hour)})
	h.s.BoardFocus()
	h.frame()
	h.frame()
	return h
}

func (h *boardHarness) frame() []Event {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(800, 600)), Now: h.now}
	evs := h.s.Board(gtx, theme.Dark(), &h.st, BoardOptions{})
	h.r.Frame(&h.ops)
	return evs
}

// key presses k on the focused board and draws the frames that follow.
func (h *boardHarness) key(k key.Name) {
	h.r.Queue(key.Event{Name: k, State: key.Press})
	h.frame()
	h.frame()
}

// TestBoardScrollKeepsAway: a selected card does not pull the board back
// to its column after the user scrolls sideways away from it.
func TestBoardScrollKeepsAway(t *testing.T) {
	h := newBoardHarness(1)
	h.s.bd.cur = "a00" // clicked
	h.frame()
	h.s.bd.x = 600
	h.frame()
	h.frame()
	if h.s.bd.x == 0 {
		t.Fatalf("scrolling away from the selected card's column snapped back")
	}
	h.key(key.NameDownArrow)
	if h.s.bd.cur != "a00" || h.s.bd.x != 0 {
		t.Errorf("Down: cursor %q, scrolled to %d, want a00 in view at 0", h.s.bd.cur, h.s.bd.x)
	}
}

// TestBoardAnswersShownCards: allow_prompt answers the focused card only
// while it is drawn whole on screen; an arrow key scrolls its column so it
// is.
func TestBoardAnswersShownCards(t *testing.T) {
	h := newBoardHarness(12)
	h.s.bd.cur = "a11" // below the column's fold
	for range 3 {
		h.now = h.now.Add(time.Second)
		h.frame()
	}
	if ev, ok := h.s.BoardAnswer(true, h.now); ok {
		t.Fatalf("answered a card below the fold: %+v", ev)
	}
	h.s.bd.cur = "a10"
	h.key(key.NameDownArrow) // back to a11, which the column scrolls to
	for range 3 {
		h.now = h.now.Add(time.Second)
		h.frame()
	}
	ev, ok := h.s.BoardAnswer(true, h.now)
	if a, _ := ev.(Answer); !ok || a.PaneID != "pa11" || !a.Allow {
		t.Fatalf("after Down onto a11: %+v %v, scroll %d", ev, ok, h.s.bd.colY[0])
	}
}
