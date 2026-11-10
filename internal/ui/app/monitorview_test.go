package app

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestMonitorView: toggle_monitor shows the shown tab's monitor in place
// of the panes and asks its daemon for samples; pressed again, or Esc,
// stops the samples on that frame and brings the panes back as they were:
// the same tab, focused pane, layout and pane views, and nothing sent that
// would resize, scroll or close a pane. Keys reach the pane again.
func TestMonitorView(t *testing.T) {
	for _, leave := range []key.Event{press("M", key.ModCtrl|key.ModAlt), press(key.NameEscape, 0)} {
		t.Run(string(leave.Name), func(t *testing.T) {
			u, keys := keyWindow(t, conventional)
			b := u.b.(*FakeBackend)
			st := u.b.State()
			ws, pane := u.nav.workspace, u.nav.focused()
			layout := shownTab(findWorkspace(&st, ws)).Layout
			view := u.panes[pane]
			if ws == "" || pane == "" || view == nil {
				t.Fatalf("no pane shown: tab %q pane %q", ws, pane)
			}
			sent := len(b.Sent())

			keys(press("M", key.ModCtrl|key.ModAlt))
			if !u.mon.on || u.mon.tabs[machineKey("")] == nil {
				t.Fatalf("Ctrl+Alt+M: on %v, views %v; want the monitor drawn", u.mon.on, u.mon.tabs)
			}
			w := watches(b)
			if len(w) != 1 || w[0].WorkspaceID != ws || w[0].Interval <= 0 {
				t.Fatalf("shown: watches %v, want one for %s", w, ws)
			}

			keys(leave)
			if u.mon.on {
				t.Fatalf("%s left the monitor shown", leave.Name)
			}
			if w := watches(b); len(w) != 2 || w[1] != (proto.MonitorWatch{WorkspaceID: ws}) {
				t.Fatalf("hidden: watches %v, want the samples stopped", w)
			}
			st = u.b.State()
			if u.nav.workspace != ws || u.nav.focused() != pane || !reflect.DeepEqual(shownTab(findWorkspace(&st, ws)).Layout, layout) || u.panes[pane] != view {
				t.Fatalf("back on tab %q pane %q; want tab %q pane %q with the same layout and view", u.nav.workspace, u.nav.focused(), ws, pane)
			}
			for _, m := range b.Sent()[sent:] {
				switch m.(type) {
				case proto.Resize, proto.Scroll, proto.ClosePane, proto.SetLayout, proto.Input:
					t.Fatalf("sent %T %+v while the monitor showed", m, m)
				}
			}
			if in := keys(press("T", key.ModCtrl)); !strings.HasSuffix(in, "\x14") { // Ctrl+T goes to the shell
				t.Fatalf("a key after the monitor sent %q to the pane", in)
			}
		})
	}
}

// TestMonitorBoardSwap: toggle_board over the monitor shows the board and
// stops the samples; toggle_monitor over the board shows the monitor.
func TestMonitorBoardSwap(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	b := u.b.(*FakeBackend)
	keys(press("M", key.ModCtrl|key.ModAlt))
	keys(press("M", key.ModCtrl|key.ModShift))
	if !u.boardOn || u.mon.on {
		t.Fatalf("Ctrl+Shift+M over the monitor: board %v, monitor %v", u.boardOn, u.mon.on)
	}
	if w := watches(b); len(w) != 2 || w[1].Interval != 0 {
		t.Fatalf("board over the monitor: watches %v, want the samples stopped", w)
	}
	keys(press("M", key.ModCtrl|key.ModAlt))
	if u.boardOn || !u.mon.on {
		t.Fatalf("Ctrl+Alt+M over the board: board %v, monitor %v", u.boardOn, u.mon.on)
	}
	if w := watches(b); len(w) != 3 || w[2].Interval <= 0 {
		t.Fatalf("monitor over the board: watches %v, want samples again", w)
	}
}

// TestMonitorViewClosesOnTabSwitch: showing another tab closes the monitor,
// as it closes the board, and stops its samples.
func TestMonitorViewClosesOnTabSwitch(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	keys(press("M", key.ModCtrl|key.ModAlt))
	keys(press(key.NameTab, key.ModCtrl)) // next_tab
	if u.mon.on {
		t.Fatal("next_tab left the monitor shown")
	}
	if w := watches(u.b.(*FakeBackend)); len(w) != 2 || w[1].Interval != 0 {
		t.Fatalf("watches %v, want the samples stopped", w)
	}
}

// TestMonitorViewPerMachine: the monitor keeps one view per machine, so a
// peek from another tab of the machine shows the history it has. The
// watch moves to the tab it shows over, and the view outlives the tab it
// was first opened over.
func TestMonitorViewPerMachine(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	b := u.b.(*FakeBackend)
	first := u.nav.workspace
	keys(press("M", key.ModCtrl|key.ModAlt))
	v := u.mon.tabs[machineKey("")]
	if v == nil || v.pushed.IsZero() {
		t.Fatalf("views %v; want this machine's view with a sample", u.mon.tabs)
	}
	keys(press("M", key.ModCtrl|key.ModAlt), press(key.NameTab, key.ModCtrl)) // hide, next_tab
	second := u.nav.workspace
	if second == first {
		t.Fatal("next_tab stayed on the tab")
	}
	keys(press("M", key.ModCtrl|key.ModAlt))
	if u.mon.tabs[machineKey("")] != v {
		t.Fatal("a peek from another tab of the machine made a new view")
	}
	want := []proto.MonitorWatch{{WorkspaceID: first, Interval: 2 * time.Second}, {WorkspaceID: first}, {WorkspaceID: second, Interval: 2 * time.Second}}
	if got := watches(b); !slices.Equal(got, want) {
		t.Fatalf("watches %v, want %v", got, want)
	}

	keys(press("M", key.ModCtrl|key.ModAlt))
	b.mu.Lock()
	b.st.Workspaces = slices.DeleteFunc(b.st.Workspaces, func(w model.Workspace) bool { return w.ID == second })
	b.mu.Unlock()
	keys(press(key.NameCtrl, 0))
	if u.mon.tabs[machineKey("")] != v {
		t.Fatal("closing the tab it was shown over dropped the machine's view")
	}
}

// TestMonitorViewHidesPaneInput: aide's prefix keys pressed twice send
// their control byte to the pane, but not to a pane the monitor hides.
func TestMonitorViewHidesPaneInput(t *testing.T) {
	u, keys := keyWindow(t, aide)
	before := keys(press("M", key.ModCtrl|key.ModAlt))
	if !u.mon.on {
		t.Fatal("Ctrl+Alt+M left the monitor hidden")
	}
	if in := keys(press("T", key.ModCtrl), press("T", key.ModCtrl), press("P", key.ModCtrl), press("P", key.ModCtrl)); in != before {
		t.Fatalf("Ctrl+T Ctrl+T, Ctrl+P Ctrl+P under the monitor sent %q to the pane", strings.TrimPrefix(in, before))
	}
}

// TestMonitorViewDialogEsc: Esc closes a dialog over the monitor and
// leaves the monitor shown.
func TestMonitorViewDialogEsc(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	keys(press("M", key.ModCtrl|key.ModAlt))
	u.modal.open(modalAddProject, "")
	keys(press(key.NameCtrl, 0)) // a frame with the dialog drawn
	keys(press(key.NameEscape, 0))
	if u.modal.kind != modalNone || !u.mon.on {
		t.Fatalf("after Esc: dialog %v, monitor %v; want the dialog closed and the monitor shown", u.modal.kind, u.mon.on)
	}
}
