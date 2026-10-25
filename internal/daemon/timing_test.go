package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/timelog"
)

// TestTimingLogged: a prompt, a test run and a Stop reach timing.jsonl
// as the tab's first prompt and a turn with the test's time.
func TestTimingLogged(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "fix", Branch: "fix-x", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	path := filepath.Join(t.TempDir(), "timing.jsonl")
	l, err := timelog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	o.Timing = l
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo",`
	hookJSON(t, d, head+`"hook_event_name":"UserPromptSubmit","prompt":"fix the bug"}`)
	hookJSON(t, d, head+`"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"x1","tool_input":{"command":"go test ./..."}}`)
	time.Sleep(40 * time.Millisecond)
	hookJSON(t, d, head+`"hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"x1","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok"}}`)
	hookJSON(t, d, head+`"hook_event_name":"Stop","last_assistant_message":"Fixed."}`)
	l.Close(time.Second)

	evs, err := timelog.Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	if p := evs[0]; p.Kind != timelog.Prompt || p.Tab != "w" {
		t.Errorf("prompt %+v", p)
	}
	e := evs[1]
	if e.Kind != timelog.Turn || e.End != timelog.Completed || e.Pane != "a" || e.Tab != "w" || e.Branch != "fix-x" || e.Name != "fix" || e.Provider != "claude" {
		t.Errorf("turn %+v", e)
	}
	if e.Tools[timelog.Test] < 30 || e.Model+e.Wait+e.Tools[timelog.Test] != e.Ms {
		t.Errorf("parts %+v", e)
	}
}
