package app

import (
	"image"
	"slices"
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
)

// TestCopiedText checks the copy notice counts characters on one line and
// lines otherwise.
func TestCopiedText(t *testing.T) {
	for s, want := range map[string]string{
		"x":             "Copied 1 character",
		"héllo wörld":   "Copied 11 characters",
		"one\ntwo\nend": "Copied 3 lines",
		"a\n":           "Copied 2 lines",
	} {
		if got := copiedText(s); got != want {
			t.Errorf("copiedText(%q) = %q, want %q", s, got, want)
		}
	}
}

// TestDismissStateNotice checks a click on the notice's text leaves it up,
// and one on its close button hides it and tells the daemon.
func TestDismissStateNotice(t *testing.T) {
	const text = "Your saved tabs could not be read."
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark()}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(800, 600)), Now: time.Now()}
		u.drawStateNotice(gtx, text)
		r.Frame(&ops)
	}
	click := func(at f32.Point) {
		frame()
		for _, k := range []pointer.Kind{pointer.Press, pointer.Release} {
			r.Queue(pointer.Event{Kind: k, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at})
		}
		frame()
	}
	sent := func() bool { return slices.Contains(b.Sent(), any(proto.DismissNotice{Notice: text})) }
	// The 640dp box is centred 12dp down; its 16dp close button sits 14dp
	// in from the right, 10dp down.
	click(f32.Pt(200, 30))
	if u.dismissed != "" || sent() {
		t.Fatal("a click on the text dismissed the notice")
	}
	click(f32.Pt(80+640-14-8, 12+10+8))
	if u.dismissed != text || !sent() {
		t.Fatalf("close button: dismissed %q, sent %v", u.dismissed, b.Sent())
	}
}
