package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/timelog"
)

// TestTimingLogged: a prompt, a test run and a Stop reach timing.jsonl
// as the tab's first prompt and a turn with the test's time and folder;
// a call that failed in the turn reaches errors.jsonl with its start.
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
	errPath := filepath.Join(t.TempDir(), "errors.jsonl")
	el, err := errlog.Open(errPath)
	if err != nil {
		t.Fatal(err)
	}
	o.Errors = el
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo",`
	hookJSON(t, d, head+`"hook_event_name":"UserPromptSubmit","prompt":"fix the bug"}`)
	hookJSON(t, d, head+`"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"x1","tool_input":{"command":"go test ./..."}}`)
	time.Sleep(40 * time.Millisecond)
	hookJSON(t, d, head+`"hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"x1","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok"}}`)
	hookJSON(t, d, head+`"hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_use_id":"x2","tool_input":{"command":"go vet ./..."},"error":"Exit code 1\nvet: bad"}`)
	hookJSON(t, d, `{"session_id":"side","transcript_path":null,"cwd":"/other","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"make"}}`)
	hookJSON(t, d, head+`"hook_event_name":"Stop","last_assistant_message":"Fixed."}`)
	hookJSON(t, d, head+`"hook_event_name":"StopFailure","last_assistant_message":"API Error: Rate limit reached"}`)
	l.Close(time.Second)
	el.Close(time.Second)

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
	if e.Cwd != "/home/u/repo" {
		t.Errorf("turn cwd %q", e.Cwd)
	}
	errs, err := errlog.Read(errPath, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 2 || !errs[0].Turn.Equal(e.Start) || !errs[1].Turn.IsZero() {
		t.Errorf("error turns %+v, turn start %v", errs, e.Start)
	}
	if e.Tools[timelog.Test] < 30 || e.Model+e.Wait+e.Tools[timelog.Test] != e.Ms {
		t.Errorf("parts %+v", e)
	}
}

// Cursor can first report work with a failed call. Its hook folder can
// differ from the pane's shell folder, and its error still belongs to the turn.
func TestTimingFirstCursorFailure(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	path := filepath.Join(t.TempDir(), "timing.jsonl")
	l, err := timelog.Open(path)
	must(t, err)
	t.Cleanup(func() { l.Close(time.Second) })
	o.Timing = l
	errPath := filepath.Join(t.TempDir(), "errors.jsonl")
	el, err := errlog.Open(errPath)
	must(t, err)
	t.Cleanup(func() { el.Close(time.Second) })
	o.Errors = el
	d, err := NewWith(o)
	must(t, err)
	for _, payload := range []string{
		`{"conversation_id":"s1","hook_event_name":"postToolUseFailure","tool_name":"Shell","tool_input":{"command":"make"},"cwd":"/repo/worktree","error_message":"Command failed with exit code 2","duration":12}`,
		`{"conversation_id":"s1","hook_event_name":"stop","status":"error"}`,
		`{"conversation_id":"s1","hook_event_name":"postToolUseFailure","tool_name":"Shell","tool_input":{"command":"make"},"error_message":"Command failed with exit code 2","duration":12}`,
		`{"conversation_id":"s1","hook_event_name":"stop","status":"error"}`,
	} {
		must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderCursor, Payload: []byte(payload)}))
	}
	l.Close(time.Second)
	el.Close(time.Second)
	evs, err := timelog.Read(path, time.Time{})
	must(t, err)
	errs, err := errlog.Read(errPath, time.Time{})
	must(t, err)
	if len(evs) != 3 || len(errs) != 4 {
		t.Fatalf("timing %+v, errors %+v", evs, errs)
	}
	turn, failure := evs[1], errs[0]
	if failure.Turn.IsZero() || !failure.Turn.Equal(turn.Start) {
		t.Errorf("first failure turn %v, want %v", failure.Turn, turn.Start)
	}
	if turn.Cwd != failure.Cwd || turn.Cwd != "/repo/worktree" {
		t.Errorf("turn cwd %q, error cwd %q", turn.Cwd, failure.Cwd)
	}
	if api := errs[1]; api.Kind != errlog.API || api.Cwd != turn.Cwd || !api.Turn.Equal(turn.Start) {
		t.Errorf("API error %+v, turn %+v", api, turn)
	}
	if d.st.Panes[0].Cwd != "/repo" {
		t.Errorf("pane cwd changed to %q", d.st.Panes[0].Cwd)
	}
	second := evs[2]
	if second.Cwd != "/repo" || second.End != timelog.Failed {
		t.Errorf("second turn %+v", second)
	}
	for _, e := range errs[2:] {
		if e.Cwd != second.Cwd || e.Turn.IsZero() || !e.Turn.Equal(second.Start) {
			t.Errorf("second turn error %+v, start %v", e, second.Start)
		}
	}
	if len(d.turnCwd) != 0 {
		t.Errorf("ended turn folders %+v", d.turnCwd)
	}
}

// TestTimingBackgroundEndsTurn: a Stop that leaves a background shell
// running ends the timed turn there, so the idle hours until the next
// prompt never count as model time.
func TestTimingBackgroundEndsTurn(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "fix", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
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
	hookJSON(t, d, head+`"hook_event_name":"UserPromptSubmit","prompt":"start the dev server"}`)
	hookJSON(t, d, head+`"hook_event_name":"Stop","last_assistant_message":"Running.","background_tasks":[{"type":"shell","status":"running"}]}`)
	d.mu.Lock()
	open := d.turns.Has("a")
	d.mu.Unlock()
	if open {
		t.Error("the turn stayed open behind a background shell")
	}
	l.Close(time.Second)
	evs, err := timelog.Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[1].Kind != timelog.Turn || evs[1].End != timelog.Completed {
		t.Fatalf("events %+v", evs)
	}
}
