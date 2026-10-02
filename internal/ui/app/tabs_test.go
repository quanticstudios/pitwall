package app

import (
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTabMode walks every tab-mode key against the fake: each runs once and
// leaves the mode.
func TestTabMode(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	var n nav
	n.sync(&st)
	ctrlT := press(tabPrefix, key.ModCtrl)
	do := func(e key.Event) any {
		t.Helper()
		if n.key(&st, ctrlT) != nil || !n.tabMode {
			t.Fatal("Ctrl+T did not enter tab mode quietly")
		}
		msg := n.key(&st, e)
		if n.tabMode {
			t.Fatalf("%s left tab mode on", e.Name)
		}
		n.key(&st, key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
		if msg != nil {
			if err := b.Send(msg); err != nil {
				t.Fatal(err)
			}
		}
		st = b.State()
		n.sync(&st)
		return msg
	}
	at := func(step, tab, pane string) {
		t.Helper()
		if n.tab != tab || n.focused() != pane {
			t.Fatalf("%s: at %s/%s, want %s/%s", step, n.tab, n.focused(), tab, pane)
		}
	}
	at("start", "t1", "a")
	if msg := do(press("L", 0)); msg != (proto.SelectTab{WorkspaceID: "w1", TabID: "t2"}) {
		t.Fatalf("l: %#v", msg)
	}
	at("l", "t2", "i")
	do(press(key.NameRightArrow, 0))
	at("right", "t3", "j")
	do(press("L", 0))
	at("l wraps", "t1", "a")
	do(press("H", 0))
	at("h wraps", "t3", "j")
	do(press(key.NameLeftArrow, 0))
	at("left", "t2", "i")
	do(press("1", 0))
	at("1", "t1", "a")
	if msg := do(press("9", 0)); msg != nil {
		t.Fatalf("9 with three tabs: %#v", msg)
	}
	do(press("R", 0))
	if n.renameTab != "t1" {
		t.Fatalf("r asked to rename %q", n.renameTab)
	}
	if msg := do(press("N", 0)); msg != (proto.NewTab{WorkspaceID: "w1", FromPane: "a"}) {
		t.Fatalf("n: %#v", msg)
	}
	if len(st.Workspaces[0].Tabs) != 4 || n.tab != st.Workspaces[0].Tabs[3].ID || n.focused() == "" {
		t.Fatalf("new tab not shown and focused: %s/%s", n.tab, n.focused())
	}
	added := n.tab
	if msg := do(press("X", 0)); msg != (proto.CloseTab{WorkspaceID: "w1", TabID: added}) {
		t.Fatalf("x: %#v", msg)
	}
	at("x lands on the neighbour tab", "t3", "j")
	for _, e := range []key.Event{press(key.NameEscape, 0), press("Q", 0), press("C", key.ModCtrl)} {
		if msg := do(e); msg != nil {
			t.Fatalf("%v: %#v", e, msg)
		}
	}

	// Ctrl+T twice sends one literal Ctrl+T to the focused pane.
	n.key(&st, ctrlT)
	if msg := n.key(&st, ctrlT); !reflect.DeepEqual(msg, proto.Input{Pane: "j", Data: []byte{0x14}}) || n.tabMode {
		t.Fatalf("Ctrl+T Ctrl+T: %#v", msg)
	}

	// An Alt chord leaves tab mode and does what it always does.
	n.key(&st, ctrlT)
	n.key(&st, press("J", key.ModAlt))
	if n.tabMode || n.workspace != "w2" {
		t.Fatalf("Alt+J in tab mode: mode %v at %s", n.tabMode, n.workspace)
	}
}

// TestTabModeNoLeak drives the real window through Gio's router: nothing
// typed in tab mode reaches the pane, and Ctrl+T reaches it only doubled.
func TestTabModeNoLeak(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	typed := func() string {
		var s string
		for _, m := range b.Sent() {
			if in, ok := m.(proto.Input); ok {
				s += string(in.Data)
			}
		}
		return s
	}
	keys := func(es ...key.Event) {
		for _, e := range es {
			r.Queue(e)
			if e.State == key.Press && len(e.Name) == 1 && e.Modifiers&^key.ModShift == 0 {
				r.Queue(key.EditEvent{Text: string(e.Name[0] | 0x20)})
			}
			frame()
		}
	}
	up := func(n key.Name, m key.Modifiers) key.Event {
		return key.Event{Name: n, Modifiers: m, State: key.Release}
	}
	frame()
	frame()
	keys(press("A", 0), up("A", 0))
	if got := typed(); got != "a" {
		t.Fatalf("pane did not get plain typing: %q", got)
	}
	keys(press(tabPrefix, key.ModCtrl), up(tabPrefix, key.ModCtrl), press("Q", 0), up("Q", 0))
	keys(press(tabPrefix, key.ModCtrl), press(key.NameEscape, 0), up(key.NameEscape, 0))
	keys(press(tabPrefix, key.ModCtrl), press("L", 0), up("L", 0))
	if got := typed(); got != "a" {
		t.Fatalf("tab mode leaked %q to the pane", got[1:])
	}
	if !slices.Contains(b.Sent(), any(proto.SelectTab{WorkspaceID: "w1", TabID: "t2"})) {
		t.Fatal("Ctrl+T l did not select the next tab")
	}
	frame()
	keys(press(tabPrefix, key.ModCtrl), press(tabPrefix, key.ModCtrl), up(tabPrefix, key.ModCtrl))
	if got := typed(); got != "a\x14" {
		t.Fatalf("double Ctrl+T sent %q", got)
	}
	frame()
	keys(press("B", 0))
	if got := typed(); got != "a\x14b" {
		t.Fatalf("pane did not get typing back after tab mode: %q", got)
	}
}

// TestFocusAfterExit: a pane that exits hands focus to its neighbour, an
// emptied tab to the session's active tab, an emptied session to the next
// session in sidebar order.
func TestFocusAfterExit(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	var n nav
	n.sync(&st)
	step := func(msg any, ws, tab, pane string) {
		t.Helper()
		if err := b.Send(msg); err != nil {
			t.Fatal(err)
		}
		st = b.State()
		n.sync(&st)
		if n.workspace != ws || n.tab != tab || n.focused() != pane {
			t.Fatalf("after %#v: at %s/%s/%s, want %s/%s/%s", msg, n.workspace, n.tab, n.focused(), ws, tab, pane)
		}
	}
	n.setFocus("b")
	step(proto.ClosePane{Pane: "b"}, "w1", "t1", "c") // the next pane in reading order
	step(proto.ClosePane{Pane: "c"}, "w1", "t1", "a") // none after it: the one before
	n.key(&st, press(tabPrefix, key.ModCtrl))
	step(n.key(&st, press("2", 0)), "w1", "t2", "i")
	step(proto.ClosePane{Pane: "i"}, "w1", "t3", "j") // the tab goes; the daemon's next active tab
	step(proto.CloseTab{WorkspaceID: "w1", TabID: "t3"}, "w1", "t1", "a")
	step(proto.ClosePane{Pane: "a"}, "w2", "t5", "d") // the session goes; the next one
	n.selectWorkspace(&st, "w6", "")
	step(proto.ClosePane{Pane: "h"}, "w5", "", "") // the last session: the one before it (the fake's empty one)
}

// TestDetached: detached sessions leave the sidebar order, Alt+J and Alt+1-9,
// and attaching from the list shows one before the state un-detaches it.
func TestDetached(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	var n nav
	n.sync(&st)
	ids := func() []string {
		var out []string
		for _, w := range ordered(&st) {
			out = append(out, w.ID)
		}
		return out
	}
	if got := ids(); slices.Contains(got, "w7") || slices.Contains(got, "w8") {
		t.Fatalf("detached sessions listed: %v", got)
	}
	n.key(&st, key.Event{Name: key.NameAlt, State: key.Press})
	for range 10 {
		n.key(&st, press("J", key.ModAlt))
		if n.workspace == "w7" || n.workspace == "w8" {
			t.Fatal("Alt+J reached a detached session")
		}
	}
	n.attachSession(&st, "w7", "")
	if n.workspace != "w7" || n.focused() != "k" || !slices.Contains(ids(), "w7") {
		t.Fatalf("attach: at %s/%s, listed %v", n.workspace, n.focused(), ids())
	}
	st = b.State() // still detached in the backend
	n.sync(&st)
	if n.workspace != "w7" || findWorkspace(&st, "w7").Detached {
		t.Fatal("the override did not hold until the state caught up")
	}
	b.Send(proto.DetachSession{WorkspaceID: "w7"})
	st = b.State()
	n.sync(&st)
	if n.attach["w7"] {
		t.Fatal("override kept after the state agreed")
	}
}

// TestFocuser: an attach request selects its session and tab at once,
// waits for a session the state does not have yet, and sends SelectTab.
func TestFocuser(t *testing.T) {
	b := NewFakeBackend()
	var _ Focuser = b
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	b.RequestFocus(proto.FocusSession{WorkspaceID: "later"})
	b.RequestFocus(proto.FocusSession{WorkspaceID: "w7", TabID: "t7"})
	for range 2 {
		u.queueFocus(<-b.Focus())
	}
	u.applyFocus(&st)
	if u.nav.workspace != "w7" || u.nav.tab != "t7" || u.nav.focused() != "k" {
		t.Fatalf("at %s/%s/%s", u.nav.workspace, u.nav.tab, u.nav.focused())
	}
	if findWorkspace(&st, "w7").Detached {
		t.Fatal("the session is still hidden")
	}
	if got := b.Sent(); !reflect.DeepEqual(got, []any{proto.SelectTab{WorkspaceID: "w7", TabID: "t7"}}) {
		t.Fatalf("sent %#v", got)
	}

	u.queueFocus(proto.FocusSession{WorkspaceID: "ns9"})
	u.applyFocus(&st)
	if u.nav.workspace != "w7" || u.focusReq == nil {
		t.Fatal("a request for an unknown session should wait")
	}
	b.Send(proto.NewSession{Cwd: "/tmp"}) // shows up as ns1
	u.focusReq.WorkspaceID = "ns1"
	st = b.State()
	u.nav.sync(&st)
	u.applyFocus(&st)
	if u.nav.workspace != "ns1" || u.focusReq != nil {
		t.Fatalf("waiting request not applied: at %s", u.nav.workspace)
	}
}

func TestTabLabel(t *testing.T) {
	for _, tc := range []struct {
		tab  model.Tab
		want string
	}{
		{model.Tab{Name: "logs", Title: "tail"}, "logs"},
		{model.Tab{Title: "claude"}, "claude"},
		{model.Tab{}, "tab 3"},
	} {
		if got := tabLabel(tc.tab, 2); got != tc.want {
			t.Errorf("tabLabel(%+v) = %q, want %q", tc.tab, got, tc.want)
		}
	}
}

// TestTabRenameInline: Ctrl+T r opens the editor on the tab's label; typing
// replaces it and Return sends RenameTab.
func TestTabRenameInline(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	frame()
	for _, e := range []key.Event{press(tabPrefix, key.ModCtrl), press("R", 0)} {
		r.Queue(e)
		frame()
	}
	frame()
	if u.tabs.renaming != "t1" || !r.Source().Focused(&u.tabs.editor) {
		t.Fatalf("rename not open and focused: %q", u.tabs.renaming)
	}
	r.Queue(key.EditEvent{Range: key.Range{Start: 0, End: 7}, Text: "server"})
	frame()
	r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	frame()
	frame()
	if !slices.Contains(b.Sent(), any(proto.RenameTab{WorkspaceID: "w1", TabID: "t1", Name: "server"})) {
		t.Fatalf("sent %#v, editor %q", b.Sent(), u.tabs.editor.Text())
	}
}
