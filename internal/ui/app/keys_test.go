package app

import (
	"fmt"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// aide is the preset the nav, tab-mode and window tests were written for.
var aide = config.Preset("aide")

// tabPrefix is the aide preset's tab prefix key, held with Ctrl.
const tabPrefix key.Name = "T"

var conventional = config.Preset("conventional")

// TestConventional walks every conventional binding through nav.
func TestConventional(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: conventional}
	n.sync(&st)
	cs := key.ModCtrl | key.ModShift
	do := func(e key.Event) any {
		t.Helper()
		msg := n.key(&st, e)
		if msg != nil {
			if err := b.Send(msg); err != nil {
				t.Fatal(err)
			}
		}
		st = b.State()
		n.sync(&st)
		return msg
	}
	at := func(step, ws, tab, pane string) {
		t.Helper()
		if n.workspace != ws || n.tab != tab || n.focused() != pane {
			t.Fatalf("%s: at %s/%s/%s, want %s/%s/%s", step, n.workspace, n.tab, n.focused(), ws, tab, pane)
		}
	}
	at("start", "w1", "t1", "a")
	for _, tc := range []struct {
		e             key.Event
		ws, tab, pane string
	}{
		{press(key.NameTab, key.ModCtrl), "w1b", "t2", "i"},
		{press(key.NameTab, cs), "w1", "t1", "a"},
		{press(key.NamePageDown, key.ModCtrl), "w1b", "t2", "i"},
		{press(key.NamePageUp, key.ModCtrl), "w1", "t1", "a"},
		{press("3", key.ModAlt), "w1c", "t3", "j"},
		{press("5", key.ModAlt), "w3", "t6", "e"},
		{press(key.NameTab, key.ModCtrl), "w4", "t4", "g"}, // across groups
		{press(key.NameTab, cs), "w3", "t6", "e"},
		{press("1", key.ModAlt), "w1", "t1", "a"},
		{press(key.NameRightArrow, key.ModCtrl|key.ModAlt), "w1", "t1", "b"},
		{press(key.NameDownArrow, key.ModCtrl|key.ModAlt), "w1", "t1", "c"},
		{press(key.NameLeftArrow, key.ModCtrl|key.ModAlt), "w1", "t1", "b"},
		{press(key.NameUpArrow, key.ModCtrl|key.ModAlt), "w1", "t1", "a"},
		{press(key.NamePageDown, cs), "w4", "t4", "g"}, // the next group's first tab
		{press(key.NamePageDown, cs), "w6", "t9", "h"},
		{press(key.NamePageDown, cs), "w1", "t1", "a"}, // wraps to the ungrouped tabs
		{press(key.NamePageUp, cs), "w6", "t9", "h"},
		{press("1", key.ModAlt), "w1", "t1", "a"},
	} {
		do(tc.e)
		at(fmt.Sprint(tc.e.Modifiers, "+", tc.e.Name), tc.ws, tc.tab, tc.pane)
	}
	if msg := n.key(&st, press("O", cs)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", TabID: "t1", Target: "a", Dir: layout.Horizontal}) {
		t.Fatalf("Ctrl+Shift+O: %#v", msg)
	}
	if msg := n.key(&st, press("E", cs)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", TabID: "t1", Target: "a", Dir: layout.Vertical}) {
		t.Fatalf("Ctrl+Shift+E: %#v", msg)
	}
	if msg := n.key(&st, press("W", cs)); msg != (proto.ClosePane{Pane: "a"}) {
		t.Fatalf("Ctrl+Shift+W: %#v", msg)
	}
	if msg := do(press("T", cs)); msg != (proto.NewTab{WorkspaceID: "w1", FromPane: "a"}) || n.workspace != st.Workspaces[1].ID {
		t.Fatalf("Ctrl+Shift+T: %#v, at %s", msg, n.workspace)
	}

	// No hold modifier: Alt alone shows nothing, Ctrl+Shift+Space toggles
	// the switcher, which takes J/K/arrows until Enter or Escape.
	n.key(&st, key.Event{Name: key.NameAlt, State: key.Press})
	if n.switcherVisible() {
		t.Fatal("Alt showed the switcher")
	}
	n.key(&st, press(key.NameSpace, cs))
	if !n.switcherVisible() {
		t.Fatal("Ctrl+Shift+Space did not open the switcher")
	}
	n.key(&st, press("J", 0))
	n.key(&st, press(key.NameDownArrow, 0))
	n.key(&st, press(key.NameDownArrow, 0))
	if n.workspace != "w2" {
		t.Fatalf("J/Down in the switcher walk every tab: at %s, want w2", n.workspace)
	}
	n.key(&st, press("K", 0))
	n.key(&st, press(key.NameReturn, 0))
	if n.switcherVisible() || n.workspace != "w1c" {
		t.Fatalf("Enter: visible %v at %s", n.switcherVisible(), n.workspace)
	}
	n.key(&st, press(key.NameSpace, cs))
	n.key(&st, press(key.NameSpace, cs))
	if n.switcherVisible() {
		t.Fatal("Ctrl+Shift+Space did not close the switcher")
	}
	n.key(&st, press(key.NameSpace, cs))
	n.key(&st, press(key.NameEscape, 0))
	if n.switcherVisible() {
		t.Fatal("Escape did not close the switcher")
	}

	n.key(&st, press("B", cs))
	if !n.sidebarHidden {
		t.Fatal("Ctrl+Shift+B did not hide the sidebar")
	}
	n.key(&st, press("B", key.ModCtrl))
	if !n.sidebarHidden {
		t.Fatal("conventional took Ctrl+B")
	}
}

