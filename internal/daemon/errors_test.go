package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/layout"
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
