package flow

import (
	"encoding/json"
	"testing"

	managed "github.com/quanticstudios/pitwall/internal/session"
)

func TestEditStat(t *testing.T) {
	for _, c := range []struct {
		tool, input string
		add, del    int
	}{
		{"Edit", `{"file_path":"a.go","old_string":"a\nb\nc","new_string":"a\nB\nc\nd"}`, 2, 1},
		{"MultiEdit", `{"file_path":"a.go","edits":[{"old_string":"x","new_string":"y"},{"old_string":"","new_string":"z"}]}`, 2, 1},
		{"Write", `{"file_path":"a.go","content":"1\n2\n3\n"}`, 3, 0},
		{"apply_patch", `{"input":"*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n+more\n*** End Patch"}`, 2, 1},
		{"Bash", `{"command":"ls"}`, 0, 0},
	} {
		if a, d := editStat(c.tool, json.RawMessage(c.input)); a != c.add || d != c.del {
			t.Errorf("%s: +%d −%d, want +%d −%d", c.tool, a, d, c.add, c.del)
		}
	}
	if a, d := ToolStat(managed.ToolCall{ToolName: "Edit", Diff: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n+c"}); a != 2 || d != 1 {
		t.Errorf("chat diff: +%d −%d", a, d)
	}
}

func TestLogEntryTimeAndStat(t *testing.T) {
	l := NewLog("claude", "")
	l.parse([]byte(`{"type":"assistant","timestamp":"2026-10-05T10:00:01Z","message":{"content":[{"type":"tool_use","id":"e","name":"Edit","input":{"file_path":"a.go","old_string":"a","new_string":"b\nc"}}]}}`))
	e := l.entries[0]
	if e.At != 1_791_194_401_000 || e.Add != 2 || e.Del != 1 {
		t.Errorf("%+v", e)
	}
}