// keyWindow drives the real window through Gio's router and returns a
// function that sends keys and the text the panes received so far.
func keyWindow(t *testing.T, keys *config.Bindings) (*ui, func(...key.Event) string) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: keys}}
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
	return u, func(es ...key.Event) string {
		for _, e := range es {
			r.Queue(e)
			frame()
			r.Queue(key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
			frame()
		}
		var s string
		for _, m := range b.Sent() {
			if in, ok := m.(proto.Input); ok {
				s += string(in.Data)
			}
		}
		return s
	}
}

// TestConventionalNoLeak: no conventional binding reaches the pane, while
// Ctrl+T and Ctrl+B, which shells use, do.
func TestConventionalNoLeak(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	cs := key.ModCtrl | key.ModShift
	for _, e := range []key.Event{
		press("T", cs), press("W", cs), press(key.NameTab, key.ModCtrl), press(key.NameTab, cs),
		press(key.NamePageDown, key.ModCtrl), press(key.NamePageUp, key.ModCtrl), press("2", key.ModAlt),
		press("O", cs), press("E", cs), press(key.NameLeftArrow, key.ModCtrl|key.ModAlt),
		press(key.NamePageDown, cs), press(key.NamePageUp, cs), press(key.NameSpace, cs), press(key.NameSpace, cs),
		press("C", cs), press("V", cs), press(key.NamePageUp, key.ModShift), press("B", cs), press("B", cs),
	} {
		if got := keys(e); got != "" {
			t.Fatalf("%v+%s leaked %q to the pane", e.Modifiers, e.Name, got)
		}
	}
	if got := keys(press("T", key.ModCtrl), press("B", key.ModCtrl)); got != "\x14\x02" {
		t.Fatalf("Ctrl+T Ctrl+B sent %q, want them in the pane", got)
	}
	if u.nav.tabMode {
		t.Fatal("Ctrl+T entered tab mode in the conventional preset")
	}
}

// TestToggleSidebarAide: Ctrl+B hides and shows the sidebar and never
// reaches the pane.
func TestToggleSidebarAide(t *testing.T) {
	u, keys := keyWindow(t, aide)
	if got := keys(press("B", key.ModCtrl)); got != "" || !u.nav.sidebarHidden {
		t.Fatalf("Ctrl+B: hidden %v, pane got %q", u.nav.sidebarHidden, got)
	}
	if got := keys(press("B", key.ModCtrl)); got != "" || u.nav.sidebarHidden {
		t.Fatalf("second Ctrl+B: hidden %v, pane got %q", u.nav.sidebarHidden, got)
	}
}

// TestSettingsKeys: Ctrl+, opens and closes the settings page in both
// presets, Escape closes it, and neither reaches the pane.
func TestSettingsKeys(t *testing.T) {
	for _, b := range []*config.Bindings{aide, conventional} {
		u, keys := keyWindow(t, b)
		u.cfg.Path = t.TempDir() + "/config.toml"
		if got := keys(press(",", key.ModCtrl)); got != "" || !u.settings.Shown() {
			t.Fatalf("%s: Ctrl+, shown %v, pane got %q", b.Preset, u.settings.Shown(), got)
		}
		if keys(press(",", key.ModCtrl)); u.settings.Shown() {
			t.Fatalf("%s: second Ctrl+, left settings open", b.Preset)
		}
		keys(press(",", key.ModCtrl))
		if got := keys(press(key.NameEscape, 0)); got != "" || u.settings.Shown() {
			t.Fatalf("%s: Escape: shown %v, pane got %q", b.Preset, u.settings.Shown(), got)
		}
	}
}
