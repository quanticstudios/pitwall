package sidebar

import (
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	lay "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// manyTabs is a session of n agent tabs, every third one asking for an
// approval, as a phone's list of a busy session.
func manyTabs(n int) model.State {
	st := model.State{Sessions: []model.Session{{ID: "s", Name: "main"}}}
	for i := range n {
		id, pane := fmt.Sprint("w", i), fmt.Sprint("p", i)
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: id, SessionID: "s", Name: id, Tabs: []model.Tab{{Layout: &lay.Node{Pane: pane}}}})
		st.Panes = append(st.Panes, model.Pane{ID: pane, WorkspaceID: id, Provider: model.ProviderClaude})
		state := model.StateWorking
		if i%3 == 0 {
			state = model.StatePendingApproval
		}
		st.Activities = append(st.Activities, model.Activity{PaneID: pane, WorkspaceID: id, Provider: model.ProviderClaude, State: state})
	}
	return st
}

// BenchmarkPhoneFrame lays out the phone's sidebar over 60 tabs, a frame
// with no change, as the app draws while a row's spinner turns.
func BenchmarkPhoneFrame(b *testing.B) {
	st := manyTabs(60)
	s := Sidebar{TreeOnly: true, Fill: true, Touch: true, ExpandAll: true, Answers: true, SeparateWorkers: true, Rev: 1}
	th := theme.Dark()
	var r input.Router
	var ops op.Ops
	now := time.Now()
	for b.Loop() {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 3.6, PxPerSp: 3.6}, Constraints: layout.Exact(image.Pt(1080, 2000)), Now: now}
		s.Layout(gtx, th, &st, "s", "")
		r.Frame(&ops)
	}
}

// TestViewCache keeps the session's view while Rev holds: a tab added
// without a new Rev stays out, a new Rev or another session shows it,
// and Rev zero shows it at once.
func TestViewCache(t *testing.T) {
	now := time.Now()
	for _, rev := range []uint64{0, 1} {
		st := manyTabs(2)
		s := Sidebar{TreeOnly: true, Rev: rev}
		draw := func() []string {
			return labels(func(gtx layout.Context) { s.Layout(gtx, theme.Dark(), &st, "s", "") }, 288, now)
		}
		draw()
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: "late", SessionID: "s", Name: "late-tab"})
		st.Activities = append(st.Activities, model.Activity{WorkspaceID: "late", PaneID: "pl", State: model.StateWorking})
		if got := slices.Contains(draw(), "late-tab"); got != (rev == 0) {
			t.Fatalf("Rev %d: the new tab shows %v before Rev changed", rev, got)
		}
		s.Rev++
		if !slices.Contains(draw(), "late-tab") {
			t.Fatalf("Rev %d: the new tab is missing after Rev changed", rev)
		}
	}
}

// TestNearRows polls the rows near the view and the busy ones: a row two
// screens down takes no polling until the list scrolls to it or its menu
// opens, and a tap on a row on screen still opens its tab.
func TestNearRows(t *testing.T) {
	st := manyTabs(60)
	s := Sidebar{TreeOnly: true, Touch: true, Rev: 1}
	var r input.Router
	var ops op.Ops
	var evs []Event
	now := time.Now()
	frame := func() {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(288, 300)), Now: now}
		_, e := s.Layout(gtx, theme.Dark(), &st, "s", "")
		evs = append(evs, e...)
		r.Frame(&ops)
	}
	if s.nearRows() != nil {
		t.Fatal("before the first frame, not every row polls")
	}
	frame()
	near := s.nearRows()
	if !near["w0"] || near["w59"] {
		t.Fatalf("near rows %v; want w0 and not w59", near)
	}
	gtx := layout.Context{Ops: new(op.Ops), Source: r.Source()}
	if s.rowBusy(gtx, "w59", s.row("w59")) {
		t.Fatal("an idle row off screen is busy")
	}
	s.menuWS = "w59"
	if !s.rowBusy(gtx, "w59", s.row("w59")) {
		t.Fatal("a row with its menu open is not busy")
	}
	s.menuWS = ""
	at := f32.Pt(100, 30)
	r.Queue(
		pointer.Event{Kind: pointer.Press, Position: at, Buttons: pointer.ButtonPrimary, Source: pointer.Touch},
		pointer.Event{Kind: pointer.Release, Position: at, Source: pointer.Touch},
	)
	frame()
	frame()
	if !slices.ContainsFunc(evs, func(e Event) bool { sw, ok := e.(SelectWorkspace); return ok && sw.WorkspaceID == "w0" }) {
		t.Fatalf("a tap on the first row gave %v", evs)
	}
	s.list.Position.Offset = s.elems[len(s.elems)-1].bot
	if near := s.nearRows(); !near["w59"] {
		t.Fatalf("scrolled to the end, near rows %v lack w59", near)
	}
}
