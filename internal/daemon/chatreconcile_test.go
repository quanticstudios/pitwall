package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// cutChat stores chat-1, in tab ws-1 of f's saved state, as the recorded
// session fixture left it after delta upto: a daemon stopped there. It
// returns events.jsonl as written.
func cutChat(t *testing.T, root string, f *fakes, fixture string, upto int64) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "session", "testdata", "session-deltas", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Deltas []session.Delta }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	for _, d := range fx.Deltas[:upto] {
		d.SessionID = "chat-1"
		if d.Snapshot != nil {
			d.Snapshot.SessionID = "chat-1"
		}
		b, _ := json.Marshal(d)
		log.Write(append(b, '\n'))
	}
	dir := filepath.Join(root, "chat-1")
	os.MkdirAll(dir, 0o700)
	cfg, _ := json.Marshal(session.Config{Format: 1, ID: "chat-1", Provider: model.ProviderClaude, Workspace: "ws-1", Resume: "r-1"})
	os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600)
	os.WriteFile(filepath.Join(dir, "events.jsonl"), log.Bytes(), 0o600)
	f.saved.Sessions = []model.Session{{ID: "s1", Order: []string{"ws-1"}}}
	f.saved.Workspaces = []model.Workspace{{ID: "ws-1", SessionID: "s1", Path: t.TempDir(), Chat: "chat-1", Label: "chat-1"}}
	return log.Bytes()
}

// readChat is chat-1's snapshot as ChatRead returns it.
func readChat(t *testing.T, c *testClient) session.Snapshot {
	t.Helper()
	var s session.Snapshot
	if err := json.Unmarshal(chatCall[proto.ChatDeltas](c, proto.ChatRead{Chat: "chat-1"}).Snapshot, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func systemLines(s session.Snapshot) []string {
	var out []string
	for _, it := range s.Content {
		if it.Kind == session.KindSystem {
			out = append(out, it.Text)
		}
	}
	return out
}

// TestChatReconcile: a chat stored mid-turn, with a tool call running or a
// request pending, loads with nothing running or answerable, a line that
// says why, and its log only appended to. One stored after its turn ended
// loads as it was. No provider starts.
func TestChatReconcile(t *testing.T) {
	const cut, expired = "pitwall restarted during this turn", "Expired: pitwall restarted before anyone answered"
	for _, c := range []struct {
		name    string
		fixture string
		upto    int64
		lines   []string
	}{
		{"tool running", "tool-calls", 13, []string{cut}},
		{"approval pending", "approval-and-input", 12, []string{cut, expired}},
		{"input pending", "approval-and-input", 18, []string{cut, expired}},
		{"turn ended", "tool-calls", 22, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			f := &fakes{statsCalls: map[string]int{}}
			stored := cutChat(t, root, f, c.fixture, c.upto)
			stub := &stubProvider{}
			sock, stop := runWith(t, f, func(o *Options) {
				o.Chats = root
				o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: stub}
			})
			defer stop()
			gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
			waitChat(gui, "chat back", func(s model.State) bool { return chatOf(s).ID == "chat-1" })

			s := readChat(t, gui)
			for _, tr := range s.Turns {
				if tr.State == session.TurnRunning || tr.CompletedAt == nil {
					t.Fatalf("turn %+v after the load", tr)
				}
			}
			for _, it := range s.Content {
				if it.Tool != nil && it.Tool.Status == "running" {
					t.Fatalf("tool %s still running", it.ID)
				}
			}
			if len(s.PendingApprovals)+len(s.PendingUserInputs) > 0 || s.Busy() {
				t.Fatalf("pending %v %v, state %s", s.PendingApprovals, s.PendingUserInputs, s.SessionState)
			}
			if got := systemLines(s); !slices.Equal(got, c.lines) {
				t.Fatalf("system lines %q, want %q", got, c.lines)
			}
			after, _ := os.ReadFile(filepath.Join(root, "chat-1", "events.jsonl"))
			if !bytes.HasPrefix(after, stored) || (c.lines == nil) != (len(after) == len(stored)) {
				t.Fatalf("events.jsonl went from %d to %d bytes; want the stored lines kept and new ones only after a cut", len(stored), len(after))
			}
			stub.mu.Lock()
			defer stub.mu.Unlock()
			if len(stub.drivers) != 0 {
				t.Fatalf("%d providers started with the continue setting off", len(stub.drivers))
			}
		})
	}
}

