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
	"github.com/quanticstudios/pitwall/internal/session/acp"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
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

// TestAgentChatTerminal flips Codex and agy chats to their TUI and back:
// the TUI resumes the session the driver held, Esc and two Ctrl+C quit
// it, and the turn it ran shows in the chat before the driver resumes.
func TestAgentChatTerminal(t *testing.T) {
	codexHome, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("HOME", home)
	t.Setenv("PITWALL_CODEX_COMMAND", "/opt/stub/codex")
	const thread = "01a2092d-8bc0-7a13-b2e5-c23146f61ac2"
	rollout := filepath.Join(codexHome, "sessions", "2026", "11", "23", "rollout-2026-11-23T17-11-50-"+thread+".jsonl")
	transcript := filepath.Join(home, ".gemini", "antigravity-cli", "brain", thread, ".system_generated", "logs", "transcript_full.jsonl")
	for _, tc := range []struct {
		prov      model.Provider
		log       string
		chat, tui string // the transcript's lines from the chat's turn, then from the TUI's
		cmd       []string
	}{
		{model.ProviderCodex, rollout,
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n" + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}` + "\n",
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}` + "\n" +
				`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t2","item":{"type":"UserMessage","id":"u2","content":[{"type":"text","text":"from the tui"}]}}}` + "\n" +
				`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t2"}}` + "\n",
			[]string{"/opt/stub/codex", "resume", thread, "--no-daemon", "-a", "on-request", "-s", "workspace-write"}},
		{model.ProviderAntigravity, transcript,
			`{"step_index":0,"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nFix it\n</USER_REQUEST>"}` + "\n",
			`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"fixed"}` + "\n" +
				`{"step_index":2,"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nfrom the tui\n</USER_REQUEST>"}` + "\n",
			[]string{"agy", "--conversation", thread, "--settings", `{"agentMode":""}`}},
	} {
		t.Run(string(tc.prov), func(t *testing.T) {
			f := &fakes{statsCalls: map[string]int{}}
			prov := &stubProvider{}
			sock, stop := runWith(t, f, func(o *Options) {
				o.Providers = map[model.Provider]session.Provider{tc.prov: prov}
			})
			defer stop()
			gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
			gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
			r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: tc.prov, Dir: t.TempDir(), Prompt: "Fix it"})
			if r.Code != "" {
				t.Fatalf("new: %+v", r)
			}
			id, drv := r.Chat, prov.driver(t)
			terminal := func(on bool) proto.ChatResult {
				return chatCall[proto.ChatResult](gui, proto.ChatTerminal{Chat: id, On: on})
			}
			// The chat's turn writes the transcript; the TUI resumes it.
			if err := os.MkdirAll(filepath.Dir(tc.log), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tc.log, []byte(tc.chat), 0o600); err != nil {
				t.Fatal(err)
			}
			drv.event(session.Event{Type: session.EventResumeID, ResumeID: thread})
			// An approval after the id: once it shows, the loop has the id too.
			drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "r1", RequestType: session.RequestCommand, Detail: "make"})
			waitChat(gui, "approval pending", func(s model.State) bool { return chatOf(s).Pending == 1 })
			if r := terminal(true); r.Code != "" {
				t.Fatalf("terminal: %+v", r)
			}
			waitChat(gui, "terminal view", func(s model.State) bool { return chatOf(s).Terminal != "" })
			f.mu.Lock()
			p := f.panes[len(f.panes)-1]
			f.mu.Unlock()
			if !slices.Equal(p.cfg.Cmd, tc.cmd) {
				t.Fatalf("terminal runs %q, want %q", p.cfg.Cmd, tc.cmd)
			}

			// The TUI runs a turn, then quits on Esc and two Ctrl+C.
			lf, err := os.OpenFile(tc.log, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			lf.WriteString(tc.tui)
			lf.Close()
			p.mu.Lock()
			p.wrote = func() {
				if string(p.input) == "\x1b\x03\x03" && !p.closed {
					p.closed = true
					close(p.done)
				}
			}
			p.mu.Unlock()
			if r := terminal(false); r.Code != "" {
				t.Fatalf("back: %+v", r)
			}
			waitChat(gui, "chat view", func(s model.State) bool { return chatOf(s).Terminal == "" })
			waitUntil(t, "driver resumed", func() bool { prov.mu.Lock(); defer prov.mu.Unlock(); return len(prov.drivers) == 2 })
			full := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id})
			var snap session.Snapshot
			if err := json.Unmarshal(full.Snapshot, &snap); err != nil {
				t.Fatal(err)
			}
			var prompts []string
			for _, it := range snap.Content {
				if it.Kind == session.KindUserPrompt {
					prompts = append(prompts, it.Text)
				}
			}
			if !slices.Equal(prompts, []string{"Fix it", "from the tui"}) || len(snap.Turns) != 2 {
				t.Fatalf("prompts %q, turns %+v", prompts, snap.Turns)
			}
			if last := snap.Content[len(snap.Content)-1]; last.Kind != session.KindSystem || last.Text != "Back from the terminal" {
				t.Fatalf("last item %+v", last)
			}
		})
	}
}

