package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

func hookFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture and inline hooks in these sequences belong to one session.
	var payload map[string]json.RawMessage
	must(t, json.Unmarshal(b, &payload))
	payload["session_id"] = json.RawMessage(`"s1"`)
	b, err = json.Marshal(payload)
	must(t, err)
	return b
}

func hook(t *testing.T, d *Daemon, fixture string) {
	t.Helper()
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: hookFixture(t, fixture)}))
}

func hookJSON(t *testing.T, d *Daemon, payload string) {
	t.Helper()
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}))
}

// TestErrorsLogged: a failed command, one that exited 1 quietly and a
// StopFailure reach errors.jsonl; a command that worked does not.
func TestErrorsLogged(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "fix", Branch: "fix-x", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	l, err := errlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	o.Errors = l
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	hook(t, d, "claude_post_tool_use")
	hook(t, d, "claude_post_tool_use_failure")
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"grep -q x f"},"error":"Exit code 1"}`)
	hook(t, d, "claude_stop_failure")
	l.Close(time.Second)

	evs, err := errlog.Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("events %+v", evs)
	}
	for _, e := range evs {
		if e.Tab != "w" || e.Name != "fix" || e.Branch != "fix-x" {
			t.Errorf("tab identity %+v", e)
		}
	}
	c, q, a := evs[0], evs[1], evs[2]
	if !q.Quiet || q.Exit != 1 || q.Command != "grep -q x f" || errlog.Signature(q) != "Exit 1 with no output: grep" {
		t.Errorf("quiet %+v", q)
	}
	if c.Kind != errlog.Command || c.Pane != "a" || c.Provider != "claude" || c.Tool != "Bash" || c.Command != "npm test" ||
		c.Output != "Error: Cannot find module 'express'" || c.Exit != 1 || c.Cwd != "/home/u/repo" || c.Ms != 4187 || c.Size == 0 {
		t.Errorf("command %+v", c)
	}
	if a.Kind != errlog.API || a.Output != "API Error: Rate limit reached" || a.Provider != "claude" {
		t.Errorf("api %+v", a)
	}
}

func TestChatErrorsKeepTab(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{Workspaces: []model.Workspace{{ID: "w", Name: "watcher", Branch: "bot/watch"}}}
	o := f.options()
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	l, err := errlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	o.Errors = l
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	detail := "stream disconnected"
	c := &chat{id: "c", cfg: session.Config{Workspace: "w", Provider: model.ProviderCodex},
		snap: session.Snapshot{SessionState: session.StateError, LastError: &detail, Turns: []session.TurnSummary{{State: session.TurnFailed}}}}
	d.mu.Lock()
	after := d.chatActivity(c)
	d.mu.Unlock()
	after()
	l.Close(time.Second)
	evs, err := errlog.Read(path, time.Time{})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v, err %v", evs, err)
	}
	if e := evs[0]; e.Tab != "w" || e.Name != "watcher" || e.Branch != "bot/watch" || e.Pane != "c" || e.Kind != errlog.API {
		t.Errorf("chat error %+v", e)
	}
}

// TestRepeatedError: 5 failed calls in a row with one signature flag a
// working pane; a success, another error, a key from the user and the end
// of the turn each clear it.
func TestRepeatedError(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	// The host differs each time; the signature masks it.
	fail := func(n int) {
		for i := range n {
			hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUseFailure","tool_name":"WebFetch","tool_input":{"url":"https://example.com/`+string(rune('a'+i))+`"},"error":"Claude Code is unable to fetch from example.com/`+string(rune('a'+i))+`"}`)
		}
	}
	repeats := func(want int) {
		t.Helper()
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.attended() {
			if a.PaneID != "a" {
				continue
			}
			if a.Repeats != want || (want > 0) != (a.Repeating != "") {
				t.Fatalf("state %s, repeats %d %q, want %d", a.State, a.Repeats, a.Repeating, want)
			}
			return
		}
		if want > 0 {
			t.Fatal("no activity")
		}
	}
	hook(t, d, "claude_user_prompt_submit")
	fail(4)
	repeats(0)
	fail(1)
	repeats(5)
	fail(2)
	repeats(7)
	hook(t, d, "claude_post_tool_use")
	repeats(0)

	fail(5)
	d.mu.Lock()
	version := d.st.Version
	d.mu.Unlock()
	hookJSON(t, d, `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"magick a.png b.png"},"error":"magick not found"}`)
	repeats(0)
	d.mu.Lock()
	if d.st.Version == version {
		t.Error("changing a visible error did not notify clients")
	}
	d.mu.Unlock()

	fail(5)
	d.noteUserKey("a", []byte("y"))
	repeats(0)

	fail(5)
	hook(t, d, "claude_stop")
	repeats(0)
	hook(t, d, "claude_user_prompt_submit")
	fail(1)
	repeats(0) // a new turn counts from 1

	// Screen detection can end a turn when its Stop hook never arrives.
	fail(4)
	repeats(5)
	d.mu.Lock()
	d.setActivity(context.Background(), "a", model.ProviderClaude, model.StateCompleted, "")
	d.mu.Unlock()
	hook(t, d, "claude_user_prompt_submit")
	fail(1)
	repeats(0)

	// A handed-off snapshot carries presentation fields, not a live run.
	fail(4)
	d.mu.Lock()
	st := d.snapshot()
	d.mu.Unlock()
	next := fromState(f.options(), st)
	next.noteUserKey("a", []byte("y"))
	next.mu.Lock()
	for _, a := range next.attended() {
		if a.Repeats != 0 || a.Repeating != "" {
			t.Errorf("handed-off marker survived input: %+v", a)
		}
	}
	next.mu.Unlock()
}

func TestRepeatedErrorCodexRedacted(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/work/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/work/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d, err := NewWith(o)
	must(t, err)
	// Synthetic credentials stay out of state just as they stay out of errors.jsonl.
	secret := "testcredentialabcdefghijklmnopqrstuvwxyz"
	payload := `{"session_id":"s1","transcript_path":"/t.jsonl","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":["sh","-c","false"]},"tool_response":{"output":"error: TOKEN=` + secret + `","exit_code":1}}`
	for range repeatAfter {
		must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderCodex, Payload: []byte(payload)}))
	}
	d.mu.Lock()
	a := d.attended()[0]
	d.mu.Unlock()
	if a.Repeats != repeatAfter || strings.Contains(a.Repeating, secret) || !strings.Contains(a.Repeating, logs.Redacted) {
		t.Fatalf("repeat state was not redacted: %+v", a)
	}
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderCodex,
		Payload: []byte(`{"session_id":"s1","transcript_path":"/t.jsonl","hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":{"output":"ok","exit_code":0}}`)}))
	d.mu.Lock()
	defer d.mu.Unlock()
	if a := d.attended()[0]; a.Repeats != 0 || a.Repeating != "" {
		t.Fatalf("success did not clear repeat: %+v", a)
	}
}
