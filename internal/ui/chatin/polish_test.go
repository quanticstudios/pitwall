package chatin

import (
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"image"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Ctrl+Backspace and Ctrl+Delete delete a word, as Alt+Backspace does,
// and Ctrl+Left and Ctrl+Right move by one; a word delete right after an
// image token takes the whole token and its image.
func TestComposerWordKeys(t *testing.T) {
	h := newHarness(t)
	h.typ("hello big world")
	if h.press(key.NameDeleteBackward, key.ModCtrl); h.c.Text() != "hello big " {
		t.Fatalf("Ctrl+Backspace left %q", h.c.Text())
	}
	if h.press(key.NameDeleteBackward, key.ModAlt); h.c.Text() != "hello " {
		t.Fatalf("Alt+Backspace left %q", h.c.Text())
	}
	h.press(key.NameLeftArrow, key.ModCtrl)
	if s, e := h.c.ed.Selection(); s != 0 || e != 0 {
		t.Fatalf("Ctrl+Left put the caret at %d-%d, want 0", s, e)
	}
	if h.press(key.NameDeleteForward, key.ModCtrl); strings.TrimSpace(h.c.Text()) != "" {
		t.Fatalf("Ctrl+Delete left %q", h.c.Text())
	}
	h.c.SetText("")
	h.typ("one two")
	h.press(key.NameLeftArrow, key.ModCtrl)
	h.press(key.NameRightArrow, key.ModCtrl)
	if _, e := h.c.ed.Selection(); e != h.c.ed.Len() {
		t.Fatalf("Ctrl+Right put the caret at %d, want the end %d", e, h.c.ed.Len())
	}

	h.c.SetText("look ")
	h.clip = pngOf(40, 20)
	h.paste("")
	h.waitImage()
	if h.c.Text() != "look [Image #1] " || len(h.c.Images()) != 1 {
		t.Fatalf("after the paste: %q, %d images", h.c.Text(), len(h.c.Images()))
	}
	if h.press(key.NameDeleteBackward, key.ModCtrl); h.c.Text() != "look " || len(h.c.Images()) != 0 {
		t.Fatalf("Ctrl+Backspace after the token left %q and %d images", h.c.Text(), len(h.c.Images()))
	}
}

// The mode chip opens a picker of the modes, bypass only when the chat
// may enter it, and a pick sends that mode; Shift+Tab still cycles.
func TestComposerModePicker(t *testing.T) {
	if got := ModeItems(false); !slices.Equal(got, []string{ModeDefault, ModeAccept, ModePlan}) {
		t.Errorf("items without bypass: %v", got)
	}
	if got := ModeItems(true); !slices.Equal(got, []string{ModeDefault, ModeAccept, ModePlan, ModeBypass}) {
		t.Errorf("items with bypass: %v", got)
	}
	h := newHarness(t)
	h.c.modeBtn.Click()
	if m := h.frame(); len(m) != 0 || !h.c.menu || h.c.which != &h.c.modeBtn {
		t.Fatalf("a click on the mode chip sent %v, menu %v: want its picker open", m, h.c.menu)
	}
	h.frame()
	if h.c.items["mode:"+ModeBypass] != nil {
		t.Error("the picker lists bypass for a chat that never ran in it")
	}
	h.c.click("mode:" + ModePlan).Click()
	m := h.frame()
	if len(m) != 1 || m[0] != (proto.ChatSet{Chat: "c1", Mode: ModePlan}) || h.c.menu {
		t.Fatalf("picking Plan sent %v, menu %v", m, h.c.menu)
	}
	h.snap.PermissionMode = ModeBypass
	h.frame()
	h.snap.PermissionMode = ModeDefault
	h.c.mode = held{}
	if m := h.press(key.NameTab, key.ModShift); len(m) != 1 || m[0] != (proto.ChatSet{Chat: "c1", Mode: ModeAccept}) {
		t.Fatalf("Shift+Tab sent %v", m)
	}
	h.c.modeBtn.Click()
	h.frame()
	h.frame()
	if h.c.items["mode:"+ModeBypass] == nil {
		t.Error("a chat that ran in bypass has no bypass in its picker")
	}
}

// Each picker chip's anchor is inside the card on the control row, so
// its popover opens above the chip, not above the card.
func TestComposerAnchors(t *testing.T) {
	h := newHarness(t)
	h.snap.Provider = "claude"
	h.typ(strings.Repeat("a long line of draft text ", 12))
	card := h.c.cardSize
	for _, b := range []*widget.Clickable{&h.c.modeBtn, &h.c.modelBtn, &h.c.effortBtn} {
		r := h.c.anchors[b]
		if r.Empty() || r.Min.Y < h.c.barY || r.Max.Y > card.Y || r.Max.X > card.X {
			t.Errorf("anchor %v: want it on the control row from y %d in the %v card", r, h.c.barY, card)
		}
	}
	if m, e := h.c.anchors[&h.c.modelBtn], h.c.anchors[&h.c.effortBtn]; !(h.c.anchors[&h.c.modeBtn].Max.X < m.Min.X && m.Max.X < e.Min.X) {
		t.Errorf("anchors out of order: mode %v, model %v, effort %v", h.c.anchors[&h.c.modeBtn], m, e)
	}
}

// An image token wraps as one word: whatever text comes before it, its
// chip covers one line's region, never a tail on the next line.
func TestComposerTokenWraps(t *testing.T) {
	h := newHarness(t)
	h.clip = pngOf(10, 10)
	h.paste("")
	h.waitImage()
	if !strings.Contains(h.c.ed.Text(), "[Image #1]") || h.c.Text() != "[Image #1] " {
		t.Fatalf("editor %q, text %q: want the glued token in, the plain one out", h.c.ed.Text(), h.c.Text())
	}
	var rg []widget.Region
	for n := range 160 {
		h.c.SetText(strings.Repeat("x", n%7+1) + strings.Repeat(" word", n) + " [Image #1] tail")
		h.frame()
		s, _ := TokenSpan(h.c.Text(), 1)
		if s < 0 {
			t.Fatalf("prefix %d: the token is gone from %q", n, h.c.Text())
		}
		if rg = h.c.ed.Regions(s, s+len([]rune(Token(1))), rg[:0]); len(rg) != 1 {
			t.Fatalf("prefix %d: the token spans %d lines", n, len(rg))
		}
	}
}

// While Claude works Enter queues and Ctrl+Enter sends now; idle, Ctrl+Enter
// sends as Enter does.
func TestComposerSendNow(t *testing.T) {
	h := newHarness(t)
	h.typ("idle")
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 1 || m[0].(proto.ChatSend).How != "" {
		t.Fatalf("idle Ctrl+Enter sent %v", m)
	}
	h.snap.SessionState = "running"
	h.snap.Turns = []session.TurnSummary{{ID: "t1", State: session.TurnRunning}}
	h.typ("later")
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("busy Enter sent %v", m)
	}
	h.typ("now")
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 1 || m[0].(proto.ChatSend).How != "now" || m[0].(proto.ChatSend).Text != "now" {
		t.Fatalf("busy Ctrl+Enter sent %v", m)
	}
}

