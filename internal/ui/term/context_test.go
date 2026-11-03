package term

import (
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"github.com/quanticstudios/pitwall/internal/vt"
	"image"
	"testing"
)

func TestContextMenuMouse(t *testing.T) {
	for _, tc := range []struct {
		mouse vt.MouseMode
		shift bool
		want  bool
	}{{vt.MouseOff, false, true}, {vt.MouseNormal, false, false}, {vt.MouseNormal, true, true}} {
		p := newPane(t)
		e := p.press(0, 0, 0)
		e.Buttons = pointer.ButtonSecondary
		if tc.shift {
			e.Modifiers = key.ModShift
		}
		in := p.v.pointer(e, grid("test"), vt.Modes{Mouse: tc.mouse, MouseSGR: true}, true)
		at, ok := p.v.ContextMenu()
		if ok != tc.want {
			t.Fatalf("mouse %d, shift %v: menu %v", tc.mouse, tc.shift, ok)
		}
		if ok && (at != image.Pt(int(e.Position.X), int(e.Position.Y)) || len(in) != 0) {
			t.Fatalf("menu at %v, input %q", at, in)
		}
		if _, ok := p.v.ContextMenu(); ok {
			t.Fatal("click returned twice")
		}
		if !tc.want && len(in) == 0 {
			t.Fatal("application lost right click")
		}
	}
}