// TestTerminalCmd: a chat with no turn yet creates its session under the
// id the driver resumes afterwards, and the TUI keeps the chat's mode,
// default included, so it does not start in its own default.
func TestTerminalCmd(t *testing.T) {
	c := &chat{cfg: session.Config{Model: "opus", Mode: "default"}}
	cmd := claudeTerminal(c, "claude")
	if len(cmd) != 7 || cmd[1] != "--session-id" || cmd[2] != c.cfg.Resume || c.cfg.Resume == "" || cmd[3] != "--model" || cmd[5] != "--permission-mode" || cmd[6] != "default" {
		t.Fatalf("cmd %q, resume %q", cmd, c.cfg.Resume)
	}
	c = &chat{cfg: session.Config{}}
	if cmd := claudeTerminal(c, "claude"); !slices.Equal(cmd[3:], []string{"--permission-mode", "default"}) {
		t.Fatalf("no mode set: cmd %q", cmd)
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

// TestAgentTerminalCmd: Codex resumes its thread outside its shared server
// with the driver's approval policy and sandbox, agy its conversation in
// the chat's mode, its own default asking as the chat's does.
func TestAgentTerminalCmd(t *testing.T) {
	t.Setenv("PITWALL_CODEX_COMMAND", "/opt/codex")
	turn := []session.TurnSummary{{ID: "t1"}}
	for _, tc := range []struct {
		cfg  session.Config
		prov session.Provider
		want []string
	}{
		{session.Config{Provider: model.ProviderCodex, Resume: "019a"}, nil,
			[]string{"/opt/codex", "resume", "019a", "--no-daemon", "-a", "on-request", "-s", "workspace-write"}},
		{session.Config{Provider: model.ProviderCodex, Resume: "019a", Mode: "plan", Model: "gpt-6", Effort: "low"}, nil,
			[]string{"/opt/codex", "resume", "019a", "--no-daemon", "-a", "on-request", "-s", "read-only", "-m", "gpt-6", "-c", `model_reasoning_effort="low"`}},
		{session.Config{Provider: model.ProviderCodex, Resume: "019a", Mode: "bypassPermissions"}, nil,
			[]string{"/opt/codex", "resume", "019a", "--no-daemon", "-a", "never", "-s", "workspace-write"}},
		{session.Config{Provider: model.ProviderAntigravity, Resume: "71b2"}, agyACP{acp.Provider{Command: "/opt/agy"}},
			[]string{"/opt/agy", "--conversation", "71b2", "--settings", `{"agentMode":""}`}},
		{session.Config{Provider: model.ProviderAntigravity, Resume: "71b2", Mode: "default", Model: "gemini-3.8-flash", Effort: "low"}, nil,
			[]string{"agy", "--conversation", "71b2", "--model", "gemini-3.8-flash", "--effort", "low", "--settings", `{"agentMode":""}`}},
		{session.Config{Provider: model.ProviderAntigravity, Resume: "71b2", Mode: "accept-edits"}, nil,
			[]string{"agy", "--conversation", "71b2", "--mode", "accept-edits"}},
	} {
		c := &chat{cfg: tc.cfg, prov: tc.prov, snap: session.Snapshot{Turns: turn}}
		if got, err := terminalCmd(c); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%+v: %q, %v; want %q", tc.cfg, got, err, tc.want)
		}
	}
	// Nothing to resume before the first turn, after a fork or rewind,
	// and no TUI for an ACP agent.
	for _, tc := range []struct {
		c    *chat
		code string
	}{
		{&chat{cfg: session.Config{Provider: model.ProviderCodex, Resume: "019a"}}, "busy"},
		{&chat{cfg: session.Config{Provider: model.ProviderAntigravity}, snap: session.Snapshot{Turns: turn}}, "busy"},
		{&chat{cfg: session.Config{Provider: model.ProviderCodex, Resume: "019a", From: &session.Origin{Resume: "019a", Fork: true}}, snap: session.Snapshot{Turns: turn}}, "busy"},
		{&chat{cfg: session.Config{Provider: "acp:grok", Resume: "s1"}, snap: session.Snapshot{Turns: turn}}, "unsupported"},
	} {
		if _, err := terminalCmd(tc.c); err == nil || err.(chatError).code != tc.code {
			t.Errorf("%+v: %v, want %s", tc.c.cfg, err, tc.code)
		}
	}
}

// TestTerminalLog: the transcript is where each agent keeps the session,
// under the CODEX_HOME or HOME the chat runs with.
func TestTerminalLog(t *testing.T) {
	codexHome, home := t.TempDir(), t.TempDir()
	rollout := filepath.Join(codexHome, "sessions", "2026", "11", "23", "rollout-2026-11-23T17-11-50-01a2092d-8bc0-7a13-b2e5-c23146f61ac2.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := terminalLog(session.Config{Provider: model.ProviderCodex, Resume: "01a2092d-8bc0-7a13-b2e5-c23146f61ac2", Env: []string{"CODEX_HOME=" + codexHome}}); got != rollout {
		t.Errorf("codex: %q, want %q", got, rollout)
	}
	want := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "71b2", ".system_generated", "logs", "transcript_full.jsonl")
	if got := terminalLog(session.Config{Provider: model.ProviderAntigravity, Resume: "71b2", Env: []string{"HOME=" + home}}); got != want {
		t.Errorf("agy: %q, want %q", got, want)
	}
	if got := terminalLog(session.Config{Provider: model.ProviderClaude, Resume: "7f3c"}); got != "" {
		t.Errorf("claude: %q", got)
	}
}

