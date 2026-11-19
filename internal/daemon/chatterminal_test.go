package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// TestChatTerminal flips a Claude chat to its TUI mid-turn and back, then
// to the TUI again, whose claude exits on its own.
func TestChatTerminal(t *testing.T) {
	t.Setenv(claude.CommandEnv, "/opt/stub/claude")
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: prov}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
	dir := t.TempDir()
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: dir, Prompt: "Fix it", Mode: "acceptEdits"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	id, drv := r.Chat, prov.driver(t)
	drv.event(session.Event{Type: session.EventResumeID, ResumeID: "7f3c2a10-0000-4000-8000-000000000001"})
	// An approval after the id: once it shows, the loop has the id too.
	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "r1", RequestType: session.RequestCommand, Detail: "make"})
	waitChat(gui, "approval pending", func(s model.State) bool { return chatOf(s).Pending == 1 })

	terminal := func(on bool) {
		t.Helper()
		if r := chatCall[proto.ChatResult](gui, proto.ChatTerminal{Chat: id, On: on}); r.Code != "" {
			t.Fatalf("terminal %v: %+v", on, r)
		}
	}
	lastPane := func() *fakePane {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.panes[len(f.panes)-1]
	}

	// To the terminal, mid-turn: the turn stops and the driver closes
	// before claude --resume starts on the same session.
	terminal(true)
	st := waitChat(gui, "terminal view", func(s model.State) bool { return chatOf(s).Terminal != "" })
	if got := drv.got(); !slices.Contains(got, "interrupt") {
		t.Fatalf("driver calls %q, want an interrupt", got)
	}
	drv.mu.Lock()
	closed := drv.closed
	drv.mu.Unlock()
	if !closed {
		t.Fatal("driver still open with the terminal running")
	}
	p := lastPane()
	want := []string{"/opt/stub/claude", "--resume", "7f3c2a10-0000-4000-8000-000000000001", "--permission-mode", "acceptEdits"}
	if !slices.Equal(p.cfg.Cmd, want) || p.cfg.Cwd != dir {
		t.Fatalf("terminal runs %q in %s, want %q in %s", p.cfg.Cmd, p.cfg.Cwd, want, dir)
	}
	if slices.Contains(p.cfg.Env, "PITWALL_PANE="+id) {
		t.Fatal("terminal claude reports as the chat")
	}
	ws := st.Workspaces[slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.Chat == id })]
	if len(ws.Tabs) != 1 || ws.Tabs[0].Layout.Pane != chatOf(st).Terminal {
		t.Fatalf("chat tab %+v, terminal %s", ws.Tabs, chatOf(st).Terminal)
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: id, Text: "hi"}); r.Code != "busy" {
		t.Fatalf("send while the terminal holds the session: %+v", r)
	}

	// Back: two Ctrl+C quit claude, and the driver resumes.
	p.mu.Lock()
	p.wrote = func() {
		if strings.Count(string(p.input), "\x03") == 2 && !p.closed {
			p.closed = true
			close(p.done)
		}
	}
	p.mu.Unlock()
	terminal(false)
	st = waitChat(gui, "chat view", func(s model.State) bool { return chatOf(s).Terminal == "" })
	if ws := st.Workspace(ws.ID); ws == nil || len(ws.Tabs) != 0 {
		t.Fatalf("chat tab after the terminal: %+v", ws)
	}
	waitUntil(t, "driver resumed", func() bool { prov.mu.Lock(); defer prov.mu.Unlock(); return len(prov.drivers) == 2 })

	// The terminal's claude exiting on its own brings the chat back too.
	terminal(true)
	waitChat(gui, "terminal again", func(s model.State) bool { return chatOf(s).Terminal != "" })
	lastPane().Close()
	st = waitChat(gui, "chat after exit", func(s model.State) bool { return chatOf(s).Terminal == "" })
	if st.Workspace(ws.ID) == nil {
		t.Fatal("the chat tab closed with its terminal")
	}
	waitUntil(t, "driver resumed again", func() bool { prov.mu.Lock(); defer prov.mu.Unlock(); return len(prov.drivers) == 3 })

	full := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id})
	var snap session.Snapshot
	if err := json.Unmarshal(full.Snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	var sys []string
	for _, it := range snap.Content {
		if it.Kind == session.KindSystem {
			sys = append(sys, strings.Fields(it.Text)[0])
		}
	}
	if want := []string{"Interrupted:", "Expired:", "Continued", "Back", "Continued", "Back"}; !slices.Equal(sys, want) {
		t.Fatalf("system items start %q, want %q", sys, want)
	}
}

// TestTerminalCmd: a chat with no turn yet creates its session under the
// id the driver resumes afterwards.
func TestTerminalCmd(t *testing.T) {
	c := &chat{cfg: session.Config{Model: "opus", Mode: "default"}}
	cmd := terminalCmd(c, "claude")
	if len(cmd) != 5 || cmd[1] != "--session-id" || cmd[2] != c.cfg.Resume || c.cfg.Resume == "" || cmd[3] != "--model" {
		t.Fatalf("cmd %q, resume %q", cmd, c.cfg.Resume)
	}
}

// TestChatTerminalAfterHandoff: a chat tab that comes over in a handoff
// with its terminal view keeps it, so the driver does not start beside
// that claude; a save leaves the view out, so a restart does not start it.
func TestChatTerminalAfterHandoff(t *testing.T) {
	root := t.TempDir()
	cfg, _ := json.Marshal(session.Config{Format: 1, ID: "c1", Provider: model.ProviderClaude, Workspace: "wc", Resume: "7f3c"})
	if err := os.MkdirAll(filepath.Join(root, "c1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c1", "config.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{o: Options{Chats: root}, chats: map[string]*chat{}, st: model.State{
		Workspaces: []model.Workspace{{ID: "wc", Chat: "c1", ActiveTab: "tt", Tabs: []model.Tab{{ID: "tt", Layout: &layout.Node{Pane: "tp"}}}}},
		Panes:      []model.Pane{{ID: "tp", WorkspaceID: "wc"}, {ID: "p1", WorkspaceID: "w1"}},
	}}
	d.loadChats()
	c := d.chats["c1"]
	t.Cleanup(func() { close(c.quit) })
	if c.term != "tp" || len(d.st.Chats) != 1 || d.st.Chats[0].Terminal != "tp" || d.chatOfTerminal("tp") != c {
		t.Fatalf("term %q, chats %+v", c.term, d.st.Chats)
	}
	var err error
	c.run(func() { err = d.chatStart(c) })
	if r, ok := err.(chatError); !ok || r.code != "busy" {
		t.Fatalf("driver start beside the terminal: %v", err)
	}
	s := d.storedState()
	if w := s.Workspaces[0]; len(w.Tabs) != 0 || w.ActiveTab != "" || len(s.Panes) != 1 || s.Panes[0].ID != "p1" {
		t.Fatalf("stored %+v, panes %+v", w, s.Panes)
	}
}

// TestChatTerminalAfterRewind: until Claude names a rewound or forked
// session, the terminal view would resume the old one, so it waits.
func TestChatTerminalAfterRewind(t *testing.T) {
	c := &chat{cfg: session.Config{Provider: model.ProviderClaude, Resume: "7f3c", From: &session.Origin{Resume: "7f3c", At: "u1", Fork: true}}}
	err := (&Daemon{}).chatTerminal(context.Background(), c, true)
	if r, ok := err.(chatError); !ok || r.code != "busy" || !strings.HasPrefix(r.msg, "Send a message first") || c.term != "" {
		t.Fatalf("terminal after a rewind: %v", err)
	}
}
