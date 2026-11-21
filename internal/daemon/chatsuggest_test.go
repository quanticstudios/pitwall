package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// fakeAgent writes an executable shell script and returns its path.
func fakeAgent(t *testing.T, name, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// finished is a snapshot whose turn t1 completed: a prompt and a reply.
func finished(prompt, reply string) session.Snapshot {
	turn := "t1"
	done := "2026-11-21T10:00:00Z"
	return session.Snapshot{
		Content: []session.Item{
			{ID: "u1", Kind: session.KindUserPrompt, Text: prompt, TurnID: &turn},
			{ID: "x1", Kind: session.KindToolCall, Text: "TOOLCALL-XYZ", TurnID: &turn},
			{ID: "a1", Kind: session.KindAssistantText, Text: reply, TurnID: &turn},
		},
		Turns: []session.TurnSummary{{ID: turn, State: session.TurnCompleted, CompletedAt: &done}},
	}
}

// suggestPane makes pane id of remoteDaemon an idle chat showing snap.
func suggestPane(t *testing.T, d *Daemon, id string, snap session.Snapshot) {
	t.Helper()
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: paneProvider(d, id), Payload: []byte(model.StateCompleted)}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	d.st.Panes[i].Transcript = filepath.Join(t.TempDir(), "s.jsonl")
	d.st.Panes[i].Cwd = t.TempDir()
	d.mu.Unlock()
	pc, _, ok := d.paneChatOf(id)
	if !ok {
		t.Fatalf("pane %s is no chat", id)
	}
	pc.mu.Lock()
	pc.snap = snap
	pc.mu.Unlock()
}

func paneProvider(d *Daemon, id string) model.Provider {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider
}

func TestChatSuggestPane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agents are shell scripts")
	}
	d, _, _ := remoteDaemon(t)
	var on atomic.Bool
	d.o.SuggestNext = on.Load
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stdinFile := filepath.Join(dir, "stdin")
	envFile := filepath.Join(dir, "env")
	// Each fake records its arguments, its stdin and whether a pitwall
	// pane variable reached it, then answers.
	record := `printf '%s\n' "$@" > ` + argsFile + `; cat > ` + stdinFile + `; printf '%s' "${PITWALL_PANE:-none}" > ` + envFile + "\n"
	t.Setenv(claude.CommandEnv, fakeAgent(t, "claude", record+`printf '"Now run the tests"\n'`))
	t.Setenv("PITWALL_PANE", "outer") // the daemon's own, as a daemon started from a pane has
	ask := func(chat, turn string) proto.ChatSuggestResult {
		return d.chatSuggest(context.Background(), proto.ChatSuggest{Chat: chat, Turn: turn})
	}

	suggestPane(t, d, "a", finished("Fix the flaky login test", "Fixed it: the test raced the session cookie. All green locally."))
	if r := ask("a", "t1"); r.Code != "off" || r.Text != "" {
		t.Fatalf("off: %+v", r)
	}
	on.Store(true)
	r := ask("a", "t1")
	if r.Err != "" || r.Text != "Now run the tests" || r.Chat != "a" || r.Turn != "t1" {
		t.Fatalf("suggestion: %+v", r)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"-p", "--model\nhaiku", "--no-session-persistence", "--tools\n\n", "--strict-mcp-config", `{"disableAllHooks":true}`} {
		if !strings.Contains(string(args), want) {
			t.Errorf("claude args %q lack %q", args, want)
		}
	}
	in, _ := os.ReadFile(stdinFile)
	for _, want := range []string{"User: Fix the flaky login test", "Assistant: Fixed it", suggestPrompt} {
		if !strings.Contains(string(in), want) {
			t.Errorf("prompt %q lacks %q", in, want)
		}
	}
	if strings.Contains(string(in), "TOOLCALL-XYZ") {
		t.Errorf("the prompt carries a tool call: %q", in)
	}
	if env, _ := os.ReadFile(envFile); string(env) != "none" {
		t.Errorf("PITWALL_PANE reached the agent: %q", env)
	}

	// The same turn again runs nothing: the answer is the first one's.
	os.Remove(argsFile)
	if r := ask("a", "t1"); r.Text != "Now run the tests" {
		t.Fatalf("again: %+v", r)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatal("a second ask for one turn ran the agent again")
	}

	// A turn that is not the newest finished one, or none, is stale.
	if r := ask("a", "t0"); r.Code != "stale" {
		t.Fatalf("an old turn: %+v", r)
	}
	if r := ask("a", ""); r.Code != "stale" {
		t.Fatalf("no turn: %+v", r)
	}

	// Working: refused.
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)}); err != nil {
		t.Fatal(err)
	}
	if r := ask("a", "t1"); r.Code != "busy" {
		t.Fatalf("working: %+v", r)
	}

	// A guarded tab: never.
	suggestPane(t, d, "a", finished("x", "y"))
	d.mu.Lock()
	d.st.Panes[0].Env = append(d.st.Panes[0].Env, guard.Env+"=1")
	d.mu.Unlock()
	if r := ask("a", "t1"); r.Code != "guarded" {
		t.Fatalf("guarded: %+v", r)
	}

	// pi has no headless mode pitwall drives; pane c is no chat at all.
	if r := ask("c", "t1"); r.Code == "" {
		t.Fatalf("pi pane: %+v", r)
	}
	if r := ask("nope", "t1"); r.Code != "not-found" {
		t.Fatalf("no chat: %+v", r)
	}
}

