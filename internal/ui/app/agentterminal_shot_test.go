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

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type agentChatBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b agentChatBackend) Chat(string) (session.Snapshot, bool) { return b.snap, true }

// TestAgentTerminalShots renders headless PNGs of an Antigravity chat tab's
// header with Terminal, a Codex chat's dialog before it stops a turn for
// the terminal, and a Codex chat's terminal view, into
// $PITWALL_TERMINAL_SHOTS; it skips when unset.
func TestAgentTerminalShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TERMINAL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TERMINAL_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	at := func(s int) string { return time.Date(2026, 11, 23, 17, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	for _, tc := range []struct {
		name     string
		prov     model.Provider
		state    string
		terminal []string // the terminal view's command, nil in the chat
		ask      bool     // press Terminal mid-turn
	}{
		{name: "agy-chat-terminal-button", prov: model.ProviderAntigravity, state: session.StateReady},
		{name: "codex-chat-stop-dialog", prov: model.ProviderCodex, state: session.StateRunning, ask: true},
		{name: "codex-chat-terminal-view", prov: model.ProviderCodex, state: session.StateReady,
			terminal: []string{"/usr/bin/codex", "resume", "01a2092d-8bc0-7a13-b2e5-c23146f61ac2", "--no-daemon", "-a", "on-request", "-s", "workspace-write"}},
	} {
		turn, prov := "t1", tc.prov
		b := agentChatBackend{NewFakeBackend(), session.Snapshot{SessionID: "c1", Provider: tc.prov, SessionState: tc.state, Running: tc.state == session.StateRunning,
			Content: []session.Item{
				{ID: "p1", Kind: session.KindUserPrompt, Text: "Reply with the word sun spelled backwards", TurnID: &turn, Provider: &prov, CreatedAt: at(0)},
				{ID: "r1", Kind: session.KindAssistantText, Text: "nus", TurnID: &turn, Provider: &prov, CreatedAt: at(4), UpdatedAt: at(4)},
			}}}
		path := t.TempDir()
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Spell it backwards", Path: path, Branch: "main", Chat: "c1"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: tc.prov, State: tc.state, Title: "Spell it backwards"}}
		if tc.terminal != nil {
			w := &b.st.Workspaces[len(b.st.Workspaces)-1]
			w.Tabs, w.ActiveTab = []model.Tab{{ID: "tt", Layout: &layout.Node{Pane: "tp"}}}, "tt"
			b.st.Panes = append(b.st.Panes, model.Pane{ID: "tp", WorkspaceID: "wc", Cwd: path, Cmd: tc.terminal})
			b.st.Chats[0].Terminal = "tp"
		}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wc", "")
		if tc.ask {
			u.toggleChatTerminal(&st, st.Workspace("wc"))
		}

		size := image.Pt(1100, 640)
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
		f, err := os.Create(filepath.Join(dir, tc.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
