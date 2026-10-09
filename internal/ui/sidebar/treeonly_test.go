package sidebar

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTreeOnly clicks where the footer's settings button is: a sidebar
// opens the settings, a TreeOnly one, the switcher's preview, has no
// footer there to click.
func TestTreeOnly(t *testing.T) {
	st := model.State{Workspaces: []model.Workspace{{ID: "w", Name: "Session"}}}
	for _, treeOnly := range []bool{false, true} {
		s := Sidebar{TreeOnly: treeOnly}
		var r input.Router
		var ops op.Ops
		var evs []Event
		frame := func() {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(288, 600)), Now: time.Now()}
			_, e := s.Layout(gtx, theme.Dark(), &st, "", "w")
			evs = append(evs, e...)
			r.Frame(&ops)
		}
		frame()
		gear := f32.Pt(265, 578)
		r.Queue(
			pointer.Event{Kind: pointer.Move, Position: gear, Source: pointer.Mouse},
			pointer.Event{Kind: pointer.Press, Position: gear, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse},
			pointer.Event{Kind: pointer.Release, Position: gear, Source: pointer.Mouse},
		)
		frame()
		frame()
		opened := slices.ContainsFunc(evs, func(e Event) bool { _, ok := e.(OpenSettings); return ok })
		if opened == treeOnly {
			t.Errorf("TreeOnly %v: the settings button opened settings: %v", treeOnly, opened)
		}
	}
}