// An open popover closes when the composer loses the keys, as when
// another pane takes focus, and on CloseMenu; a press outside it closes
// it and still reaches what lies under it.
func TestComposerPopoverCloses(t *testing.T) {
	h := newHarness(t)
	h.c.modelBtn.Click()
	h.frame()
	if !h.c.menu {
		t.Fatal("the model chip opened no popover")
	}
	h.c.CloseMenu()
	if h.c.menu {
		t.Fatal("CloseMenu left it open")
	}

	h.c.modelBtn.Click()
	h.frame()
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 800)), Now: h.now}
	env := h.env()
	env.Focus = false
	h.c.Layout(gtx, theme.Dark(), env)
	if h.c.menu {
		t.Fatal("the popover stayed open with the keys elsewhere")
	}

	// Under the composer, another pane's press area.
	var below int
	h.c.modelBtn.Click()
	h.frame()
	frame := func() {
		h.ops.Reset()
		gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Exact(image.Pt(1000, 800)), Now: h.now}
		gtx.Constraints.Min.Y = 0
		area := clip.Rect{Max: image.Pt(1000, 800)}.Push(gtx.Ops)
		event.Op(gtx.Ops, &below)
		area.Pop()
		h.c.Layout(gtx, theme.Dark(), h.env())
		h.r.Frame(&h.ops)
	}
	frame()
	gtx.Source = h.r.Source()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &below, Kinds: pointer.Press}); !ok {
			break
		}
	}
	at := f32.Pt(900, 20) // above the card, far right of the popover
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	pressed := false
	for {
		ev, ok := h.r.Source().Event(pointer.Filter{Target: &below, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			pressed = true
		}
	}
	frame()
	if h.c.menu || !pressed {
		t.Fatalf("a press outside: menu %v, the pane under it pressed %v", h.c.menu, pressed)
	}
}
