package app

import (
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestNextChatSize: the keys step by a point from the size shown, stop
// at 6 and 48, and reset to the interface size.
func TestNextChatSize(t *testing.T) {
	for _, c := range []struct {
		action  string
		cur, ui float64
		want    float64
	}{
		{"chat_text_bigger", 13, 13, 14},
		{"chat_text_smaller", 13, 13, 12},
		{"chat_text_bigger", 48, 13, 48},
		{"chat_text_smaller", 6, 13, 6},
		{"chat_text_bigger", 13.5, 13, 15},
		{"chat_text_reset", 20, 14, 14},
	} {
		if got := nextChatSize(c.action, c.cur, c.ui); got != c.want {
			t.Errorf("%s from %v (ui %v) = %v, want %v", c.action, c.cur, c.ui, got, c.want)
		}
	}
}

// TestChatMetric: the chat scales dp and sp by chat_size over ui_size,
// and an unset or equal size leaves the window's metric.
func TestChatMetric(t *testing.T) {
	m := unit.Metric{PxPerDp: 1.5, PxPerSp: 1.5}
	u := &ui{}
	if got := u.chatMetric(m); got != m {
		t.Errorf("unset: %+v", got)
	}
	u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 26
	if got := u.chatMetric(m); got.PxPerDp != 3 || got.PxPerSp != 3 {
		t.Errorf("26 over 13: %+v", got)
	}
}

// TestChatSizeKeys: Ctrl+= on a pane chat makes the chat's text bigger at
// once and writes [font] chat_size; Ctrl+0 removes the key again; on a
// shell pane the keys stay the pane's.
func TestChatSizeKeys(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[font]\nui_size = 13\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
	b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/s.jsonl"
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.Claude, u.cfg.Path, u.cfg.Font.UISize, u.cfg.Font.ChatSize = true, path, 13, 13
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
		r.Frame(&ops)
	}
	press := func(name key.Name) {
		r.Queue(key.Event{Name: name, Modifiers: key.ModCtrl, State: key.Press},
			key.Event{Name: name, Modifiers: key.ModCtrl, State: key.Release})
		frame()
		frame()
	}
	file := func() string { b, _ := os.ReadFile(path); return string(b) }
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	u.nav.setFocus("h")
	frame()
	frame()
	press("=")
	press("=")
	if u.cfg.Font.ChatSize != 15 || !strings.Contains(file(), "chat_size = 15") {
		t.Fatalf("Ctrl+= twice: size %v, file:\n%s", u.cfg.Font.ChatSize, file())
	}
	if u.chatSizeLabel(gl.Context{Now: time.Now()}) == nil {
		t.Error("no size in the header after a step")
	}
	press("-")
	if u.cfg.Font.ChatSize != 14 || !strings.Contains(file(), "chat_size = 14") {
		t.Fatalf("Ctrl+-: size %v, file:\n%s", u.cfg.Font.ChatSize, file())
	}
	press("0")
	if u.cfg.Font.ChatSize != 13 || strings.Contains(file(), "chat_size") {
		t.Fatalf("Ctrl+0: size %v, file:\n%s", u.cfg.Font.ChatSize, file())
	}
	for _, m := range b.Sent() {
		if in, ok := m.(proto.Input); ok {
			t.Errorf("a chat size key typed %q into %s", in.Data, in.Pane)
		}
	}

	// A shell tab: Ctrl+= is the pane's.
	u.nav.selectWorkspace(&st, "w1", "")
	frame()
	u.nav.chatTextKey = u.chatTextKeys(&st)
	if slices.ContainsFunc(u.nav.keyFilters(&st), func(f key.Filter) bool { return f.Name == "=" && f.Required == key.ModCtrl }) {
		t.Error("Ctrl+= taken from a shell")
	}
}
