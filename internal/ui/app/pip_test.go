package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The picture_in_picture key pins the focused pane, another pane in its
// place, and unpins it when it is the pinned one; a closed pane unpins.
func TestPiPToggle(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	n.globalOp(&st, "picture_in_picture")
	if n.pip != "a" {
		t.Fatalf("pinned %q, want a", n.pip)
	}
	n.selectWorkspace(&st, "w2", "")
	n.globalOp(&st, "picture_in_picture")
	if n.pip != "d" {
		t.Fatalf("pinned %q, want d in place of a", n.pip)
	}
	n.globalOp(&st, "picture_in_picture")
	if n.pip != "" {
		t.Fatalf("pressed again: pinned %q", n.pip)
	}
	if p := n.focusIn(&st, "w1"); p != "a" {
		t.Fatalf("w1's focused pane %q, want a", p)
	}
	n.pip = "b"
	if err := b.Send(proto.ClosePane{Pane: "b"}); err != nil {
		t.Fatal(err)
	}
	st = b.State()
	n.sync(&st)
	if n.pip != "" {
		t.Fatalf("b closed, still pinned %q", n.pip)
	}
}

func TestPiPCorners(t *testing.T) {
	area := image.Rect(0, 0, 1000, 600)
	size := image.Pt(300, 200)
	for c, want := range map[pipCorner]image.Rectangle{
		pipBottomRight: image.Rect(684, 384, 984, 584),
		pipBottomLeft:  image.Rect(16, 384, 316, 584),
		pipTopRight:    image.Rect(684, 16, 984, 216),
		pipTopLeft:     image.Rect(16, 16, 316, 216),
	} {
		r := pipPlace(area, size, c, 16)
		if r != want {
			t.Errorf("corner %d at %v, want %v", c, r, want)
		}
		if got := pipNearest(area, r.Min.Add(r.Max).Div(2)); got != c {
			t.Errorf("a card dropped at corner %d goes to %d", c, got)
		}
	}
}

func TestPiPShift(t *testing.T) {
	for _, c := range []struct{ full, shown, want, shift int }{
		{500, 300, 100, 0},   // the cursor shows from the top
		{500, 300, 420, 120}, // the rows down to it
		{500, 300, 600, 200}, // never past the bottom
		{200, 300, 150, 0},   // a screen that fits
	} {
		if got := pipShift(c.full, c.shown, c.want); got != c.shift {
			t.Errorf("pipShift(%d, %d, %d) = %d, want %d", c.full, c.shown, c.want, got, c.shift)
		}
	}
}

func TestPiPLastRow(t *testing.T) {
	g := vt.Grid{Cols: 4, Rows: 6, Cells: make([]vt.Cell, 24), Cursor: vt.Cursor{Y: 1}}
	if got := pipLastRow(&g); got != 1 {
		t.Fatalf("blank below the cursor: row %d, want 1", got)
	}
	g.Cells[4*4+2].Content = "x"
	if got := pipLastRow(&g); got != 4 {
		t.Fatalf("text on row 4 below the cursor: row %d, want 4", got)
	}
}

// The card shows over another tab, a drag moves it to the nearest corner,
// a click goes to its tab, where the pane shows in place of the card.
func TestPiPCard(t *testing.T) {
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
	frame()
	u.nav.pip = "d" // w2's, while w1 shows
	frame()
	card := u.pip.card.Add(u.contentOrigin)
	if card.Empty() {
		t.Fatal("no card over w1")
	}
	at := func(p image.Point) f32.Point { return f32.Pt(float32(p.X), float32(p.Y)) }
	mid := card.Min.Add(card.Max).Div(2)
	r.Queue(pointer.Event{Kind: pointer.Press, Position: at(mid), Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	frame()
	r.Queue(pointer.Event{Kind: pointer.Move, Position: at(image.Pt(400, 200)), Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	frame()
	r.Queue(pointer.Event{Kind: pointer.Release, Position: at(image.Pt(400, 200)), Source: pointer.Mouse})
	frame()
	frame()
	if u.pip.corner != pipTopLeft || u.nav.workspace != "w1" {
		t.Fatalf("dragged up left: corner %d, tab %s", u.pip.corner, u.nav.workspace)
	}
	card = u.pip.card.Add(u.contentOrigin)
	mid = card.Min.Add(card.Max).Div(2)
	r.Queue(pointer.Event{Kind: pointer.Press, Position: at(mid), Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	frame()
	r.Queue(pointer.Event{Kind: pointer.Release, Position: at(mid), Source: pointer.Mouse})
	frame()
	if u.nav.workspace != "w2" || u.nav.focused() != "d" {
		t.Fatalf("click: at %s/%s, want w2/d", u.nav.workspace, u.nav.focused())
	}
	u.pip.card = image.Rectangle{}
	frame()
	if !u.pip.card.Empty() || u.nav.pip != "d" {
		t.Fatalf("card %v drawn over its own pane, pinned %q", u.pip.card, u.nav.pip)
	}
}
