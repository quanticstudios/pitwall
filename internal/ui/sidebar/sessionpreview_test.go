package sidebar

import (
	"image"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestSessionPreviewOrder(t *testing.T) {
	st := model.State{}
	for _, a := range []model.Activity{
		{WorkspaceID: "seen", State: model.StateCompleted},
		{WorkspaceID: "work", State: model.StateWorking},
		{WorkspaceID: "done", State: model.StateCompleted, Unseen: true},
		{WorkspaceID: "plan", State: model.StateWorking},
		{WorkspaceID: "error", State: model.StateError},
		{WorkspaceID: "approval", State: model.StatePendingApproval},
		{WorkspaceID: "input", State: model.StateAwaitingInput},
		{WorkspaceID: "pr", State: model.StatePROpen, Unseen: true},
		{WorkspaceID: "idle"},
	} {
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: a.WorkspaceID, SessionID: "remote"})
		st.Activities = append(st.Activities, a)
	}
	st.Activities = append(st.Activities, model.Activity{WorkspaceID: "plan", State: model.StatePlanReady})
	st.Workspaces = append(st.Workspaces, model.Workspace{ID: "other", SessionID: "local"}, model.Workspace{ID: "detached", SessionID: "remote", Detached: true})
	st.Activities = append(st.Activities, model.Activity{WorkspaceID: "other", State: model.StateError}, model.Activity{WorkspaceID: "detached", State: model.StateError})
	tabs := sessionPreviewTabs(&st, "remote")
	var ids []string
	for _, tab := range tabs {
		ids = append(ids, tab.ws.ID)
	}
	want := []string{"error", "approval", "input", "plan", "pr", "done", "work", "seen", "idle"}
	if !slices.Equal(ids, want) {
		t.Fatalf("tabs %v, want %v", ids, want)
	}
	if tabs[7].dot != nil || tabs[7].activity.State != model.StateCompleted {
		t.Fatalf("seen completion: %+v", tabs[7])
	}
}

func TestSessionPreviewDisabled(t *testing.T) {
	var s Sidebar
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Unix(1e9, 0)}
	s.sessionPreviewFrame(gtx, "remote")
	gtx.Now = gtx.Now.Add(time.Second)
	s.sessionPreviewFrame(gtx, "remote")
	if s.preview.hover.shown != "" {
		t.Fatal("disabled preview opened")
	}
	s.SessionPreview = true
	s.sessionPreviewFrame(gtx, "remote")
	gtx.Now = gtx.Now.Add(sessionPreviewDelay)
	s.sessionPreviewFrame(gtx, "remote")
	if s.preview.hover.shown != "remote" {
		t.Fatal("enabled preview did not open")
	}
	s.SessionPreview = false
	s.sessionPreviewFrame(gtx, "remote")
	if s.preview.hover.shown != "" || s.preview.hover.over != "" {
		t.Fatal("turning the setting off left the preview open")
	}
}

func TestSessionPreviewHoverAndClick(t *testing.T) {
	st := model.State{
		Sessions:   []model.Session{{ID: "local", Name: "main"}, {ID: "remote", Name: "remote"}},
		Workspaces: []model.Workspace{{ID: "here", SessionID: "local"}},
	}
	for i := range 15 {
		id := strconv.Itoa(i)
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: id, Name: "Tab " + id, SessionID: "remote"})
		state := model.StateWorking
		if i == 14 {
			state = model.StateError
		}
		st.Activities = append(st.Activities, model.Activity{WorkspaceID: id, State: state, Provider: model.ProviderCodex})
	}
	s := Sidebar{SessionPreview: true, Window: image.Pt(900, 800), Hosts: []HostInfo{
		{Up: true, Sessions: []string{"local"}}, {Name: "box", Up: true, Sessions: []string{"remote"}},
	}}
	var r input.Router
	var ops op.Ops
	now := time.Unix(1e9, 0)
	th := theme.Dark()
	frame := func() []Event {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(288, 800)), Now: now}
		_, evs := s.Layout(gtx, th, &st, "local", "here")
		r.Frame(&ops)
		return evs
	}
	move := func(x, y float32) {
		for i := range 2 {
			r.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(x+float32(i), y), Source: pointer.Mouse})
			frame()
		}
	}
	frame()
	move(100, 156)
	now = now.Add(sessionPreviewDelay - time.Millisecond)
	frame()
	if s.preview.hover.shown != "" {
		t.Fatal("preview opened before 400ms")
	}
	now = now.Add(time.Millisecond)
	frame()
	if s.preview.hover.shown != "remote" {
		t.Fatalf("preview %q, want remote", s.preview.hover.shown)
	}
	var labels []string
	var collect func([]input.SemanticNode)
	collect = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			labels = append(labels, n.Desc.Label)
			collect(n.Children)
		}
	}
	collect(r.AppendSemantics(nil))
	if !slices.Contains(labels, "+3 more") || slices.Contains(labels, "Tab 11") {
		t.Fatalf("preview did not cap its rows: %q", labels)
	}
	move(350, 192)
	now = now.Add(time.Second)
	frame()
	if !s.preview.inside || s.preview.hover.shown != "remote" {
		t.Fatal("pointer on the preview did not keep it open")
	}
	move(850, 700)
	now = now.Add(hoverGrace)
	frame()
	if s.preview.hover.shown != "" {
		t.Fatal("preview stayed open off its row and popover")
	}
	move(100, 156)
	now = now.Add(sessionPreviewDelay)
	frame()
	move(350, 192)
	st.Activities[13].State = model.StateError
	frame()
	at := f32.Pt(350, 192)
	r.Queue(pointer.Event{Kind: pointer.Press, Position: at, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary},
		pointer.Event{Kind: pointer.Release, Position: at, Source: pointer.Mouse})
	want := []Event{ShowSession{SessionID: "remote"}, SelectWorkspace{WorkspaceID: "14"}}
	if evs := frame(); !reflect.DeepEqual(evs, want) {
		t.Fatalf("preview click: %#v, want %#v", evs, want)
	}
	if s.preview.hover.shown != "" {
		t.Fatal("click left the preview open")
	}
}

