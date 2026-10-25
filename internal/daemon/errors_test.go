package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestErrorsLogged: a failed command, one that exited 1 quietly and a
// StopFailure reach errors.jsonl with every decision feature off; a
// command that worked does not.
func TestErrorsLogged(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
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
