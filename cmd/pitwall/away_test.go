package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// awayState is driveState's tab "build" (#2) with a card on its agent
// pane p, and an ungrouped tab "docs" (#1) with none.
func awayState() model.State {
	st := driveState(model.ProviderClaude, model.StateAwaitingInput, false, 0).State
	st.Sessions[0].Order = append(st.Sessions[0].Order, "w2")
	st.Workspaces = append(st.Workspaces, model.Workspace{ID: "w2", SessionID: "m", Name: "docs", NameSet: true, Path: "/work/docs", Tabs: []model.Tab{{ID: "t2", Layout: &layout.Node{Pane: "q"}}}, ActiveTab: "t2"})
	st.Panes = append(st.Panes, model.Pane{ID: "q", WorkspaceID: "w2", Provider: model.ProviderCodex})
	left := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	st.Away = map[string]model.Away{"p": {Chat: "p", Since: left, At: left.Add(2*time.Hour + 14*time.Minute), Replies: 3, Turns: 2,
		Edited: []string{"login.go"}, EditedN: 1, Commands: 4, Failed: "go test ./auth/...", Tests: "failed", Item: "e1", Seq: 4,
		Status: "The race is fixed; one test still fails.", Needs: "Pick a cookie lifetime."}}
	return st
}

func TestCLIAway(t *testing.T) {
	want := "#2 build · since you left 2h 14m · Needs you\n" +
		"  The race is fixed; one test still fails.\n" +
		"  Pick a cookie lifetime.\n" +
		"  3 messages in 2 turns · 1 file edited · 4 commands · tests failed\n" +
		"  Last failed: go test ./auth/...\n"
	for _, args := range [][]string{{"away"}, {"away", "build"}, {"away", "@p"}, {"away", "#2"}} {
		fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
		code, out, stderr := cliOutput(args...)
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("%q: %d %q\n%s\nwant\n%s", args, code, stderr, out, want)
		}
	}

	// A tab without a card prints nothing; in JSON, [].
	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
	if code, out, stderr := cliOutput("away", "docs"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("docs: %d %q %q", code, out, stderr)
	}
	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
	if code, out, _ := cliOutput("away", "docs", "--json"); code != 0 || out != "[]\n" {
		t.Fatalf("docs --json: %d %q", code, out)
	}

	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
	code, out, stderr := cliOutput("away", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); code != 0 || err != nil || len(rows) != 1 {
		t.Fatalf("--json: %d %q %q %v", code, out, stderr, err)
	}
	r := rows[0]
	for k, v := range map[string]any{"addr": "#2", "tab": "w", "title": "build", "now": "Needs you", "for": "2h 14m", "chat": "p", "item": "e1", "seq": 4.0, "status": "The race is fixed; one test still fails.", "tests": "failed"} {
		if r[k] != v {
			t.Errorf("--json %s = %v, want %v", k, r[k], v)
		}
	}

	// A summary on its way says so.
	st := awayState()
	a := st.Away["p"]
	a.Status, a.Needs, a.Asking = "", "", true
	st.Away["p"] = a
	fakeCLI(t, cliExchange{state: st, level: proto.Level})
	if _, out, _ := cliOutput("away"); !strings.Contains(out, "\n  (summarizing)\n") {
		t.Fatalf("asking: %q", out)
	}
}

func TestCLIAwayDismiss(t *testing.T) {
	gone := awayState()
	gone.Away = nil
	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level}, cliExchange{request: proto.ChatAwayDismiss{Chat: "p"}, state: gone, level: proto.Level})
	if code, out, stderr := cliOutput("away", "--dismiss", "build"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("dismiss: %d %q %q", code, out, stderr)
	}
	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
	if code, _, stderr := cliOutput("away", "--dismiss", "docs"); code == 0 || !strings.Contains(stderr, "has no Since you left card") {
		t.Fatalf("dismiss without a card: %d %q", code, stderr)
	}
	fakeCLI(t, cliExchange{state: awayState(), level: proto.AwayLevel - 1})
	if code, _, stderr := cliOutput("away"); code == 0 || !strings.Contains(stderr, "older") {
		t.Fatalf("old daemon: %d %q", code, stderr)
	}
}

// TestCLIListAway: ls --json rows carry the tab's card.
func TestCLIListAway(t *testing.T) {
	fakeCLI(t, cliExchange{state: awayState(), level: proto.Level})
	_, out, _ := cliOutput("ls", "--json")
	var rows []struct {
		Away *model.Away `json:"away"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 || rows[1].Away == nil || rows[1].Away.Item != "e1" || rows[0].Away != nil {
		t.Fatalf("ls --json away: %v %q", err, out)
	}
}
