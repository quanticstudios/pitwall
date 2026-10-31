package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestCheckpointOnPrompt: a prompt checkpoints the tab's folder before its
// hook is answered, a slash command does not, closing the tab drops its
// checkpoints, and a checkpoint that waited past the close takes none.
func TestCheckpointOnPrompt(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "w", Path: "/repo", Tabs: []model.Tab{{ID: "t", Layout: layout.Leaf("a")}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive = agent.Derive
	calls := make(chan string, 4)
	o.Checkpoint = func(_ context.Context, dir, ws string) (string, error) {
		time.Sleep(50 * time.Millisecond)
		calls <- "checkpoint " + dir + " " + ws
		return "", nil
	}
	o.DropTurns = func(_ context.Context, dir, ws string) error { calls <- "drop " + dir + " " + ws; return nil }
	d := newDaemon(t, o)
	next := func(wait time.Duration) string {
		select {
		case c := <-calls:
			return c
		case <-time.After(wait):
			return "none"
		}
	}

	claudeHook(t, d, "a", `{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"/compact"}`)
	claudeHook(t, d, "a", `{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"fix it"}`)
	select {
	case c := <-calls:
		if c != "checkpoint /repo w" {
			t.Errorf("when the prompt's hook returned: %s", c)
		}
	default:
		t.Error("the prompt's hook returned before its checkpoint")
	}
	if err := d.killSession(proto.KillSession{WorkspaceID: "w"}); err != nil {
		t.Fatal(err)
	}
	if c := next(2 * time.Second); c != "drop /repo w" {
		t.Errorf("after closing the tab: %s", c)
	}
	d.checkpoint("/repo", "w")
	if c := next(100 * time.Millisecond); c != "none" {
		t.Errorf("a closed tab got %s", c)
	}
}
