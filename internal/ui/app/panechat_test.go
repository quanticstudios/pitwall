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

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPaneChat: a pane running Claude Code with a transcript shows as a
// chat in [chat] claude's chat view, Alt+T flips it, terminal starts it
// as the TUI, and an exit drops the flip; the chat view still sends the
// pane its size.
func TestPaneChat(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
	b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/s.jsonl"
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.Claude = true
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	ws := st.Workspace("w6")
	if !u.paneChatShown(&st, ws, "h") {
		t.Fatal("claude pane not shown as a chat")
	}
	if c := findChat(&st, "h"); c == nil || c.Provider != model.ProviderClaude {
		t.Fatalf("findChat(h) = %+v", c)
	}

	var ops op.Ops
	u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
	b.mu.Lock()
	asked := slices.Contains(b.asked, "h")
	b.mu.Unlock()
	sized := slices.ContainsFunc(b.Sent(), func(m any) bool { r, ok := m.(proto.Resize); return ok && r.Pane == "h" })
	if !asked || !sized {
		t.Fatalf("chat view: read %v, sized %v", asked, sized)
	}

	u.nav.paneChatKey = u.paneChatKey(&st)
	if !slices.ContainsFunc(u.nav.keyFilters(&st), func(f key.Filter) bool { return f.Name == "T" && f.Required == key.ModAlt }) {
		t.Fatal("Alt+T left to the pane")
	}
	u.flipPaneChat("h")
	if u.paneChatShown(&st, ws, "h") {
		t.Fatal("flipped pane still a chat")
	}
	u.cfg.Chat.Claude = false
	if !u.paneChatShown(&st, ws, "h") {
		t.Fatal("terminal default: flipped pane not a chat")
	}
	st.Panes[i].Exited = true
	if u.paneChatShown(&st, ws, "h") || u.paneFlip["h"] {
		t.Fatal("an exited claude kept its chat or flip")
	}
	if u.nav.paneChatKey = u.paneChatKey(&st); slices.ContainsFunc(u.nav.keyFilters(&st), func(f key.Filter) bool { return f.Name == "T" && f.Required == key.ModAlt }) {
		t.Fatal("Alt+T taken from a shell")
	}
}

// TestPaneChatAltTAfterClick: after a click anywhere in a pane chat, the
// transcript or the composer, also coming from a shell pane of the split,
// the first Alt+T flips the pane to the TUI and types nothing into it.
func TestPaneChatAltTAfterClick(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	for _, c := range []struct{ ws, pane, first, where string }{
		{"w6", "h", "h", "transcript"}, {"w6", "h", "h", "composer"},
		{"w1", "b", "a", "transcript"}, {"w1", "b", "a", "composer"},
	} {
		b := &chatBackend{FakeBackend: NewFakeBackend()}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Chat.Claude = true
		var r input.Router
		var ops op.Ops
		frame := func() {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
			r.Frame(&ops)
		}
		// The pane is a shell with the keys first, then claude starts in it.
		b.mu.Lock()
		i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == c.pane })
		b.st.Panes[i].Provider = ""
		b.mu.Unlock()
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, c.ws, "")
		u.nav.setFocus(c.first)
		frame()
		frame()
		b.mu.Lock()
		b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/s.jsonl"
		b.mu.Unlock()
		st = b.State()
		u.nav.sync(&st)
		frame()
		frame()
		rect := u.panes[c.pane].rect
		at := f32.Pt(float32(rect.Min.X+rect.Dx()/2), float32(rect.Min.Y+rect.Dy()/3))
		if c.where == "composer" {
			at.Y = float32(rect.Max.Y - 40)
		}
		r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
		frame() // why: one frame, as a window draws for a click when nothing asks for another
		if st = b.State(); !u.paneChatShown(&st, st.Workspace(c.ws), c.pane) {
			t.Fatalf("%s: not a chat before Alt+T", c.pane)
		}
		if f := u.nav.focused(); f != c.pane {
			t.Fatalf("%s %s click: focus on %s", c.pane, c.where, f)
		}
		before := len(b.Sent())
		// As X11 sends a quick Alt+T: Alt down on its own first, the whole
		// chord in one batch, before a frame has run since the click.
		r.Queue(key.Event{Name: key.NameAlt, Modifiers: key.ModAlt, State: key.Press},
			key.Event{Name: "T", Modifiers: key.ModAlt, State: key.Press},
			key.Event{Name: "T", Modifiers: key.ModAlt, State: key.Release},
			key.Event{Name: key.NameAlt, State: key.Release})
		frame()
		frame()
		st = b.State()
		if u.paneChatShown(&st, st.Workspace(c.ws), c.pane) {
			t.Errorf("%s %s click: the first Alt+T did not flip the pane", c.pane, c.where)
		}
		for _, m := range b.Sent()[before:] {
			if in, ok := m.(proto.Input); ok {
				t.Errorf("%s %s click: Alt+T typed %q into %s", c.pane, c.where, in.Data, in.Pane)
			}
		}
	}
}
