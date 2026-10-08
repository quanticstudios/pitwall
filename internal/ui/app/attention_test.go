package app

import (
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestJumpAttention(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tab := func(ws string, panes ...string) model.Workspace {
		root := &layout.Node{Pane: panes[0]}
		if len(panes) > 1 {
			root = &layout.Node{Dir: layout.Horizontal, Children: []*layout.Node{{Pane: panes[0]}, {Pane: panes[1]}}}
		}
		return model.Workspace{ID: ws, Tabs: []model.Tab{{ID: ws + "t", Layout: root}}, ActiveTab: ws + "t"}
	}
	act := func(ws, pane string, s model.AgentState, at int, unseen bool) model.Activity {
		return model.Activity{PaneID: pane, WorkspaceID: ws, State: s, UpdatedAt: t0.Add(time.Duration(at) * time.Second), Unseen: unseen}
	}
	st := model.State{Workspaces: []model.Workspace{tab("w1", "a", "b"), tab("w2", "c"), tab("w3", "d"), tab("w4", "e"), tab("w5", "f"), tab("w6", "g")}}
	st.Workspaces[2].Detached = true
	st.Activities = []model.Activity{
		act("w1", "a", model.StateCompleted, 5, true), // newest, but a finished turn ranks last
		act("w1", "b", model.StatePendingApproval, 3, true),
		act("w2", "c", model.StateAwaitingInput, 4, true),
		act("w3", "d", model.StateError, 6, true), // detached: never
		act("w4", "e", model.StateError, 1, false),
		act("w5", "f", model.StatePlanReady, 2, false),
		act("w6", "g", model.StateWorking, 7, false), // not needs-you
	}
	for _, tc := range []struct {
		name  string
		keys  *nav
		press key.Event
	}{
		{"conventional", &nav{}, press("U", key.ModCtrl|key.ModShift)},
		{"aide", &nav{keys: aide}, press("U", key.ModAlt)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := st
			s.Activities = slices.Clone(st.Activities)
			n := tc.keys
			n.sync(&s)
			n.selectWorkspace(&s, "w4", "e")
			for i, want := range []string{"w2/c", "w1/b", "w1/a", "w4/e", "w1/b", "w2/c", "w5/f"} {
				n.key(&s, tc.press)
				if got := n.workspace + "/" + n.focused(); got != want {
					t.Fatalf("press %d: at %s, want %s", i+1, got, want)
				}
				for j := range s.Activities { // what the window does for the focused pane
					if s.Activities[j].PaneID == n.focused() {
						s.Activities[j].Unseen = false
					}
				}
			}
		})
	}

	// Tab mode u does the same; with nothing that needs the user it stays.
	s := st
	n := nav{keys: aide}
	n.sync(&s)
	n.key(&s, press("T", key.ModCtrl))
	n.key(&s, press("U", 0))
	if n.workspace != "w2" || n.focused() != "c" {
		t.Fatalf("tab mode u: at %s/%s", n.workspace, n.focused())
	}
	s.Activities = s.Activities[len(s.Activities)-1:]
	n.key(&s, press("U", key.ModAlt))
	if n.workspace != "w2" {
		t.Fatalf("nothing needs you, yet moved to %s", n.workspace)
	}
}

