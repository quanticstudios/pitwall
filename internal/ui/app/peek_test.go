package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Peeking at the shown tab does nothing; peeking at another takes its
// focused pane. Escape closes the peek, keep_peek goes to its tab with
// that pane focused, and the window takes keep_peek's chord only while a
// peek is open. A peek ends with its tab.
func TestPeekState(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: conventional}
	n.sync(&st)
	keep := press(key.NameReturn, key.ModCtrl|key.ModShift)
	takes := func() bool {
		return slices.ContainsFunc(n.keyFilters(&st), func(f key.Filter) bool { return f.Name == keep.Name && f.Required == keep.Modifiers })
	}
	if takes() {
		t.Fatal("keep_peek's chord taken with no peek open")
	}
	n.peekTab(&st, "w1", "")
	if n.peek != "" {
		t.Fatalf("peeked at the shown tab: %q", n.peek)
	}
	n.peekTab(&st, "w2", "")
	if n.peek != "d" || !takes() {
		t.Fatalf("peek %q, keep_peek taken %v; want d, true", n.peek, takes())
	}
	n.key(&st, press(key.NameEscape, 0))
	if n.peek != "" || n.workspace != "w1" {
		t.Fatalf("Escape: peek %q on %s", n.peek, n.workspace)
	}
	n.peekTab(&st, "w2", "")
	if msg := n.key(&st, keep); msg != nil || n.peek != "" || n.workspace != "w2" || n.focused() != "d" {
		t.Fatalf("keep: sent %v, peek %q, at %s/%s", msg, n.peek, n.workspace, n.focused())
	}
	n.peekTab(&st, "w1b", "")
	if err := b.Send(proto.CloseTab{WorkspaceID: "w1b"}); err != nil {
		t.Fatal(err)
	}
	st = b.State()
	n.sync(&st)
	if n.peek != "" {
		t.Fatalf("w1b closed, still peeking at %q", n.peek)
	}
}

// keepPeek attaches a detached tab before it shows it.
func TestKeepPeekDetached(t *testing.T) {
	st := NewFakeBackend().State()
	n := nav{keys: conventional}
	n.sync(&st)
	n.peekTab(&st, "w7", "")
	msg := n.keepPeek(&st)
	if msg != (proto.DetachSession{WorkspaceID: "w7", Detached: false}) || n.workspace != "w7" || n.focused() != "k" {
		t.Fatalf("sent %v, at %s/%s", msg, n.workspace, n.focused())
	}
}

// shownFake records the panes the window asks frames of.
type shownFake struct {
	*FakeBackend
	shown []string
}

func (f *shownFake) Show(panes, _ []string) { f.shown = panes }

// A peek streams its pane's frames and takes the keys: typed text goes to
// it, not the shown tab's pane, until Escape, after which the keys go
// back. keep_peek goes to its tab. A press outside the card closes it,
// and so does its tab closing.
func TestPeekTakesKeys(t *testing.T) {
	fb := NewFakeBackend()
	b := &shownFake{FakeBackend: fb}
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
	typed := func() []string {
		var got []string
		for _, m := range fb.Sent() {
			if in, ok := m.(proto.Input); ok {
				got = append(got, in.Pane+":"+string(in.Data))
			}
		}
		return got
	}
	peek := func() {
		st := b.State()
		u.openPeek(&st, "w2", "")
		frame()
		frame()
	}
	frame()
	frame()
	pane := u.nav.focused()
	peek()
	if u.nav.peek != "d" || !slices.Contains(b.shown, "d") {
		t.Fatalf("peek %q, frames asked of %v", u.nav.peek, b.shown)
	}
	in := f32.Pt(640, 400) // inside the card, on the pane
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: in},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: in})
	frame()
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	if got := typed(); len(got) != 1 || got[0] != "d:q" || u.nav.peek != "d" || u.nav.focused() != pane {
		t.Fatalf("after a click in the peek: peek %q, tab's focus %q; the panes got %q, want d:q", u.nav.peek, u.nav.focused(), got)
	}
	r.Queue(press(key.NameEscape, 0))
	frame()
	r.Queue(key.Event{Name: key.NameEscape, State: key.Release})
	frame()
	frame()
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	if got := typed(); u.nav.peek != "" || len(got) != 2 || got[1] != pane+":q" {
		t.Fatalf("after Escape: peek %q, the panes got %q, want %s:q last", u.nav.peek, got, pane)
	}

	peek()
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(20, 20)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(20, 20)})
	frame()
	if u.nav.peek != "" || u.nav.workspace != "w1" {
		t.Fatalf("a press outside the card: peek %q on %s", u.nav.peek, u.nav.workspace)
	}

	peek()
	r.Queue(press(key.NameReturn, key.ModCtrl|key.ModShift))
	frame()
	if u.nav.peek != "" || u.nav.workspace != "w2" || u.nav.focused() != "d" {
		t.Fatalf("keep_peek: peek %q, at %s/%s", u.nav.peek, u.nav.workspace, u.nav.focused())
	}
	st := b.State()
	u.nav.selectWorkspace(&st, "w1", "")
	frame()

	peek()
	if err := fb.Send(proto.CloseTab{WorkspaceID: "w2"}); err != nil {
		t.Fatal(err)
	}
	frame()
	if u.nav.peek != "" {
		t.Fatalf("w2 closed, still peeking at %q", u.nav.peek)
	}
}
