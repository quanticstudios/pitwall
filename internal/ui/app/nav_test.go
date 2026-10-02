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
	var n nav
	n.sync(&st)
	check := func(step, ws, pane string) {
		t.Helper()
		if n.workspace != ws || n.focused() != pane {
			t.Fatalf("%s: at %s/%s, want %s/%s", step, n.workspace, n.focused(), ws, pane)
		}
	}
	check("start", "w1", "a")

	// Switcher hidden: Alt+J/K stay inside the ungrouped sessions (w1, w2,
	// w3) and wrap.
	alt := key.ModAlt
	for _, want := range []string{"w2", "w3", "w1"} {
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

	// Alt held: the switcher shows and J/K walk every session, ungrouped
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

	// Each session remembers its focused pane.
	n.key(&st, press("J", alt))
	n.key(&st, press("K", alt))
	check("remembered pane", "w1", "c")

	// Alt+digit jumps by switcher order; the empty session has no focus.
	n.key(&st, press("5", alt))
	check("Alt+5", "w5", "")
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
	if msg := n.key(&st, press("N", alt|key.ModShift)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", Target: "c", Dir: layout.Vertical}) {
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

// TestNavSync covers a closed focused pane and an archived session.
func TestNavSync(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{}
	n.sync(&st)
	n.selectWorkspace(&st, "w3", "f")
	b.Send(proto.ClosePane{Pane: "f"})
	st = b.State()
	n.sync(&st)
	if n.workspace != "w3" || n.focused() != "e" {
		t.Fatalf("after close: %s/%s", n.workspace, n.focused())
	}
	b.Send(proto.ArchiveWorkspace{WorkspaceID: "w3", Archived: true})
	st = b.State()
	n.sync(&st)
	if n.workspace != "w1" {
		t.Fatalf("after archive: %s", n.workspace)
	}
}

func TestKeyFiltersSwallowAlt(t *testing.T) {
	var n nav
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
// selection, and a new session taking the selection and pane focus.
func TestNavGroups(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	var n nav
	n.sync(&st)
	alt := key.ModAlt
	n.key(&st, press("4", alt))
	for _, want := range []string{"w5", "w4"} {
		n.key(&st, press("J", alt))
		if n.workspace != want {
			t.Fatalf("group J: %s, want %s", n.workspace, want)
		}
	}

	// Ungrouping g1 puts w4 and w5 after the other ungrouped sessions.
	b.Send(proto.DeleteGroup{GroupID: "g1"})
	st = b.State()
	n.sync(&st)
	var got []string
	for _, w := range ordered(&st) {
		got = append(got, w.ID)
	}
	if want := []string{"w1", "w2", "w3", "w4", "w5", "w6"}; !reflect.DeepEqual(got, want) {
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

	// Alt+Shift+T opens a session in the open one's folder and selects it.
	msg := n.key(&st, press("T", alt|key.ModShift))
	if msg != (proto.NewSession{Cwd: fakeHome + "/Work/pitwall", FromPane: "a"}) {
		t.Fatalf("Alt+Shift+T: %#v", msg)
	}
	b.Send(msg)
	st = b.State()
	n.sync(&st)
	if w := findWorkspace(&st, n.workspace); w == nil || w.Name != "pitwall" || n.focused() == "" || w.ProjectID != "" {
		t.Fatalf("new session not selected and focused: %s/%s", n.workspace, n.focused())
	}
}

// TestStartup: the daemon's first session shows up after the window opens
// and gets selected with its pane focused.
func TestStartup(t *testing.T) {
	var n nav
	st := model.State{}
	n.sync(&st)
	st.Workspaces = []model.Workspace{{ID: "s1", Path: "/home/me", Layout: layout.Leaf("p1")}}
	n.sync(&st)
	if n.workspace != "s1" || n.focused() != "p1" {
		t.Fatalf("at %s/%s", n.workspace, n.focused())
	}
}