func TestChatSuggestCodex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agents are shell scripts")
	}
	d, _, _ := remoteDaemon(t)
	d.o.SuggestNext = func() bool { return true }
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(`{"models":[
		{"slug":"big","visibility":"list","priority":1,"description":"Our most capable model."},
		{"slug":"gpt-reserve","visibility":"hide","priority":2,"description":"Fast and affordable agentic coding model."},
		{"slug":"small-luna","visibility":"list","priority":8,"description":"Fast and affordable model for easier tasks."},
		{"slug":"old-luna","visibility":"list","priority":11,"description":"Legacy fast and affordable model."}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("PITWALL_CODEX_COMMAND", fakeAgent(t, "codex", `printf '%s\n' "$@" > `+args+`; cat >/dev/null; echo "Ship it"`+"\n"))
	suggestPane(t, d, "b", finished("Add a retry", "Added a retry with backoff."))
	d.mu.Lock()
	d.st.Panes[1].Env = []string{"CODEX_HOME=" + home}
	d.mu.Unlock()
	r := d.chatSuggest(context.Background(), proto.ChatSuggest{Chat: "b", Turn: "t1"})
	if r.Err != "" || r.Text != "Ship it" {
		t.Fatalf("codex: %+v", r)
	}
	got, _ := os.ReadFile(args)
	for _, want := range []string{"exec", "--ephemeral", "--sandbox\nread-only", "--disable\nhooks", `model_reasoning_effort="low"`, "-m\nsmall-luna", "\n-\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("codex args %q lack %q", got, want)
		}
	}
}

func TestChatSuggestAntigravity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agents are shell scripts")
	}
	d, _, _ := remoteDaemon(t)
	d.o.SuggestNext = func() bool { return true }
	old := agyModels
	t.Cleanup(func() { agyModels = old })
	agyModels = func() string {
		return "Fetching available models...\ngemini-4-neon-high\tNeon\ngemini-3.8-flash-high\tFlash (High)\ngemini-3.8-flash-low\tFlash (Low)\n"
	}
	home := t.TempDir()
	id := "ed9d73be-dea3-46ad-9369-43683d9d6d75"
	state := filepath.Join(home, ".gemini", "antigravity-cli")
	keep := filepath.Join(state, "conversations", "55343791-b193-4d05-8c17-34fe00ea84c2.db")
	for _, p := range []string{filepath.Join(state, "conversations", id+".db"), filepath.Join(state, "brain", id, "x"), keep} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("x"), 0o600)
	}
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("PITWALL_AGY_COMMAND", fakeAgent(t, "agy", `printf '%s\n' "$@" > `+args+`; printf '{"conversation_id":"`+id+`","status":"SUCCESS","response":"Deploy it\\n"}'`+"\n"))
	d.mu.Lock()
	d.st.Panes[2].Provider = model.ProviderAntigravity
	d.st.Panes[2].Env = []string{"HOME=" + home}
	d.mu.Unlock()
	suggestPane(t, d, "c", finished("Write the release notes", "Done: RELEASE.md."))
	r := d.chatSuggest(context.Background(), proto.ChatSuggest{Chat: "c", Turn: "t1"})
	if r.Err != "" || r.Text != "Deploy it" {
		t.Fatalf("agy: %+v", r)
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "--model\ngemini-3.8-flash-low") || !strings.Contains(string(got), "-p=Here is the end") || !strings.Contains(string(got), "--output-format\njson") {
		t.Errorf("agy args %q", got)
	}
	if _, err := os.Stat(filepath.Join(state, "conversations", id+".db")); err == nil {
		t.Error("the run's conversation stayed in agy's history")
	}
	if _, err := os.Stat(filepath.Join(state, "brain", id)); err == nil {
		t.Error("the run's brain folder stayed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("another conversation went too")
	}
}

// TestChatSuggestStopsWhenBusy: an agent that starts working stops the
// run, and its answer is never offered.
func TestChatSuggestStopsWhenBusy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agents are shell scripts")
	}
	oldPoll := suggestPoll
	suggestPoll = 10 * time.Millisecond
	t.Cleanup(func() { suggestPoll = oldPoll })
	d, _, _ := remoteDaemon(t)
	d.o.SuggestNext = func() bool { return true }
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv(claude.CommandEnv, fakeAgent(t, "claude", "touch "+started+"; cat >/dev/null; sleep 30; echo late\n"))
	suggestPane(t, d, "a", finished("a", "b"))
	done := make(chan proto.ChatSuggestResult, 1)
	go func() { done <- d.chatSuggest(context.Background(), proto.ChatSuggest{Chat: "a", Turn: "t1"}) }()
	for i := 0; ; i++ {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("the fake never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Code != "busy" || r.Text != "" {
			t.Fatalf("stopped run: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run kept going while the agent worked")
	}
}

func TestCleanSuggestion(t *testing.T) {
	for in, want := range map[string]string{
		"  Run the tests  \n":        "Run the tests",
		`"Add a changelog line"`:     "Add a changelog line",
		"`git push`":                 "git push",
		"'ok'":                       "ok",
		"Run\nthe   tests":           "Run the tests",
		"":                           "",
		"nothing":                    "",
		"(nothing)":                  "",
		"None.":                      "",
		strings.Repeat("word ", 100): "",
		`"`:                          `"`,
	} {
		if got := cleanSuggestion(in); got != want {
			t.Errorf("cleanSuggestion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestContext(t *testing.T) {
	turn := "t"
	var items []session.Item
	for i := range 10 {
		items = append(items, session.Item{Kind: session.KindUserPrompt, Text: "prompt " + string(rune('a'+i)), TurnID: &turn},
			session.Item{Kind: session.KindReasoningText, Text: "thinking", TurnID: &turn},
			session.Item{Kind: session.KindAssistantText, Text: "reply " + string(rune('a'+i)), TurnID: &turn})
	}
	got := suggestContext(session.Snapshot{Content: items})
	if n := strings.Count(got, "User:") + strings.Count(got, "Assistant:"); n != suggestItems {
		t.Fatalf("%d blocks, want %d: %q", n, suggestItems, got)
	}
	if !strings.HasSuffix(got, "Assistant: reply j") || strings.Contains(got, "thinking") || strings.Contains(got, "prompt a") {
		t.Fatalf("context %q", got)
	}
	// One huge reply keeps its end, within the budget.
	long := strings.Repeat("x", 20000) + " DONE"
	got = suggestContext(session.Snapshot{Content: []session.Item{{Kind: session.KindAssistantText, Text: long}}})
	if !strings.HasSuffix(got, "DONE") || len([]rune(got)) > suggestItemRunes+20 {
		t.Fatalf("long reply: %d runes, end %q", len([]rune(got)), got[len(got)-10:])
	}
	if suggestContext(session.Snapshot{}) != "" {
		t.Fatal("an empty chat has context")
	}
}

func TestCheapest(t *testing.T) {
	if got := cheapestAgy("gemini-4-neon-high\tx\ngemini-3.8-flash-medium\ty\n"); got != "gemini-3.8-flash-medium" {
		t.Errorf("no low flash: %q", got)
	}
	if got := cheapestAgy("Error: not logged in\n"); got != "" {
		t.Errorf("no models: %q", got)
	}
	if got := codexCheapest(t.TempDir()); got != "" {
		t.Errorf("no cache: %q", got)
	}
	if got := codexCheapest(""); got != "" {
		t.Errorf("no home: %q", got)
	}
}

// TestChatSuggestTaskChat runs ChatSuggest over the socket for a New task
// chat whose turn ended.
func TestChatSuggestTaskChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agents are shell scripts")
	}
	t.Setenv("PITWALL_CODEX_COMMAND", fakeAgent(t, "codex", "cat >/dev/null; echo 'Open a PR'\n"))
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderCodex: prov}
		o.SuggestNext = func() bool { return true }
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderCodex, Dir: t.TempDir(), Prompt: "Fix the build"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	drv := prov.driver(t)
	waitChat(gui, "running", func(s model.State) bool { return chatOf(s).State == session.StateRunning })
	if s := chatCall[proto.ChatSuggestResult](gui, proto.ChatSuggest{Chat: r.Chat, Turn: r.Turn}); s.Code != "busy" && s.Code != "stale" {
		t.Fatalf("while running: %+v", s)
	}
	drv.event(session.Event{Type: session.EventContent, ItemID: "a1", Kind: session.KindAssistantText, Text: "Fixed the build."})
	drv.end(session.TurnCompleted)
	waitChat(gui, "idle", func(s model.State) bool { return chatOf(s).State == session.StateReady })
	s := chatCall[proto.ChatSuggestResult](gui, proto.ChatSuggest{Chat: r.Chat, Turn: r.Turn})
	if s.Err != "" || s.Text != "Open a PR" || s.Chat != r.Chat {
		t.Fatalf("after the turn: %+v", s)
	}
}