func TestMarkSeen(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, panes: map[string]*paneUI{}}
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w1", "c") // c holds an unseen OSC notification
	unseen := func(st model.State) bool {
		i := slices.IndexFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == "c" })
		return i >= 0 && st.Activities[i].Unseen
	}
	sees := func() int {
		n := 0
		for _, m := range b.Sent() {
			if m == (proto.SeePane{Pane: "c"}) {
				n++
			}
		}
		return n
	}

	u.markSeen(&st)
	if !unseen(st) || sees() != 0 {
		t.Fatal("an unfocused window marked the pane seen")
	}
	u.winFocused = true
	before := b.State()
	u.markSeen(&st)
	if unseen(st) || sees() != 1 {
		t.Fatalf("focused: unseen %v, %d SeePane", unseen(st), sees())
	}
	if !unseen(before) {
		t.Fatal("markSeen wrote the backend's slice")
	}
	st = b.State()
	if slices.ContainsFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == "c" }) {
		t.Fatal("the seen notification did not clear")
	}
	u.markSeen(&st)
	if sees() != 1 {
		t.Fatal("SeePane sent twice")
	}

	// Focus changes go to the daemon too, for bells: "" when the window
	// shows no pane focused.
	focusSent := func() []string {
		var out []string
		for _, m := range b.Sent() {
			if s, ok := m.(proto.SeePane); ok {
				out = append(out, s.Pane)
			}
		}
		return out
	}
	u.winFocused = false
	u.markSeen(&st)
	u.markSeen(&st)
	u.winFocused = true
	u.markSeen(&st)
	if got := focusSent(); !slices.Equal(got, []string{"c", "", "c"}) {
		t.Fatalf("SeePane sent for %q", got)
	}

	for since, want := range map[time.Duration]float32{0: 2, ringPulse / 2: 4, ringPulse: 2, 2 * ringPulse: 2, time.Hour: 2} {
		if got := ringWidth(since); got < want-0.01 || got > want+0.01 {
			t.Errorf("ringWidth(%v) = %v, want %v", since, got, want)
		}
	}
}

func TestJumpAttentionByUrgency(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tab := func(ws string) model.Workspace {
		return model.Workspace{ID: ws, Tabs: []model.Tab{{ID: ws + "t", Layout: &layout.Node{Pane: ws + "p"}}}, ActiveTab: ws + "t"}
	}
	act := func(ws string, s model.AgentState, at int, urgency string) model.Activity {
		return model.Activity{PaneID: ws + "p", WorkspaceID: ws, State: s, UpdatedAt: t0.Add(time.Duration(at) * time.Second), Unseen: true, Urgency: urgency}
	}
	st := model.State{Workspaces: []model.Workspace{tab("w0"), tab("w1"), tab("w2"), tab("w3"), tab("w4")}}
	st.Activities = []model.Activity{
		act("w1", model.StateAwaitingInput, 9, "fyi"),     // newest, but fyi
		act("w2", model.StateCompleted, 2, "now"),         // a finished turn that needs review now
		act("w3", model.StatePendingApproval, 5, "later"), // later
		act("w4", model.StateAwaitingInput, 3, ""),        // untriaged sits between soon and later
	}
	n := &nav{}
	n.sync(&st)
	n.selectWorkspace(&st, "w0", "w0p")
	for i, want := range []string{"w2", "w4", "w3", "w1"} {
		n.jumpAttention(&st)
		if n.workspace != want {
			t.Fatalf("press %d: at %s, want %s", i+1, n.workspace, want)
		}
		for j := range st.Activities {
			if st.Activities[j].PaneID == n.focused() {
				st.Activities[j].Unseen = false
			}
		}
	}
}

// TestWriteClipboard puts each OSC 52 write on the clipboard once, and none
// while [terminal] osc52 is off.
func TestWriteClipboard(t *testing.T) {
	u := &ui{cfg: config.Settings{OSC52: true}}
	var r input.Router
	write := func(c model.Clipboard) string {
		var ops op.Ops
		u.writeClipboard(gl.Context{Ops: &ops, Source: r.Source()}, c)
		r.Frame(&ops)
		if _, b, ok := r.WriteClipboard(); ok {
			return string(b)
		}
		return ""
	}
	if got := write(model.Clipboard{Seq: 1, Text: "one"}); got != "one" {
		t.Fatalf("first write: %q", got)
	}
	if got := write(model.Clipboard{Seq: 1, Text: "one"}); got != "" {
		t.Fatalf("the same write again: %q", got)
	}
	if got := write(model.Clipboard{Seq: 2}); got != "" {
		t.Fatalf("a write whose text expired: %q", got)
	}
	u.cfg.OSC52 = false
	if got := write(model.Clipboard{Seq: 3, Text: "three"}); got != "" {
		t.Fatalf("osc52 = off wrote %q", got)
	}
}
