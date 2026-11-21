package app

import (
	"image"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
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

// TestChatSizeKeys: on a pane chat and on a New task chat tab of Claude,
// Codex and Antigravity, Ctrl+= (also as Ctrl++ and Ctrl+Shift++) makes
// the chat's text bigger at once and writes [font] chat_size, Ctrl+-
// steps back, Ctrl+0 removes the key, and none of them types into a pane.
// A shell tab, a pane flipped to its TUI and a chat tab in its terminal
// view leave the keys to the pane.
func TestChatSizeKeys(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	for _, prov := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity} {
		for _, tab := range []bool{false, true} {
			name := string(prov) + " pane chat"
			if tab {
				name = string(prov) + " chat tab"
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[font]\nui_size = 13\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			b := &chatBackend{FakeBackend: NewFakeBackend()}
			ws := "w6"
			b.mu.Lock()
			i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
			b.st.Panes[i].Provider, b.st.Panes[i].Transcript = prov, "/tmp/s.jsonl"
			if tab {
				ws = "wc"
				b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Path: t.TempDir(), Chat: "c1"})
				s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
				s1.Order = append(s1.Order, "wc")
				b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: prov, State: session.StateReady}}
			}
			b.mu.Unlock()
			u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
			u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
			u.cfg.Path, u.cfg.Font.UISize, u.cfg.Font.ChatSize = path, 13, 13
			var r input.Router
			var ops op.Ops
			frame := func() {
				ops.Reset()
				u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
					Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
				r.Frame(&ops)
			}
			press := func(name key.Name, mods key.Modifiers) {
				r.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press},
					key.Event{Name: name, Modifiers: mods, State: key.Release})
				frame()
				frame()
			}
			file := func() string { b, _ := os.ReadFile(path); return string(b) }
			st := b.State()
			u.nav.sync(&st)
			u.nav.selectWorkspace(&st, ws, "")
			if !tab {
				u.nav.setFocus("h")
			}
			frame()
			frame()
			if !u.chatTextKeys(&st) {
				t.Fatalf("%s: the window does not take the keys", name)
			}
			press("=", key.ModCtrl)
			press("+", key.ModCtrl)
			press("+", key.ModCtrl|key.ModShift)
			if u.cfg.Font.ChatSize != 16 || !strings.Contains(file(), "chat_size = 16") {
				t.Fatalf("%s: Ctrl+=, Ctrl++, Ctrl+Shift++: size %v, file:\n%s", name, u.cfg.Font.ChatSize, file())
			}
			if u.chatSizeLabel(gl.Context{Now: time.Now()}) == nil {
				t.Errorf("%s: no size in the header after a step", name)
			}
			press("-", key.ModCtrl)
			if u.cfg.Font.ChatSize != 15 || !strings.Contains(file(), "chat_size = 15") {
				t.Fatalf("%s: Ctrl+-: size %v, file:\n%s", name, u.cfg.Font.ChatSize, file())
			}
			press("0", key.ModCtrl)
			if u.cfg.Font.ChatSize != 13 || strings.Contains(file(), "chat_size") || !strings.Contains(file(), "ui_size = 13") {
				t.Fatalf("%s: Ctrl+0: size %v, file:\n%s", name, u.cfg.Font.ChatSize, file())
			}
			for _, m := range b.Sent() {
				if in, ok := m.(proto.Input); ok {
					t.Errorf("%s: a chat size key typed %q into %s", name, in.Data, in.Pane)
				}
			}
			if tab { // the terminal view: the keys are its pane's
				b.mu.Lock()
				b.st.Chats[0].Terminal = "h"
				b.mu.Unlock()
			} else { // flipped to the TUI
				u.flipPaneChat("h")
			}
			st = b.State()
			u.nav.sync(&st)
			if u.nav.chatTextKey = u.chatTextKeys(&st); hasCtrlEq(u.nav.keyFilters(&st)) {
				t.Errorf("%s: Ctrl+= taken from the TUI", name)
			}
		}
	}

	// A shell tab: Ctrl+= is the pane's.
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w1", "")
	if u.nav.chatTextKey = u.chatTextKeys(&st); hasCtrlEq(u.nav.keyFilters(&st)) {
		t.Error("Ctrl+= taken from a shell")
	}
}

func hasCtrlEq(fs []key.Filter) bool {
	return slices.ContainsFunc(fs, func(f key.Filter) bool { return f.Name == "=" && f.Required == key.ModCtrl })
}

// TestChatSizePresets: every preset binds the chat size keys to chords
// nothing else in it uses: Ctrl in aide and conventional, Cmd in mac.
func TestChatSizePresets(t *testing.T) {
	for _, p := range config.Presets {
		b := config.Preset(p)
		mod := key.ModCtrl
		if p == "mac" {
			mod = key.ModSuper
			if runtime.GOOS == "darwin" {
				mod = key.ModCommand
			}
		}
		for a, k := range map[string]key.Name{"chat_text_bigger": "=", "chat_text_smaller": "-", "chat_text_reset": "0"} {
			if got := b.Action(key.Event{Name: k, Modifiers: mod}); got != a {
				t.Errorf("%s: %v+%s runs %q, want %s", p, mod, k, got, a)
			}
		}
		if got := b.Action(key.Event{Name: "+", Modifiers: mod | key.ModShift}); got != "chat_text_bigger" {
			t.Errorf("%s: Shift++ runs %q", p, got)
		}
	}
}