// TestChatContinueAfterRestart: with chat_continue_after_restart on, each
// restart that cuts a turn off resumes the provider and sends the
// continue, three times in five minutes at most, counted across restarts:
// the fourth waits for the first to age out, and the fifth is refused.
func TestChatContinueAfterRestart(t *testing.T) {
	root := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	cutChat(t, root, f, "tool-calls", 13)
	t0 := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old := continueNow
	t.Cleanup(func() { continueNow = old })
	stub := &stubProvider{}
	sends := func() (n int) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		for _, d := range stub.drivers {
			for _, c := range d.got() {
				if c == "send "+restartContinue {
					n++
				}
			}
		}
		return n
	}
	for i, step := range []struct {
		at    time.Duration
		sends int
	}{{0, 1}, {time.Minute, 2}, {2 * time.Minute, 3}, {5 * time.Minute, 4}, {5*time.Minute + 30*time.Second, 4}} {
		continueNow = func() time.Time { return t0.Add(step.at) }
		sock, stop := runWith(t, f, func(o *Options) {
			o.Chats = root
			o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: stub}
			o.ChatContinue = func() bool { return true }
		})
		gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
		waitChat(gui, "chat back", func(s model.State) bool { return chatOf(s).ID == "chat-1" })
		readChat(t, gui)
		s := readChat(t, gui) // after the continue, which runs once the first read is done
		if got := sends(); got != step.sends {
			t.Fatalf("restart %d at +%v: %d continues sent, want %d", i, step.at, got, step.sends)
		}
		limited := slices.ContainsFunc(systemLines(s), func(l string) bool { return strings.HasPrefix(l, "Not continued: ") })
		if limited != (i == 4) {
			t.Fatalf("restart %d: system lines %q", i, systemLines(s))
		}
		stop()
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for _, d := range stub.drivers {
		if d.cfg.Resume != "r-1" {
			t.Fatalf("a continue started a new session, not r-1: %+v", d.cfg)
		}
	}
}

// TestChatExitReconciles: a process that exits on its own mid-turn leaves
// no tool call running and says its request expired.
func TestChatExitReconciles(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderCodex: prov}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderCodex, Dir: t.TempDir(), Prompt: "go"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	drv := prov.driver(t)
	drv.event(session.Event{Type: session.EventContent, ItemID: "b1", Kind: session.KindToolCall, Tool: &session.ToolCall{CallID: "b1", ToolName: "Bash", Status: "running"}})
	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "r1", RequestType: session.RequestCommand})
	waitChat(gui, "approval pending", func(s model.State) bool { return chatOf(s).Pending == 1 })
	code := 1
	drv.event(session.Event{Type: session.EventExited, ExitCode: &code})
	waitChat(gui, "exited", func(s model.State) bool { return chatOf(s).State == session.StateError })

	var s session.Snapshot
	if err := json.Unmarshal(chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: r.Chat}).Snapshot, &s); err != nil {
		t.Fatal(err)
	}
	if tool := s.Content[slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == "b1" })].Tool; tool.Status != "failed" {
		t.Fatalf("tool %+v after the exit", tool)
	}
	if got, want := systemLines(s), []string{"Expired: Codex exited before anyone answered"}; !slices.Equal(got, want) {
		t.Fatalf("system lines %q, want %q", got, want)
	}
	if s.Turns[0].State != session.TurnFailed {
		t.Fatalf("turn %+v, want failed by the exit", s.Turns[0])
	}
}
