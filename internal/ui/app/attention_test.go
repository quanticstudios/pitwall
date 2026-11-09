package app

import (
	"path/filepath"
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

// TestJumpAttentionOrder: unseen panes newest first, finished turns last, then
// the seen ones by state priority.
func TestJumpAttentionOrder(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tab := func(ws string) model.Workspace {
		return model.Workspace{ID: ws, Tabs: []model.Tab{{ID: ws + "t", Layout: &layout.Node{Pane: ws + "p"}}}, ActiveTab: ws + "t"}
	}
	act := func(ws string, s model.AgentState, at int) model.Activity {
		return model.Activity{PaneID: ws + "p", WorkspaceID: ws, State: s, UpdatedAt: t0.Add(time.Duration(at) * time.Second), Unseen: true}
	}
	st := model.State{Workspaces: []model.Workspace{tab("w0"), tab("w1"), tab("w2"), tab("w3"), tab("w4")}}
	st.Activities = []model.Activity{
		act("w1", model.StateCompleted, 9),       // newest, but finished
		act("w2", model.StateAwaitingInput, 2),   // oldest question
		act("w3", model.StatePendingApproval, 5), // newest that waits
		act("w4", model.StateError, 3),
	}
	n := &nav{}
	n.sync(&st)
	n.selectWorkspace(&st, "w0", "w0p")
	for i, want := range []string{"w3", "w4", "w2", "w1", "w4", "w3", "w2", "w1"} {
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

// clipHosts is a Backend whose clipboard writes all came from host.
type clipHosts struct {
	Backend
	host  string
	stale bool
}

func (c *clipHosts) ClipboardHost(uint64) (string, bool) { return c.host, !c.stale }

// TestWriteClipboardFromHost holds a write from another host's pane for
// the dialog, writes it on Allow, and follows a remembered answer per host
// without asking again.
func TestWriteClipboardFromHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	b := &clipHosts{}
	u := &ui{b: b, cfg: config.Settings{OSC52: true, Path: path}}
	var r input.Router
	frame := func(f func(gl.Context)) string {
		var ops op.Ops
		f(gl.Context{Ops: &ops, Source: r.Source()})
		r.Frame(&ops)
		if _, b, ok := r.WriteClipboard(); ok {
			return string(b)
		}
		return ""
	}
	seq := uint64(0)
	write := func(host, text string) string {
		seq++
		b.host = host
		return frame(func(gtx gl.Context) { u.writeClipboard(gtx, model.Clipboard{Seq: seq, Text: text}) })
	}
	answer := func(allow, remember bool) string {
		u.modal.remember = remember
		return frame(func(gtx gl.Context) { u.answerClipboard(gtx, allow) })
	}

	if got := write("", "local"); got != "local" || u.modal.kind != modalNone {
		t.Fatalf("local write: %q, dialog %v", got, u.modal.kind)
	}
	if got := write("box", "one"); got != "" || u.modal.kind != modalClipboard || u.modal.clip != (clipAsk{"box", "one"}) {
		t.Fatalf("box's first write: %q, dialog %v %+v", got, u.modal.kind, u.modal.clip)
	}
	// Allow answers for the text the dialog shows, not for a newer write
	// that came in before the click; that one gets a dialog of its own.
	if got := write("box", "two"); got != "" || u.modal.clip.text != "one" {
		t.Fatalf("box's next write while asking: %q, dialog %+v", got, u.modal.clip)
	}
	if got := answer(true, false); got != "one" || u.modal.kind != modalNone {
		t.Fatalf("Allow once: %q, dialog %v", got, u.modal.kind)
	}
	if got := frame(u.askClipboard); got != "" || u.modal.kind != modalClipboard || u.modal.clip.text != "two" {
		t.Fatalf("the waiting write after Allow once: %q, dialog %v %+v", got, u.modal.kind, u.modal.clip)
	}
	if got := answer(false, false); got != "" {
		t.Fatalf("Deny once: %q", got)
	}
	if got := write("box", "three"); got != "" || u.modal.kind != modalClipboard {
		t.Fatalf("box after Allow once: %q, dialog %v", got, u.modal.kind)
	}
	if got := answer(true, true); got != "three" {
		t.Fatalf("Allow and remember: %q", got)
	}
	if got := write("box", "four"); got != "four" || u.modal.kind != modalNone {
		t.Fatalf("box remembered allow: %q, dialog %v", got, u.modal.kind)
	}

	// gpu has its own answer.
	if got := write("gpu", "five"); got != "" || u.modal.kind != modalClipboard || u.modal.clip.host != "gpu" {
		t.Fatalf("gpu's first write: %q, dialog %v %+v", got, u.modal.kind, u.modal.clip)
	}
	if got := answer(false, true); got != "" || u.modal.kind != modalNone {
		t.Fatalf("Deny and remember: %q, dialog %v", got, u.modal.kind)
	}
	if got := write("gpu", "six"); got != "" || u.modal.kind != modalNone {
		t.Fatalf("gpu remembered deny: %q, dialog %v", got, u.modal.kind)
	}

	// A write waiting behind the dialog follows the answer remembered there.
	u.modal.open(modalClipboard, "")
	u.modal.clip = clipAsk{"web", "eight"}
	if got := write("web", "nine"); got != "" {
		t.Fatalf("web's write while asking: %q", got)
	}
	if got := answer(true, true); got != "eight" {
		t.Fatalf("Allow and remember for web: %q", got)
	}
	if got := frame(u.askClipboard); got != "nine" || u.modal.kind != modalNone {
		t.Fatalf("web's waiting write after remembered allow: %q, dialog %v", got, u.modal.kind)
	}

	// The answers are in config.toml for the next window.
	if s, probs := config.LoadFile(path); len(probs) != 0 || s.HostClipboard["box"] != "allow" || s.HostClipboard["gpu"] != "deny" || s.HostClipboard["web"] != "allow" {
		t.Fatalf("config.toml: %v %v", s.HostClipboard, probs)
	}

	// A write the backend no longer has a host for was replaced: skipped.
	b.stale = true
	if got := write("", "replaced"); got != "" || u.modal.kind != modalNone {
		t.Fatalf("replaced write: %q, dialog %v", got, u.modal.kind)
	}
	b.stale = false

	// pitwall --host dev: the whole state is dev's, and its backend names
	// no host.
	defer func(h string) { Host = h }(Host)
	Host = "dev"
	if got := write("", "seven"); got != "" || u.modal.kind != modalClipboard || u.modal.clip.host != "dev" {
		t.Fatalf("pitwall --host dev: %q, dialog %v %+v", got, u.modal.kind, u.modal.clip)
	}
}
