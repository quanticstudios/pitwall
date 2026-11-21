package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTimelineWindowShots writes PNGs of the whole window to
// $PITWALL_TIMELINE_SHOTS: a Claude chat tab in each tool call mode with
// the header's Tools button, a Codex pane chat stopped on a usage limit
// its TUI wrote, and an Antigravity chat tab with code at 18 beside text
// at 13. It skips without it.
func TestTimelineWindowShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TIMELINE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TIMELINE_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	reset := time.Now().Add(95 * time.Minute).Truncate(time.Minute)
	limit := "■ You've hit your usage limit. Upgrade to Pro, or try again at " + reset.Format("15:04") + "."
	size := image.Pt(1400, 1100)
	for _, c := range []struct {
		name, mode string
		prov       model.Provider
		pane       bool
		code       float64
	}{
		{"window-claude-all", config.ToolsAll, model.ProviderClaude, false, 0},
		{"window-claude-failed", config.ToolsFailed, model.ProviderClaude, false, 0},
		{"window-claude-none", config.ToolsNone, model.ProviderClaude, false, 0},
		{"window-claude-zen", config.ToolsZen, model.ProviderClaude, false, 0},
		{"window-codex-pane-limit", config.ToolsAll, model.ProviderCodex, true, 0},
		{"window-antigravity-code-18", config.ToolsAll, model.ProviderAntigravity, false, 18},
	} {
		b := &limitBackend{FakeBackend: NewFakeBackend(), snap: sizeChat()}
		b.snap.Provider = c.prov
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Chat text size", Path: t.TempDir(), Chat: "c1", Branch: "chat-font-size"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: c.prov, State: session.StateRunning, Title: "Chat text size"}}
		ws := "wc"
		if c.pane {
			i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
			b.st.Panes[i].Provider, b.st.Panes[i].Transcript = c.prov, "/tmp/s.jsonl"
			ws = "w6"
			// The turn the limit stopped: no approval waits, the queue is empty.
			b.snap.SessionState, b.snap.Running, b.snap.PendingApprovals, b.snap.QueuedInputs = session.StateReady, false, nil, nil
			b.snap.Content = b.snap.Content[:len(b.snap.Content)-2]
			b.st.Activities = append(b.st.Activities, model.Activity{PaneID: "h", WorkspaceID: "w6", Provider: c.prov, State: model.StateError, Detail: limit})
		}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize, u.cfg.Font.ChatCodeSize = 13, 13, c.code
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		u.cfg.Chat.ToolCalls, u.cfg.Chat.Width = c.mode, config.ChatComfortable
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, ws, "")
		if c.pane {
			u.nav.setFocus("h")
		}
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(size), Now: time.Now()})
			r.Frame(&ops)
			time.Sleep(100 * time.Millisecond)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, out)
		f.Close()
	}
}
