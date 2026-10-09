package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestEscapeReturnsFocus opens the command palette, the session switcher
// and the find bar in turn: Escape closes each, the pane it came from is
// still focused, and the next key typed reaches that pane.
func TestEscapeReturnsFocus(t *testing.T) {
	cs := key.ModCtrl | key.ModShift
	for _, c := range []struct {
		name string
		open key.Event
		shut func(u *ui) bool
	}{
		{"palette", press("P", cs), func(u *ui) bool { return !u.pal.open }},
		{"switcher", press("S", cs), func(u *ui) bool { return !u.sw.open }},
		{"find", press("F", cs), func(u *ui) bool { return u.find.pane == "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := NewFakeBackend()
			u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
			var r input.Router
			var ops op.Ops
			frame := func() {
				ops.Reset()
				gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
					Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
				u.layout(gtx)
				r.Frame(&ops)
			}
			hit := func(e key.Event) {
				r.Queue(e)
				frame()
				r.Queue(key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
				frame()
			}
			frame()
			frame()
			pane := u.nav.focused()
			hit(c.open)
			frame()
			if c.shut(u) {
				t.Fatal("did not open")
			}
			hit(press(key.NameEscape, 0))
			frame()
			if !c.shut(u) || u.nav.focused() != pane {
				t.Fatalf("Escape: closed %v, focus on %q, want %q", c.shut(u), u.nav.focused(), pane)
			}
			r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
			frame()
			var got []string
			for _, m := range b.Sent() {
				if in, ok := m.(proto.Input); ok {
					got = append(got, in.Pane+":"+string(in.Data))
				}
			}
			if len(got) != 1 || got[0] != pane+":q" {
				t.Fatalf("after Escape the panes got %q, want %s:q", got, pane)
			}
		})
	}
}
