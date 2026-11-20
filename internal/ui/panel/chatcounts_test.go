package panel

import (
	"testing"

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
