package app

import (
	"image"
	"reflect"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// withPopup is st with pane pop open as w1's popup.
func withPopup(st model.State, pop string) model.State {
	st.Panes = append(st.Panes, model.Pane{ID: pop, WorkspaceID: "w1", Cmd: []string{"/usr/bin/lazygit"}})
	for i := range st.Workspaces {
		if st.Workspaces[i].ID == "w1" {
			st.Workspaces[i].Popup = &model.Popup{Pane: pop}
		}
	}
	return st
}

// Keys go to a tab's popup while it is open, close_pane closes the popup
// alone, a split leaves the tab's splits alone, and focus comes back to
// the pane it was on once the popup closes.
func TestNavPopup(t *testing.T) {
	plain := NewFakeBackend().State()
	n := nav{keys: aide}
	n.sync(&plain)
	if n.focused() != "a" {
		t.Fatalf("focused %q", n.focused())
	}
	st := withPopup(plain, "pop")
	n.sync(&st)
	if n.focused() != "pop" || n.popup() != "pop" {
		t.Fatalf("with a popup: focused %q", n.focused())
	}
	if msg := n.key(&st, press("W", key.ModAlt|key.ModShift)); msg != (proto.ClosePane{Pane: "pop"}) {
		t.Fatalf("close_pane sent %#v", msg)
	}
	if msg := n.key(&st, press("N", key.ModAlt)); msg != nil {
		t.Fatalf("split_right under a popup sent %#v", msg)
	}
	if msg := n.paneOp(&st, "fullscreen"); msg != nil || n.zoom != "" {
		t.Fatalf("fullscreen under a popup: %#v, zoom %q", msg, n.zoom)
	}
	// Another tab has no popup.
	n.key(&st, press("J", key.ModAlt))
	if n.workspace == "w1" || n.focused() == "pop" {
		t.Fatalf("next tab: %s/%s", n.workspace, n.focused())
	}
	n.key(&st, press("K", key.ModAlt))
	if n.focused() != "pop" {
		t.Fatalf("back on the popup's tab: %q", n.focused())
	}
	n.sync(&plain)
	if n.focused() != "a" {
		t.Fatalf("after the popup closed: %q", n.focused())
	}
}

func TestPopupRect(t *testing.T) {
	area := layout.Rect{X: 10, Y: 20, W: 1000, H: 600}
	cell := image.Pt(10, 20)
	for _, c := range []struct {
		w, h string
		want layout.Rect
	}{
		{"", "", layout.Rect{X: 112, Y: 117, W: 796, H: 406}},      // 80% of 97 cells, 70% of 28 rows, plus 26 px
		{"50", "10", layout.Rect{X: 247, Y: 207, W: 526, H: 226}},  // cells
		{"100%", "999", layout.Rect{X: 12, Y: 27, W: 996, H: 586}}, // whole cells, cut to the area
	} {
		if got := popupRect(model.Popup{Width: c.w, Height: c.h}, area, cell, 26); got != c.want {
			t.Errorf("%q x %q: %+v, want %+v", c.w, c.h, got, c.want)
		}
	}
	if got := popupTitle(model.Popup{}, &model.Pane{Cmd: []string{"/usr/bin/lazygit", "-p"}}); got != "lazygit" {
		t.Errorf("title %q", got)
	}
}

// A [[popups]] key opens its popup over the shown tab once, and does
// nothing while the tab has one open.
func TestPopupKey(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.nav.sync(&st)
	c, _ := config.ParseChord("Ctrl+Alt+G")
	u.cfg.Popups = []config.Popup{{Name: "git", Chord: c, Command: []string{"lazygit"}, Width: "90%"}}
	e := press("G", key.ModCtrl|key.ModAlt)
	if !u.popupKey(&st, e) {
		t.Fatal("the popup key was not taken")
	}
	want := proto.OpenPopup{WorkspaceID: "w1", Cmd: []string{"lazygit"}, Width: "90%", Title: "git"}
	if got := b.Sent(); len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], want) {
		t.Fatalf("sent %#v", got)
	}
	st = withPopup(st, "pop")
	u.nav.sync(&st)
	n := len(b.Sent())
	if !u.popupKey(&st, e) || len(b.Sent()) != n {
		t.Fatal("a second popup was requested")
	}
	if u.popupKey(&st, press("G", key.ModCtrl)) {
		t.Fatal("Ctrl+G was taken")
	}
}
