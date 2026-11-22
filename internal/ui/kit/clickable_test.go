package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func cursorAt(r *input.Router, ops *op.Ops, p f32.Point) pointer.Cursor {
	r.Queue(pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse})
	r.Frame(ops)
	return r.Cursor()
}

func TestClickableCursor(t *testing.T) {
	sz := image.Pt(80, 28)
	check := func(disabled bool, want pointer.Cursor) {
		t.Helper()
		var c widget.Clickable
		var r input.Router
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(sz)}
		if disabled {
			gtx = gtx.Disabled()
		}
		Clickable(gtx, &c, func(layout.Context) layout.Dimensions { return layout.Dimensions{Size: sz} })
		r.Frame(&ops)
		if got := cursorAt(&r, &ops, f32.Pt(40, 14)); got != want {
			t.Fatalf("disabled=%v: cursor %v, want %v", disabled, got, want)
		}
	}
	check(false, pointer.CursorPointer)
	check(true, pointer.CursorDefault)
}

func TestControlsPointerCursor(t *testing.T) {
	th := theme.Dark()
	icon := func(layout.Context, color.NRGBA, int) {}
	cases := []struct {
		name string
		draw func(layout.Context, *widget.Clickable) layout.Dimensions
		want pointer.Cursor
		pt   f32.Point
	}{
		{"Button", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			return Button(gtx, th, c, Primary, Medium, "Save")
		}, pointer.CursorPointer, f32.Pt(12, 12)},
		{"ButtonDisabled", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			return Button(gtx, th, c, Primary.Disabled(), Medium, "Save")
		}, pointer.CursorDefault, f32.Pt(12, 12)},
		{"IconButton", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			return IconButton(gtx, th, c, Ghost, Medium, icon)
		}, pointer.CursorPointer, f32.Pt(12, 12)},
		{"IconButtonDisabled", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			return IconButton(gtx, th, c, Ghost.Disabled(), Medium, icon)
		}, pointer.CursorDefault, f32.Pt(12, 12)},
		{"SwitchFullHitArea", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(0, 32)
			return Switch(gtx, th, c, true)
		}, pointer.CursorPointer, f32.Pt(16, 2)},
		{"SwitchDisabled", func(gtx layout.Context, c *widget.Clickable) layout.Dimensions {
			return Switch(gtx.Disabled(), th, c, true)
		}, pointer.CursorDefault, f32.Pt(16, 9)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c widget.Clickable
			var r input.Router
			var ops op.Ops
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(200, 100)}}
			tc.draw(gtx, &c)
			r.Frame(&ops)
			if got := cursorAt(&r, &ops, tc.pt); got != tc.want {
				t.Fatalf("cursor at %v = %v, want %v", tc.pt, got, tc.want)
			}
		})
	}
}
