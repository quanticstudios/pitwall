package claude

import (
	"reflect"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// reduce feeds stdout lines to a stream and reduces what it emits into a
// snapshot, as the chat loop does.
func reduce(t *testing.T, s *stream, snap session.Snapshot, lines ...string) (session.Snapshot, []session.Event) {
	t.Helper()
	var all []session.Event
	for _, l := range lines {
		for _, e := range s.line([]byte(l)) {
			e.CreatedAt = "2026-11-18T10:00:00.000Z"
			all = append(all, e)
			for _, d := range session.Reduce(snap, e) {
				d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
				n, err := session.Apply(&snap, snap.Revision, d)
				if err != nil {
					t.Fatalf("%s: %v", d.Type, err)
				}
				snap = n
			}
		}
	}
	return snap, all
}

func base() session.Snapshot {
	return session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Running: true}
}

// The keys of a real system/init from the spike's captures
// (testdata/basic.stdout.jsonl), with short lists.
const initLine = `{"type":"system","subtype":"init","cwd":"/tmp/acme-api","session_id":"SESSION","tools":["Task","Bash"],"mcp_servers":[],"model":"claude-sonnet-5-5","permissionMode":"default","slash_commands":["review","doctor","color","compact","config"],"terminal_slash_commands":["doctor","color"],"apiKeySource":"none","claude_code_version":"2.1.314","output_style":"default","agents":["Explore","Plan"],"skills":["review"],"plugins":[],"capabilities":["interrupt_receipt_v1"],"uuid":"init-1"}`

func TestInitCommands(t *testing.T) {
	s := newStream("SESSION")
	snap, events := reduce(t, s, base(), initLine)
	want := []session.Command{{Name: "review"}, {Name: "doctor", Terminal: true}, {Name: "color", Terminal: true}, {Name: "compact"}, {Name: "config", Terminal: true}}
	if !reflect.DeepEqual(snap.Commands, want) {
		t.Fatalf("commands: %+v", snap.Commands)
	}
	// Each turn repeats system/init; the same list emits nothing.
	if _, events = reduce(t, s, snap, initLine); len(events) != 0 {
		t.Fatalf("repeated init: %+v", events)
	}
}

// The composer's meter needs the used context and the window: the first
// result gives both, and each later API response moves the used part.
func TestContextWindow(t *testing.T) {
	s := newStream("SESSION")
	s.turn = "turn-1"
	snap, _ := reduce(t, s, base(), initLine,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":2,"output_tokens":4,"cache_read_input_tokens":11384,"cache_creation_input_tokens":5817}},"session_id":"SESSION","uuid":"a-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"SESSION","uuid":"r-1","total_cost_usd":0.02,"modelUsage":{"claude-sonnet-5-5":{"inputTokens":2,"outputTokens":4,"cacheReadInputTokens":11384,"cacheCreationInputTokens":5817,"costUSD":0.02,"contextWindow":200000}}}`)
	if u := snap.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 17207 || u.MaxTokens == nil || *u.MaxTokens != 200000 {
		t.Fatalf("after the first turn: %+v", snap.Usage)
	}
	s.turn = "turn-2"
	snap, _ = reduce(t, s, snap,
		`{"type":"assistant","message":{"id":"msg_2","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}],"usage":{"input_tokens":2,"output_tokens":40,"cache_read_input_tokens":17207,"cache_creation_input_tokens":100}},"session_id":"SESSION","uuid":"a-2"}`)
	if u := snap.Usage; u == nil || *u.ContextTokens != 17349 || *u.MaxTokens != 200000 || u.InputTokens != 2 || u.OutputTokens != 4 {
		t.Fatalf("during the second turn: %+v", snap.Usage)
	}
}