func TestSessionPreviewSwitchDelay(t *testing.T) {
	s := Sidebar{SessionPreview: true}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Now: time.Unix(1e9, 0)}
	a, b := image.Rect(0, 100, 288, 128), image.Rect(0, 128, 288, 156)
	s.preview.rowAnchor = a
	s.sessionPreviewFrame(gtx, "a")
	gtx.Now = gtx.Now.Add(sessionPreviewDelay)
	s.sessionPreviewFrame(gtx, "a")
	s.preview.rowAnchor = b
	gtx.Now = gtx.Now.Add(time.Millisecond)
	s.sessionPreviewFrame(gtx, "b")
	if s.preview.hover.shown != "a" || s.preview.anchor != a {
		t.Fatal("crossing another row changed the preview or anchor")
	}
	gtx.Now = gtx.Now.Add(100 * time.Millisecond)
	s.preview.inside = true
	s.sessionPreviewFrame(gtx, "")
	if s.preview.hover.shown != "a" || s.preview.anchor != a {
		t.Fatal("entering the popover lost the original session")
	}
	s.preview.inside = false
	s.sessionPreviewFrame(gtx, "b")
	gtx.Now = gtx.Now.Add(sessionPreviewDelay - time.Millisecond)
	s.sessionPreviewFrame(gtx, "b")
	if s.preview.hover.shown != "a" {
		t.Fatal("another session opened before its delay")
	}
	gtx.Now = gtx.Now.Add(time.Millisecond)
	s.sessionPreviewFrame(gtx, "b")
	if s.preview.hover.shown != "b" || s.preview.anchor != b || !s.preview.hover.shownAt.Equal(gtx.Now) {
		t.Fatal("another session did not open at its row after the delay")
	}
}

func TestSessionPreviewFrozenOrder(t *testing.T) {
	st := model.State{
		Workspaces: []model.Workspace{{ID: "a", SessionID: "s"}, {ID: "b", SessionID: "s"}},
		Activities: []model.Activity{{WorkspaceID: "a", State: model.StateWorking}, {WorkspaceID: "b", State: model.StateWorking}},
	}
	var p sessionPreview
	p.tabs(&st, "s")
	p.inside = true
	st.Activities[1].State = model.StateError
	tabs := p.tabs(&st, "s")
	if tabs[0].ws.ID != "a" || tabs[1].activity.State != model.StateError {
		t.Fatalf("order changed or pill stayed stale: %+v", tabs)
	}
	p.inside = false
	if tabs := p.tabs(&st, "s"); tabs[0].ws.ID != "b" {
		t.Fatal("order did not update after leaving the popover")
	}
}

func TestSessionPreviewSurfaceBlocksInput(t *testing.T) {
	for _, tt := range []struct {
		name string
		at   f32.Point
	}{
		{"header", f32.Pt(350, 118)},
		{"padding", f32.Pt(296, 102)},
		{"more", f32.Pt(350, 532)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := model.State{Sessions: []model.Session{{ID: "s", Name: "session"}}}
			for i := range 15 {
				st.Workspaces = append(st.Workspaces, model.Workspace{ID: strconv.Itoa(i), SessionID: "s"})
			}
			s := Sidebar{SessionPreview: true, Hosts: []HostInfo{{}, {}}, Window: image.Pt(900, 800), height: 800}
			s.preview.hover.shown = "s"
			s.preview.anchor = image.Rect(0, 100, 288, 128)
			var r input.Router
			var ops op.Ops
			under := new(int)
			var received []pointer.Kind
			frame := func() {
				ops.Reset()
				gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Unix(1e9, 0),
					Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 800))}
				for {
					ev, ok := gtx.Event(pointer.Filter{Target: under, Kinds: pointer.Press | pointer.Scroll,
						ScrollX: pointer.ScrollRange{Min: -1e6, Max: 1e6}, ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}})
					if !ok {
						break
					}
					if e, ok := ev.(pointer.Event); ok {
						received = append(received, e.Kind)
					}
				}
				a := clip.Rect{Max: image.Pt(900, 800)}.Push(&ops)
				event.Op(&ops, under)
				a.Pop()
				s.drawSessionPreview(gtx, theme.Dark(), &st)
				r.Frame(&ops)
			}
			inputAt := func(at f32.Point) {
				r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: at},
					pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: at, Buttons: pointer.ButtonPrimary},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at},
					pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, 20)})
				frame()
				frame()
			}
			frame()
			inputAt(tt.at)
			if len(received) != 0 {
				t.Fatalf("popover input reached the pane: %v", received)
			}
			inputAt(f32.Pt(850, 700))
			if !slices.Contains(received, pointer.Press) || !slices.Contains(received, pointer.Scroll) {
				t.Fatalf("probe did not receive input outside the popover: %v", received)
			}
		})
	}
}
