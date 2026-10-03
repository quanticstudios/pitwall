package app

import (
	"reflect"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func press(n key.Name, m key.Modifiers) key.Event {
	return key.Event{Name: n, Modifiers: m, State: key.Press}
}

func TestNav(t *testing.T) {
	st := NewFakeBackend().State()
	n := nav{keys: aide}
	n.sync(&st)
	check := func(step, ws, pane string) {
		t.Helper()
		if n.workspace != ws || n.focused() != pane {
			t.Fatalf("%s: at %s/%s, want %s/%s", step, n.workspace, n.focused(), ws, pane)
		}
	}
	check("start", "w1", "a")

	// Switcher hidden: Alt+J/K stay inside the ungrouped tabs (w1, w1b,
	// w1c, w2, w3) and wrap.
	alt := key.ModAlt
	for _, want := range []string{"w1b", "w1c", "w2", "w3", "w1"} {
		n.key(&st, press("J", alt))
		if n.workspace != want {
			t.Fatalf("local J: %s, want %s", n.workspace, want)
		}
	}
	n.key(&st, press("K", alt))
	check("local K wraps", "w3", "e")
	n.key(&st, press(key.NameUpArrow, alt))
	check("up mirrors K", "w2", "d")
	n.key(&st, press(key.NameDownArrow, alt))
	check("down mirrors J", "w3", "e")

	// Alt held: the switcher shows and J/K walk every tab, ungrouped
	// first, then group g1 (w4, w5) and g2 (w6).
	n.key(&st, key.Event{Name: key.NameAlt, State: key.Press})
	if !n.switcherVisible() {
		t.Fatal("Alt press did not show the switcher")
	}
	n.key(&st, press("J", alt))
	check("all J crosses into the groups", "w4", "g")
	n.key(&st, press("J", alt))
	n.key(&st, press("J", alt))
	n.key(&st, press("J", alt))
	check("all J wraps", "w1", "a")
	n.key(&st, key.Event{Name: key.NameAlt, Modifiers: alt, State: key.Release})
	if n.switcherVisible() {
		t.Fatal("Alt release did not hide the switcher")
	}

	// Alt with another modifier is not a hold.
	n.key(&st, key.Event{Name: key.NameAlt, Modifiers: key.ModShift, State: key.Press})
	if n.switcherVisible() {
		t.Fatal("Shift+Alt showed the switcher")
	}

	// Panes cycle in reading order (a | b over c) and wrap; arrows mirror H/L.
	for _, want := range []string{"b", "c", "a"} {
		n.key(&st, press("L", alt))
		check("L", "w1", want)
	}
	n.key(&st, press("H", alt))
	check("H wraps", "w1", "c")
	n.key(&st, press(key.NameRightArrow, alt))
	check("right mirrors L", "w1", "a")
	n.key(&st, press(key.NameLeftArrow, alt))
	check("left mirrors H", "w1", "c")

	// Each tab remembers its focused pane.
	n.key(&st, press("J", alt))
	n.key(&st, press("K", alt))
	check("remembered pane", "w1", "c")

	// Alt+digit jumps by switcher order; the empty tab has no focus.
	n.key(&st, press("7", alt))
	check("Alt+7", "w5", "")
	if msg := n.key(&st, press("N", alt)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w5"}) {
		t.Fatalf("Alt+N on empty: %#v", msg)
	}
	b := NewFakeBackend()
	b.Send(n.key(&st, press("N", alt)))
	st = b.State()
	n.sync(&st)
	check("new pane takes focus", "w5", "n1")
	st = NewFakeBackend().State()
	n.key(&st, press("1", alt))
	if msg := n.key(&st, press("N", alt|key.ModShift)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", TabID: "t1", Target: "c", Dir: layout.Vertical}) {
		t.Fatalf("Alt+Shift+N: %#v", msg)
	}
	if msg := n.key(&st, press("W", alt|key.ModShift)); msg != (proto.ClosePane{Pane: "c"}) {
		t.Fatalf("Alt+Shift+W: %#v", msg)
	}

	// Alt+Space pins the switcher past the release; Esc closes it.
	n.key(&st, key.Event{Name: key.NameAlt, State: key.Press})
	n.key(&st, press(key.NameSpace, alt))
	n.key(&st, key.Event{Name: key.NameAlt, State: key.Release})
	if !n.switcherVisible() {
		t.Fatal("pinned switcher hid on release")
	}
	n.key(&st, press(key.NameEscape, 0))
	if n.switcherVisible() {
		t.Fatal("Esc did not close the switcher")
	}
}

// TestNavSync covers a closed focused pane and a detached session.
func TestNavSync(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	n.selectWorkspace(&st, "w3", "f")
	b.Send(proto.ClosePane{Pane: "f"})
	st = b.State()
	n.sync(&st)
	if n.workspace != "w3" || n.focused() != "e" {
		t.Fatalf("after close: %s/%s", n.workspace, n.focused())
	}
	b.Send(proto.DetachSession{WorkspaceID: "w3", Detached: true})
	st = b.State()
	n.sync(&st)
	if n.workspace != "w4" {
		t.Fatalf("after detach: %s, want the next tab in sidebar order", n.workspace)
	}
}

func TestKeyFiltersSwallowAlt(t *testing.T) {
	n := nav{keys: aide}
	has := func(name key.Name) bool {
		for _, f := range n.keyFilters() {
			if f.Name == name {
				return true
			}
		}
		return false
	}
	if !has(key.NameAlt) || has(key.NameEscape) {
		t.Fatal("hidden switcher: want Alt filtered, Esc left to the pane")
	}
	n.altHeld = true
	if !has(key.NameEscape) {
		t.Fatal("visible switcher should take Esc")
	}
}

func TestDragRatios(t *testing.T) {
	n := &layout.Node{Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}
	dragRatios(n, 100, 1, 0, 30)
	if n.Ratios[0] != 0.3 || n.Ratios[1] != 0.7 {
		t.Fatalf("got %v", n.Ratios)
	}
	dragRatios(n, 100, 1, 0, -50)
	if n.Ratios[0] != minRatio {
		t.Fatalf("clamp: %v", n.Ratios)
	}
	rects := rectsOf(&layout.Node{Ratios: []float64{0.3, 0.7}, Children: n.Children}, layout.Rect{W: 101, H: 10}, 1)
	if rects["a"] != (layout.Rect{W: 30, H: 10}) || rects["b"] != (layout.Rect{X: 31, W: 70, H: 10}) {
		t.Fatalf("rects %v", rects)
	}
}

// TestNavGroups covers cycling inside a group, a group dissolving under the
// selection, and a new tab opening below the open one with focus.
func TestNavGroups(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	alt := key.ModAlt
	n.key(&st, press("6", alt))
	for _, want := range []string{"w5", "w4"} {
		n.key(&st, press("J", alt))
		if n.workspace != want {
			t.Fatalf("group J: %s, want %s", n.workspace, want)
		}
	}

	// Ungrouping g1 puts w4 and w5 at its place: after the other
	// ungrouped tabs, before g2.
	b.Send(proto.DeleteGroup{GroupID: "g1"})
	st = b.State()
	n.sync(&st)
	var got []string
	for _, w := range orderedIn(&st, "s1") {
		got = append(got, w.ID)
	}
	if want := []string{"w1", "w1b", "w1c", "w2", "w3", "w4", "w5", "w6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	n.key(&st, press("J", alt))
	if n.workspace != "w5" {
		t.Fatalf("ungrouped J from w4: %s", n.workspace)
	}
	n.key(&st, press("J", alt))
	if n.workspace != "w1" {
		t.Fatalf("ungrouped J wraps to w1: %s", n.workspace)
	}

	// Alt+Shift+T opens a tab right below the open one, where its focused
	// pane's shell is, and selects it.
	msg := n.key(&st, press("T", alt|key.ModShift))
	if msg != (proto.NewTab{WorkspaceID: "w1", FromPane: "a"}) {
		t.Fatalf("Alt+Shift+T: %#v", msg)
	}
	b.Send(msg)
	st = b.State()
	n.sync(&st)
	w := findWorkspace(&st, n.workspace)
	if w == nil || orderedIn(&st, "s1")[1].ID != w.ID || w.Path != fakeHome+"/Work/pitwall" || n.focused() == "" || w.ProjectID != "" {
		t.Fatalf("new tab not below w1, selected and focused: %s/%s", n.workspace, n.focused())
	}
}

// TestStartup: the daemon's first session shows up after the window opens
// and gets selected with its pane focused.
func TestStartup(t *testing.T) {
	n := nav{keys: aide}
	st := model.State{}
	n.sync(&st)
	st.Sessions = []model.Session{{ID: "x", Name: "swift-otter"}}
	st.Workspaces = []model.Workspace{{ID: "s1", SessionID: "x", Path: "/home/me", Tabs: []model.Tab{{ID: "t", Layout: layout.Leaf("p1")}}}}
	n.sync(&st)
	if n.session != "x" || n.workspace != "s1" || n.focused() != "p1" {
		t.Fatalf("at %s %s/%s", n.session, n.workspace, n.focused())
	}
}

// TestWindowFollowsSessions: a window whose session ends or loses its last
// visible tab moves to the most recently used session with one, and closes
// when none is left.
func TestWindowFollowsSessions(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	if n.session != "s1" || n.closed {
		t.Fatalf("starts on %q, closed %v", n.session, n.closed)
	}
	b.mu.Lock()
	for i := range b.st.Workspaces {
		if w := &b.st.Workspaces[i]; w.SessionID == "s1" {
			w.Detached = true
		}
	}
	b.mu.Unlock()
	st = b.State()
	n.sync(&st)
	if n.session != "s2" || n.workspace != "w11" || n.closed {
		t.Fatalf("after detaching every tab: %s/%s, closed %v", n.session, n.workspace, n.closed)
	}
	b.Send(proto.SessionKill{SessionID: "s2"})
	st = b.State()
	n.sync(&st)
	if n.session != "s3" || n.closed {
		t.Fatalf("after its session was killed: %s, closed %v", n.session, n.closed)
	}
	b.Send(proto.SessionKill{SessionID: "s3"})
	st = b.State()
	n.sync(&st)
	if !n.closed {
		t.Fatalf("stays open on %s with only detached tabs left elsewhere", n.session)
	}
	// Switching to a session whose tabs are all detached shows it.
	m := nav{keys: aide}
	m.sync(&st)
	if m.session != "s1" || m.closed {
		t.Fatalf("new window: %s, closed %v", m.session, m.closed)
	}
}

// TestNavFollowsTopOrder: with a group moved above the ungrouped tabs,
// cycling and Alt+digit follow the sidebar: g2's tab first.
func TestNavFollowsTopOrder(t *testing.T) {
	b := NewFakeBackend()
	b.Send(proto.MoveGroup{GroupID: "g2", Before: "w1"})
	b.Send(proto.MoveSession{WorkspaceID: "w2", Before: "g1"})
	st := b.State()
	var got []string
	for _, w := range orderedIn(&st, "s1") {
		got = append(got, w.ID)
	}
	if want := []string{"w6", "w1", "w1b", "w1c", "w3", "w2", "w4", "w5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	n := nav{keys: aide}
	n.sync(&st)
	n.key(&st, press("1", key.ModAlt))
	if n.workspace != "w6" {
		t.Fatalf("Alt+1: %s", n.workspace)
	}
	n.workspace, n.pinned = "w1", true // with the switcher open it crosses groups
	n.tabOp(&st, "prev")
	if n.workspace != "w6" {
		t.Fatalf("prev from w1: %s", n.workspace)
	}
}
