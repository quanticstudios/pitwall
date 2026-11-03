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

func TestSessionActivity(t *testing.T) {
	work := model.Activity{State: model.StateWorking}
	done := model.Activity{State: model.StateCompleted, Unseen: true}
	seen := model.Activity{State: model.StateCompleted}
	pr := model.Activity{State: model.StatePROpen, Unseen: true}
	plan := model.Activity{State: model.StatePlanReady}
	input := model.Activity{State: model.StateAwaitingInput}
	approval := model.Activity{State: model.StatePendingApproval}
	err := model.Activity{State: model.StateError}
	for _, tt := range []struct {
		name  string
		acts  []model.Activity
		state model.AgentState
	}{
		{"working and plan", []model.Activity{work, plan}, model.StatePlanReady},
		{"working and unseen done", []model.Activity{work, done}, model.StateCompleted},
		{"working and seen done", []model.Activity{work, seen}, model.StateWorking},
		{"only seen done", []model.Activity{seen}, ""},
		{"working and unseen PR", []model.Activity{work, pr}, model.StatePROpen},
		{"only seen PR", []model.Activity{{State: model.StatePROpen}}, ""},
		{"input above plan and done", []model.Activity{plan, input, done}, model.StateAwaitingInput},
		{"approval above input", []model.Activity{input, approval}, model.StatePendingApproval},
		{"error above approval", []model.Activity{approval, err, work, plan, input, done, seen, pr}, model.StateError},
		{"PR above done", []model.Activity{done, pr}, model.StatePROpen},
		{"connecting", []model.Activity{{State: model.StateConnecting}}, model.StateConnecting},
		{"terminal and watching", []model.Activity{{State: model.StateTerminalRunning}, {State: model.StateWatching}}, ""},
		{"empty", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dot := sessionActivity(tt.acts)
			var state model.AgentState
			if dot != nil {
				state = dot.State
			}
			if state != tt.state {
				t.Fatalf("sessionActivity = %q, want %q", state, tt.state)
			}
		})
	}
}

func TestHostListCounts(t *testing.T) {
	for _, tt := range []struct {
		name, session, count string
		acts                 []model.Activity
	}{
		{"seen plan", "main", "23 tabs", []model.Activity{{State: model.StateWorking}, {State: model.StatePlanReady}}},
		{"waiting", "main", "23 tabs · 2 need you", []model.Activity{{State: model.StateError}, {State: model.StateAwaitingInput}}},
		{"mixed needs", "main", "23 tabs · 3 need you", []model.Activity{{State: model.StateError}, {State: model.StateAwaitingInput}, {State: model.StateCompleted, Unseen: true}}},
		{"unseen plan", "main", "23 tabs · 1 needs you", []model.Activity{{State: model.StatePlanReady, Unseen: true}}},
		{"done", "main", "23 tabs · 2 need you", []model.Activity{{State: model.StateCompleted, Unseen: true}, {State: model.StatePROpen, Unseen: true}}},
		{"seen", "main", "23 tabs", []model.Activity{{State: model.StateCompleted}}},
		{"long name", "do-not-truncate-this-name", "23 tabs", []model.Activity{{State: model.StateCompleted, Unseen: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := model.State{Sessions: []model.Session{{ID: "s", Name: tt.session}}}
			for i := range 23 {
				st.Workspaces = append(st.Workspaces, model.Workspace{ID: strconv.Itoa(i), SessionID: "s"})
			}
			for _, a := range tt.acts {
				a.WorkspaceID = "0"
				st.Activities = append(st.Activities, a)
			}
			st.Workspaces = append(st.Workspaces, model.Workspace{ID: "detached", SessionID: "s", Detached: true})
			st.Activities = append(st.Activities, model.Activity{WorkspaceID: "detached", State: model.StateCompleted, Unseen: true})
			s := Sidebar{Hosts: []HostInfo{{Up: true, Sessions: []string{"s"}}, {Name: "box", Up: true}}}
			var r input.Router
			var ops op.Ops
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(288, 800)), Now: time.Now()}
			s.hostList(gtx, theme.Dark(), &st)
			r.Frame(&ops)
			var labels []string
			var collect func([]input.SemanticNode)
			collect = func(nodes []input.SemanticNode) {
				for _, n := range nodes {
					labels = append(labels, n.Desc.Label)
					collect(n.Children)
				}
			}
			collect(r.AppendSemantics(nil))
			if !slices.Contains(labels, tt.count) {
				t.Fatalf("host list labels %q do not contain %q", labels, tt.count)
			}
		})
	}
}

func TestSessionWatching(t *testing.T) {
	st := model.State{
		Workspaces: []model.Workspace{{ID: "o", SessionID: "s"}, {ID: "w", SessionID: "s", Parent: "o"}},
		Activities: []model.Activity{
			{WorkspaceID: "o", State: model.StateCompleted, Unseen: true},
			{WorkspaceID: "w", Provider: model.ProviderCodex, State: model.StateWorking},
		},
	}
	live := sessionState(&st)
	if dot := sessionActivity(live.Activities); dot == nil || dot.State != model.StateWorking {
		t.Fatalf("watching session dot: %+v", dot)
	}
	tabs := sessionPreviewTabs(&st, "s")
	if tabs[0].ws.ID != "w" || tabs[1].activity.State != model.StateWatching || tabs[1].activity.Unseen {
		t.Fatalf("watching preview: %+v", tabs)
	}
	if sum := live.Summary("s"); sum.NeedsYou != 0 || sum.Working != 1 || sum.Unseen != 0 {
		t.Fatalf("watching counts: %+v", sum)
	}
	if st.Activities[0].State != model.StateCompleted || !st.Activities[0].Unseen {
		t.Fatal("session view changed source activity")
	}
}

func TestSessionDotColor(t *testing.T) {
	th := theme.Dark()
	if got := sessionDotColor(th, model.StatePROpen); got != th.Green {
		t.Fatalf("unseen PR dot = %v, want Done color %v", got, th.Green)
	}
	if got := sessionDotColor(th, model.StateWorking); got != th.Blue {
		t.Fatalf("working dot = %v, want %v", got, th.Blue)
	}
}

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
