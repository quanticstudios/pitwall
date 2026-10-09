package settings

import (
	"image"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func narrowCtx(ops *op.Ops, w int) gl.Context {
	return gl.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Constraints{Max: image.Pt(w, 1000)}, Now: time.Now()}
}

// TestRowStacks: a row puts its control under the label once the label
// column beside it would be under 220dp, and side by side otherwise.
func TestRowStacks(t *testing.T) {
	var ops op.Ops
	var p Page
	p.th = theme.Dark()
	box := func(w, h int) gl.Widget {
		return func(gl.Context) gl.Dimensions { return gl.Dimensions{Size: image.Pt(w, h)} }
	}
	for _, tc := range []struct {
		w     int
		stack bool
	}{{200 + 16 + 220, false}, {200 + 16 + 219, true}, {1000, false}, {300, true}} {
		d := p.rowLine(narrowCtx(&ops, tc.w), box(50, 20), box(200, 28))
		want := 28 // side by side: the taller of the two
		if tc.stack {
			want = 20 + 12 + 28
		}
		if d.Size.Y != want {
			t.Errorf("width %d: height %d, want %d (stacked %v)", tc.w, d.Size.Y, want, tc.stack)
		}
	}
}

// TestNavCollapses: under 760dp the categories go in a dropdown, and the
// page still draws with the dropdown open.
func TestNavCollapses(t *testing.T) {
	var ops op.Ops
	gtx := narrowCtx(&ops, 0)
	if !collapsed(gtx, 759) || collapsed(gtx, 760) {
		t.Errorf("collapse at 759/760: %v %v", collapsed(gtx, 759), collapsed(gtx, 760))
	}
	gtx.Metric = unit.Metric{PxPerDp: 2, PxPerSp: 2}
	if !collapsed(gtx, 1500) {
		t.Error("the breakpoint is in dp, not pixels")
	}
	var p Page
	p.Show("/nonexistent/config.toml")
	p.focusSearch = false
	p.dd = catMenu
	gtx = narrowCtx(&ops, 600)
	gtx.Constraints.Min = gtx.Constraints.Max
	p.Layout(gtx, theme.Dark(), p.s, nil)
	if p.dd != catMenu {
		t.Error("the open dropdown closed on its own")
	}
	gtx = narrowCtx(&ops, 1000)
	gtx.Constraints.Min = gtx.Constraints.Max
	p.Layout(gtx, theme.Dark(), p.s, nil)
	if p.dd == catMenu {
		t.Error("the dropdown stays open once the column is back")
	}
}

// TestThemeCols: four cards a row when they fit, else two, never three.
func TestThemeCols(t *testing.T) {
	var ops op.Ops
	gtx := narrowCtx(&ops, 0)
	for w, want := range map[int]int{1000: 4, 596: 4, 595: 2, 540: 2, 300: 2} {
		if got := themeCols(gtx, w); got != want {
			t.Errorf("themeCols(%d) = %d, want %d", w, got, want)
		}
	}
}

// TestUsageBlank: Usage says it is reading before the first scan lands,
// and says so when the scan found nothing.
func TestUsageBlank(t *testing.T) {
	if wait, line, ok := usageBlank(nil); !wait || !ok || line == "" {
		t.Errorf("scanning: %v %q %v", wait, line, ok)
	}
	empty := &flow.Report{Days: make([]time.Time, 7)}
	if wait, line, ok := usageBlank(empty); wait || !ok || line != "No Claude Code, Codex or pi sessions in the last 7 days." {
		t.Errorf("empty: %v %q %v", wait, line, ok)
	}
	full := &flow.Report{Days: make([]time.Time, 7)}
	full.Tokens.Input = 1
	if _, _, ok := usageBlank(full); ok {
		t.Error("a report with tokens is blank")
	}
}
