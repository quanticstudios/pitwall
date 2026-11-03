package sidebar

import (
	"image"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
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
