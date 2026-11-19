package claude

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// lastTool is the call the last event that carries one says.
func lastTool(t *testing.T, evs []session.Event) session.ToolCall {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Tool != nil {
			return *evs[i].Tool
		}
	}
	t.Fatal("no tool event")
	return session.ToolCall{}
}

func use(id, name, input string) string {
	return `{"type":"assistant","message":{"id":"m-` + id + `","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

// TestToolResults: a result's exit code, background shell, patch, created
// file and pictures reach the call, as the transcript file spells them
// and as stream-json does.
func TestToolResults(t *testing.T) {
	s := newStream("")
	s.line([]byte(use("b1", "Bash", `{"command":"go test"}`)))
	b := lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b1","content":"Exit code 2\nFAIL","is_error":true}]},"tool_use_result":"Error: Exit code 2"}`)))
	if b.Status != "failed" || b.ExitCode == nil || *b.ExitCode != 2 {
		t.Fatalf("failed bash %+v", b)
	}
	s.line([]byte(use("b2", "Bash", `{"command":"true"}`)))
	b = lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b2","content":""}]},"toolUseResult":{"stdout":"","interrupted":false}}`)))
	if b.ExitCode == nil || *b.ExitCode != 0 {
		t.Fatalf("passed bash %+v", b)
	}

	s.line([]byte(use("bg", "Bash", `{"command":"npm run dev","run_in_background":true}`)))
	b = lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg","content":"Command running in background with ID: bx1. Output is being written to: /tmp/x"}]},"toolUseResult":{"backgroundTaskId":"bx1"}}`)))
	if b.Background != "running" || b.ExitCode != nil {
		t.Fatalf("background %+v", b)
	}
	clear(s.tools) // a new prompt clears the turn's calls; the shell outlives it
	b = lastTool(t, s.line([]byte(`{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>\n<task-id>bx1</task-id>\n<tool-use-id>bg</tool-use-id>\n<status>killed</status>\n</task-notification>"}}`)))
	if b.Background != "killed" || b.CallID != "bg" {
		t.Fatalf("notice %+v", b)
	}
	if evs := s.line([]byte(`{"type":"user","message":{"content":"<task-notification><tool-use-id>bg</tool-use-id><status>completed</status></task-notification>"}}`)); len(evs) != 0 {
		t.Fatalf("a shell that ended took a second notice: %+v", evs)
	}

	s.line([]byte(use("e1", "Edit", `{"file_path":"/r/a.go","old_string":"b","new_string":"c"}`)))
	e := lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e1","content":"updated"}]},"toolUseResult":{"filePath":"/r/a.go","structuredPatch":[{"oldStart":10,"oldLines":3,"newStart":10,"newLines":3,"lines":[" a","-b","+c"," d"]}]}}`)))
	if want := "@@ -10,3 +10,3 @@\n a\n-b\n+c\n d\n"; e.Diff != want {
		t.Fatalf("patch %q", e.Diff)
	}
	s.line([]byte(use("w1", "Write", `{"file_path":"/r/new.go","content":"package r\n"}`)))
	w := lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"w1","content":"File created"}]},"toolUseResult":{"type":"create","filePath":"/r/new.go","structuredPatch":[]}}`)))
	if len(w.Files) != 1 || w.Files[0].Status != "add" || w.Diff != "" {
		t.Fatalf("created %+v", w)
	}

	s.line([]byte(use("r1", "Read", `{"file_path":"/r/shot.png"}`)))
	r := lastTool(t, s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"r1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}}`)))
	if len(r.Images) != 1 || r.Images[0].MimeType != "image/png" || r.Images[0].Data != "iVBORw0KGgo=" {
		t.Fatalf("image %+v", r.Images)
	}
}

// TestBackgroundStates: a background shell's end reaches its call as
// stream-json says it, by tool use or by task id, and as Claude Code's
// transcript file queues it.
func TestBackgroundStates(t *testing.T) {
	start := func(s *stream) {
		s.line([]byte(use("bg", "Bash", `{"command":"sleep 4","run_in_background":true}`)))
		s.line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg","content":"Command running in background with ID: b96. Output is"}]},"tool_use_result":{"backgroundTaskId":"b96"}}`))
	}
	s := newStream("")
	start(s)
	if b := lastTool(t, s.line([]byte(`{"type":"system","subtype":"task_updated","task_id":"b96","patch":{"status":"completed","end_time":1}}`))); b.Background != "completed" {
		t.Fatalf("task_updated %+v", b)
	}
	if evs := s.line([]byte(`{"type":"system","subtype":"task_notification","task_id":"b96","tool_use_id":"bg","status":"completed"}`)); len(evs) != 0 {
		t.Fatalf("an ended shell took its notice again: %+v", evs)
	}
	s = newStream("")
	start(s)
	if b := lastTool(t, s.line([]byte(`{"type":"system","subtype":"task_notification","task_id":"b96","tool_use_id":"bg","status":"failed"}`))); b.Background != "failed" {
		t.Fatalf("task_notification %+v", b)
	}
	if evs := s.line([]byte(`{"type":"system","subtype":"task_notification","tool_use_id":"agent-1","status":"completed"}`)); len(evs) != 0 {
		t.Fatalf("a subagent's notice touched a call: %+v", evs)
	}

	tr := NewTranscript()
	tr.Line([]byte(`{"type":"user","uuid":"p1","message":{"content":"go"},"origin":{"kind":"human"}}`))
	tr.Line([]byte(use("bg", "Bash", `{"command":"sleep 4","run_in_background":true}`)))
	tr.Line([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg","content":"Command running in background with ID: b96."}]},"toolUseResult":{"backgroundTaskId":"b96"}}`))
	if b := lastTool(t, tr.Line([]byte(`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>b96</task-id>\n<tool-use-id>bg</tool-use-id>\n<status>completed</status>\n</task-notification>"}`))); b.Background != "completed" {
		t.Fatalf("queued notice %+v", b)
	}
	if evs := tr.Line([]byte(`{"type":"queue-operation","operation":"enqueue","content":[{"type":"text","text":"x"}]}`)); len(evs) != 0 {
		t.Fatalf("a queued message that is not text: %+v", evs)
	}
}
