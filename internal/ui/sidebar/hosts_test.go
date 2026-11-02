package sidebar

import (
	"image"
	"reflect"
	"slices"
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

// A window of two machines lists each one's sessions under its heading,
// and a click on the other machine's session asks to show it. The agents
// waiting anywhere follow, and a click on one selects its tab.
func TestHostList(t *testing.T) {
	st := model.State{
		Sessions: []model.Session{{ID: "ls", Name: "main"}, {ID: "rs", Name: "main"}},
		Workspaces: []model.Workspace{
			{ID: "lw", SessionID: "ls", Name: "here"},
			{ID: "rw", SessionID: "rs", Name: "there"},
			{ID: "hid", SessionID: "rs", Name: "hidden", Detached: true},
		},
		Activities: []model.Activity{
			{PaneID: "rp", WorkspaceID: "rw", State: model.StatePendingApproval},
			{PaneID: "lp", WorkspaceID: "lw", State: model.StateWorking},
			{PaneID: "hp", WorkspaceID: "hid", State: model.StateAwaitingInput},
		},
	}
	if got := Blocked(&st); len(got) != 1 || got[0].PaneID != "rp" {
		t.Fatalf("blocked %+v", got)
	}
	s := Sidebar{Hosts: []HostInfo{{Up: true, Sessions: []string{"ls"}}, {Name: "box", Note: "Reconnecting…", Sessions: []string{"rs"}}}}
	var r input.Router
	var ops op.Ops
	frame := func() []Event {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(288, 800)), Now: time.Now()}
		_, events := s.Layout(gtx, theme.Dark(), &st, "ls", "lw")
		r.Frame(&ops)
		return events
	}
	click := func(y float32) []Event {
		at := f32.Pt(100, y)
		r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
		return frame()
	}
	frame()
	// The header, a gap, then per host a 26dp heading and 28dp rows.
	if got := click(56 + 6 + 26 + 28 + 26 + 14); !reflect.DeepEqual(got, []Event{ShowSession{SessionID: "rs"}}) {
		t.Fatalf("click on box's session: %#v", got)
	}
	if got := click(56 + 6 + 2*(26+28) + 26 + 14); !reflect.DeepEqual(got, []Event{SelectWorkspace{WorkspaceID: "rw", PaneID: "rp"}}) {
		t.Fatalf("click on the waiting agent: %#v", got)
	}
	s.Hosts = s.Hosts[:1]
	frame()
	if got := click(56 + 6 + 26 + 14); reflect.DeepEqual(got, []Event{ShowSession{SessionID: "ls"}}) {
		t.Fatal("one machine still draws the host list")
	}
}

// The tree's host list names every waiting agent under Waiting on you. The
// inbox shows the current session's already, so its list keeps the rest
// under Waiting elsewhere and takes no room when nothing waits elsewhere.
func TestHostListWaitingPerView(t *testing.T) {
	st := model.State{
		Sessions: []model.Session{{ID: "ls", Name: "main"}, {ID: "l2", Name: "side"}, {ID: "rs", Name: "main"}},
		Workspaces: []model.Workspace{
			{ID: "lw", SessionID: "ls", Name: "here"},
			{ID: "l2w", SessionID: "l2", Name: "beside"},
			{ID: "rw", SessionID: "rs", Name: "there"},
		},
		Activities: []model.Activity{
			{PaneID: "lp", WorkspaceID: "lw", State: model.StatePendingApproval},
			{PaneID: "l2p", WorkspaceID: "l2w", State: model.StateAwaitingInput},
			{PaneID: "rp", WorkspaceID: "rw", State: model.StateError},
		},
	}
	sessionOf := map[string]string{"lw": "ls", "l2w": "l2", "rw": "rs"}
	s := Sidebar{session: "ls", Hosts: []HostInfo{{Up: true, Sessions: []string{"ls", "l2"}}, {Name: "box", Up: true, Sessions: []string{"rs"}}}}
	panes := func(acts []model.Activity) []string {
		var out []string
		for _, a := range acts {
			out = append(out, a.PaneID)
		}
		slices.Sort(out)
		return out
	}
	if got, head := s.waiting(&st, sessionOf); !slices.Equal(panes(got), []string{"l2p", "lp", "rp"}) || head != "Waiting on you" {
		t.Fatalf("tree: %v under %q", panes(got), head)
	}
	s.Inbox = true
	if got, head := s.waiting(&st, sessionOf); !slices.Equal(panes(got), []string{"l2p", "rp"}) || head != "Waiting elsewhere" {
		t.Fatalf("inbox: %v under %q", panes(got), head)
	}

	height := func(st model.State) int {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(288, 800)), Now: time.Now()}
		return s.hostList(gtx, theme.Dark(), &st).Size.Y
	}
	only := st
	only.Activities = st.Activities[:1] // only the current session's agent waits
	none := st
	none.Activities = nil
	if h, want := height(only), height(none); h != want {
		t.Fatalf("inbox, nothing waiting elsewhere: host list %ddp, want %ddp as with nothing waiting", h, want)
	}
	s.Inbox = false
	if h, want := height(only), height(none)+26+28; h != want {
		t.Fatalf("tree: host list %ddp, want %ddp with the current session's agent listed", h, want)
	}
}
