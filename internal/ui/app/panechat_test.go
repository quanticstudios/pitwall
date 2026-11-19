package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/key"
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
