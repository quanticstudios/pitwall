package panel

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestToolKinds: a work log's header counts its calls by kind, in the
// order each kind first came, three kinds at most, task lists left out.
func TestToolKinds(t *testing.T) {
	call := func(name string) session.Item {
		return session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: name}}
	}
	todo := call("TodoWrite")
	todo.Tool.Tasks = []session.Task{{Subject: "x"}}
	for _, c := range []struct {
		tools []session.Item
		want  string
	}{
		{nil, ""},
		{[]session.Item{todo}, ""},
		{[]session.Item{call("Read"), call("Read"), call("Bash"), todo, call("Bash")}, "Read 2 files, ran 2 commands"},
		{[]session.Item{call("Grep"), call("Edit")}, "Searched for 1 pattern, edited 1 file"},
		{[]session.Item{call("Bash"), call("ToolSearch"), call("Read")}, "Ran 1 command, read 1 file, used 1 other tool"},
		{[]session.Item{call("Read"), call("Bash"), call("Edit"), call("WebFetch"), call("Agent")}, "Read 1 file, ran 1 command, edited 1 file, used 2 other tools"},
	} {
		if got := toolKinds(c.tools, 0, len(c.tools)); got != c.want {
			t.Errorf("toolKinds = %q, want %q", got, c.want)
		}
	}
}

// TestLogStatus: a running fold's header says its newest running call as
// its row says it, and for how long it has run.
func TestLogStatus(t *testing.T) {
	bash := func(cmd, status string) session.Item {
		return session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Bash", Command: cmd, Status: status}}
	}
	dir := t.TempDir()
	in, _ := json.Marshal(map[string]string{"file_path": filepath.Join(dir, "internal", "vt", "vt.go")})
	read := session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Read", Status: "running", Input: in}}
	todo := bash("", "running")
	todo.Tool.ToolName, todo.Tool.Tasks = "TodoWrite", []session.Task{{Subject: "x"}}
	for _, c := range []struct {
		items []session.Item
		want  string
	}{
		{nil, ""},
		{[]session.Item{bash("go build ./...", "completed")}, ""},
		{[]session.Item{bash("go test ./...\necho done", "running")}, "Bash · go test ./..."},
		{[]session.Item{bash("make", "running"), read}, "Read · " + filepath.Join("internal", "vt", "vt.go")},
		{[]session.Item{bash("make", "running"), todo}, "Bash · make"},
		{[]session.Item{{Kind: session.KindReasoningText, Text: "plain thinking"}}, ""},
		{[]session.Item{{Kind: session.KindToolCall, CreatedAt: "2026-11-19T14:00:48Z", Tool: &session.ToolCall{ToolName: "Bash", Command: "make", Status: "running"}}}, "Bash · make · 0:12"},
	} {
		if got := logStatus(c.items, 0, len(c.items), dir, time.Date(2026, 11, 19, 14, 1, 0, 0, time.UTC)); got != c.want {
			t.Errorf("logStatus = %q, want %q", got, c.want)
		}
	}
}
