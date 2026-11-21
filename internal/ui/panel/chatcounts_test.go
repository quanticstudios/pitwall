package panel

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestFoldLabel: a fold's header says what its calls did as T3 Code
// sums a group: the two kinds first by priority, in the order they came,
// the rest as other actions; an edit counts its files once; task lists
// left out; thinking alone is a thought.
func TestFoldLabel(t *testing.T) {
	call := func(name string) session.Item {
		return session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: name}}
	}
	edit := func(path string) session.Item {
		in, _ := json.Marshal(map[string]string{"file_path": path})
		return session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Edit", Input: in}}
	}
	fail := func(it session.Item) session.Item {
		c := *it.Tool
		c.Status = "failed"
		it.Tool = &c
		return it
	}
	think := session.Item{Kind: session.KindReasoningText}
	todo := call("TodoWrite")
	todo.Tool.Tasks = []session.Task{{Subject: "x"}}
	for _, c := range []struct {
		items      []session.Item
		want, icon string
	}{
		{nil, "", ""},
		{[]session.Item{todo}, "", ""},
		{[]session.Item{call("Bash"), call("Bash")}, "Ran 2 commands", "terminal"},
		{[]session.Item{call("Read"), call("Read"), call("Bash"), todo, call("Bash")}, "Read 2 files and ran 2 commands", "hammer"},
		{[]session.Item{call("Grep"), call("Edit")}, "Searched code 1 time and changed 1 file", "hammer"},
		{[]session.Item{call("Bash"), call("ToolSearch"), call("Read")}, "Ran 1 command, read 1 file, and performed 1 other action", "hammer"},
		{[]session.Item{call("Read"), call("Bash"), call("Edit"), call("WebFetch"), call("Agent")}, "Ran 1 command, changed 1 file, and performed 3 other actions", "hammer"},
		{[]session.Item{edit("a.go"), edit("a.go"), edit("b.go")}, "Changed 2 files", "pencil"},
		{[]session.Item{think, call("Read")}, "Read 1 file", "file"},
		{[]session.Item{call("Bash"), fail(call("Bash")), call("Bash")}, "Ran 2 commands and 1 command failed", "terminal"},
		{[]session.Item{fail(call("Bash"))}, "1 command failed", "terminal"},
		{[]session.Item{call("Read"), fail(call("Bash")), fail(call("Read"))}, "Read 1 file and 2 calls failed", "hammer"},
		{[]session.Item{call("Bash"), call("Read"), call("Read"), fail(call("Bash")), call("Grep")}, "Ran 1 command, read 2 files, performed 1 other action, and 1 command failed", "hammer"},
		{[]session.Item{call("Read"), fail(call("Bash")), fail(call("Bash"))}, "Read 1 file and 2 commands failed", "hammer"},
		{[]session.Item{edit("a.go"), fail(edit("b.go"))}, "Changed 1 file and 1 edit failed", "pencil"},
		// The other actions count calls, an edit's included, failed or not.
		{[]session.Item{call("Bash"), call("Agent"), edit("a.go"), edit("a.go"), fail(call("Read"))}, "Ran 1 command, started 1 agent, and performed 3 other actions", "hammer"},
		{[]session.Item{think}, "Thought", "brain"},
		{[]session.Item{think, think}, "Thought (×2)", "brain"},
	} {
		if got, icon := foldLabel(c.items, 0, len(c.items)); got != c.want || icon != c.icon {
			t.Errorf("foldLabel = %q %q, want %q %q", got, icon, c.want, c.icon)
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