// TestReadTurns: what the TUI wrote before its first turn closes the
// chat's stopped turn and stays out; a turn the TUI left running ends
// interrupted.
func TestReadTurns(t *testing.T) {
	codexLog := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t2","item":{"type":"UserMessage","id":"u2","content":[{"type":"text","text":"from the tui"}]}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t2","item":{"type":"AgentMessage","id":"a2","content":[{"type":"Text","text":"done"}]}}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t2"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t3"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t3","item":{"type":"UserMessage","id":"u3","content":[{"type":"text","text":"left running"}]}}}`,
		`{"type":"event_msg","payload":{"type":"task_started"`, // cut off by the pane closing
	}, "\n")
	s := snapOf(t, readTurns(strings.NewReader(codexLog), codex.NewRollout()))
	if len(s.Turns) != 2 || s.Turns[0].ID != "t2" || s.Turns[0].State != session.TurnCompleted || s.Turns[1].ID != "t3" || s.Turns[1].State != session.TurnInterrupted {
		t.Fatalf("codex turns %+v", s.Turns)
	}
	if got := itemTexts(s); !slices.Equal(got, []string{"from the tui", "done", "left running"}) {
		t.Fatalf("codex items %q", got)
	}
	agyLog := strings.Join([]string{
		`{"step_index":2,"type":"PLANNER_RESPONSE","status":"DONE","content":"the chat's turn"}`,
		`{"step_index":3,"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>\nfrom the tui\n</USER_REQUEST>"}`,
		`{"step_index":4,"type":"PLANNER_RESPONSE","status":"DONE","content":"noom"}`,
	}, "\n")
	s = snapOf(t, readTurns(strings.NewReader(agyLog), antigravity.NewTranscript()))
	if len(s.Turns) != 1 || s.Turns[0].ID != "step-3" || itemTexts(s)[0] != "from the tui" || itemTexts(s)[1] != "noom" {
		t.Fatalf("agy turns %+v, items %q", s.Turns, itemTexts(s))
	}
	if got := readTurns(strings.NewReader(""), codex.NewRollout()); len(got) != 0 {
		t.Fatalf("nothing written: %+v", got)
	}
}

func snapOf(t *testing.T, es []session.Event) session.Snapshot {
	t.Helper()
	var s session.Snapshot
	var seq int64
	for _, e := range es {
		for _, dl := range session.Reduce(s, e) {
			seq++
			dl.Version, dl.Seq = session.DeltaVersion, seq
			n, err := session.Apply(&s, seq-1, dl)
			if err != nil {
				t.Fatalf("%s: %v", e.Type, err)
			}
			s = n
		}
	}
	return s
}

func itemTexts(s session.Snapshot) []string {
	var out []string
	for _, it := range s.Content {
		out = append(out, it.Text)
	}
	return out
}
