package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The payloads in testdata/antigravity are agy 1.5.13's own, from a run
// under a temporary home, with paths and the conversation id replaced.
func agyFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "antigravity", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFromAntigravity(t *testing.T) {
	const sid = "11111111-2222-3333-4444-555555555555"
	now := time.Unix(1_800_000_000, 0)

	// The fixture's own paths are Unix ones; a path of this system stands in, so it is absolute everywhere.
	var raw map[string]any
	json.Unmarshal(agyFixture(t, "session_start.json"), &raw)
	path := filepath.Join(t.TempDir(), sid, ".system_generated", "logs", "transcript_full.jsonl")
	raw["transcriptPath"] = path
	b, _ := json.Marshal(raw)
	start := FromAntigravity(b)
	if SessionID(model.ProviderAntigravity, start) != sid || Transcript(start) != path {
		t.Fatalf("SessionStart: session %q, transcript %q", SessionID(model.ProviderAntigravity, start), Transcript(start))
	}
	if _, ok := Derive(nil, model.ProviderAntigravity, start, now); ok {
		t.Error("SessionStart changed the activity; agy waits at its prompt")
	}

	prompt := FromAntigravity(agyFixture(t, "pre_invocation.json"))
	if got := Prompt(model.ProviderAntigravity, prompt); !strings.HasPrefix(got, "Run exactly this shell command") {
		t.Errorf("first PreInvocation's prompt %q", got)
	}
	a, ok := Derive(nil, model.ProviderAntigravity, prompt, now)
	if !ok || a.State != model.StateWorking || a.SessionID != sid {
		t.Errorf("first PreInvocation: %+v, %v", a, ok)
	}
	if b := FromAntigravity(agyFixture(t, "pre_invocation_next.json")); b != nil {
		t.Errorf("a later PreInvocation read as %s", b)
	}
	if b := FromAntigravity(agyFixture(t, "pre_tool_use_question.json")); b != nil {
		t.Errorf("PreToolUse, which pitwall does not register, read as %s", b)
	}

	post := FromAntigravity(agyFixture(t, "post_tool_use.json"))
	if a, ok := Derive(nil, model.ProviderAntigravity, post, now); !ok || a.State != model.StateWorking {
		t.Errorf("PostToolUse: %+v, %v", a, ok)
	}
	if _, failed := ToolFailure(model.ProviderAntigravity, post); failed {
		t.Error("a command that succeeded read as a failure")
	}
	if c, ok := ToolCall(post); !ok || !c.End || c.Tool != "run_command" || c.Command != "mkdir -p ./junk2 && rmdir ./junk2" || c.ID != "call_12900578850290419103" {
		t.Errorf("PostToolUse call %+v, %v", c, ok)
	}

	// A command the user stopped: the same payload with the error agy gives it.
	raw = nil
	json.Unmarshal(agyFixture(t, "post_tool_use.json"), &raw)
	raw["error"] = "command was terminated by a signal: context canceled"
	b, _ = json.Marshal(raw)
	f, failed := ToolFailure(model.ProviderAntigravity, FromAntigravity(b))
	if !failed || !f.Interrupted || !f.Shell || f.Command != "mkdir -p ./junk2 && rmdir ./junk2" || f.Output != "command was terminated by a signal: context canceled" {
		t.Errorf("stopped command: %+v, %v", f, failed)
	}

	// Stop takes its reply from the transcript the payload names.
	dir := t.TempDir()
	tr := filepath.Join(dir, "transcript_full.jsonl")
	os.WriteFile(tr, []byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nhi\n</USER_REQUEST>"}
{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"Ran it; **all good**."}
`), 0o600)
	stop := func(reason, errText string) []byte {
		var raw map[string]any
		json.Unmarshal(agyFixture(t, "stop.json"), &raw)
		raw["transcriptPath"], raw["terminationReason"], raw["error"] = tr, reason, errText
		b, _ := json.Marshal(raw)
		return FromAntigravity(b)
	}
	working := &model.Activity{Provider: model.ProviderAntigravity, State: model.StateWorking, SessionID: sid}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("NO_TOOL_CALL", ""), now); !ok || a.State != model.StateCompleted || a.Detail != "Ran it; **all good**." {
		t.Errorf("Stop: %+v, %v", a, ok)
	}
	if got := LastMessage(stop("NO_TOOL_CALL", "")); got != "Ran it; **all good**." {
		t.Errorf("Stop's last message %q", got)
	}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("ERROR", "quota exhausted"), now); !ok || a.State != model.StateError || a.Detail != "quota exhausted" {
		t.Errorf("Stop ERROR: %+v, %v", a, ok)
	}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("USER_CANCELED", ""), now); !ok || a.State != "" {
		t.Errorf("Stop USER_CANCELED: %+v, %v; want the activity gone", a, ok)
	}

	for name, b := range map[string]string{
		"not JSON":       `{`,
		"no event":       `{"conversationId":"x"}`,
		"PostInvocation": `{"hookEventName":"PostInvocation","conversationId":"x","invocationNum":0}`,
		"Claude's shape": `{"hook_event_name":"Stop","session_id":"x"}`,
	} {
		if got := FromAntigravity([]byte(b)); got != nil {
			t.Errorf("%s: %s", name, got)
		}
	}
	// A transcript path that names some other file is not read or passed on.
	raw = map[string]any{"hookEventName": "Stop", "conversationId": "x", "transcriptPath": "/etc/passwd", "terminationReason": "NO_TOOL_CALL"}
	b, _ = json.Marshal(raw)
	if got := FromAntigravity(b); Transcript(got) != "" || LastMessage(got) != "" {
		t.Errorf("foreign transcript path kept: %s", got)
	}
}

func TestAntigravityHooks(t *testing.T) {
	var got map[string]map[string][]map[string]any
	if err := json.Unmarshal(AntigravityHooks("/opt/pitwall"), &got); err != nil {
		t.Fatal(err)
	}
	hooks := got[AntigravityHookName]
	if len(got) != 1 || len(hooks) != 4 || hooks["PreToolUse"] != nil {
		t.Fatalf("hooks %v; want SessionStart, PreInvocation, PostToolUse and Stop under %q", got, AntigravityHookName)
	}
	if c := hooks["Stop"][0]["command"]; c != commandPath(runtime.GOOS, "/opt/pitwall")+" hook antigravity" || hooks["Stop"][0]["timeout"] != float64(5) {
		t.Errorf("Stop handler %v", hooks["Stop"][0])
	}
	g := hooks["PostToolUse"][0]
	if inner, _ := g["hooks"].([]any); g["matcher"] != "*" || len(inner) != 1 {
		t.Errorf("PostToolUse group %v", g)
	}
}

func TestIdentifyAntigravity(t *testing.T) {
	for _, c := range [][2]string{{"agy", ""}, {"antigravity", ""}, {"x", "/home/u/.local/share/mise/installs/antigravity-cli/1.5.13/antigravity"}} {
		if p := Identify(c[0], c[1]); p != model.ProviderAntigravity {
			t.Errorf("Identify(%q, %q) = %q", c[0], c[1], p)
		}
	}
	if Command(model.ProviderAntigravity) != "agy" || !HooksFor(model.ProviderAntigravity) {
		t.Error("agy's command or hooks")
	}
}

// TestAgyFooter reads the footers agy 1.5.13 drew: with a mode word, in
// its default mode without one, and none while a picker covers it.
func TestAgyFooter(t *testing.T) {
	screen := func(rows ...string) vt.Grid {
		e := vt.New(100, 6, nil)
		e.Write([]byte(strings.Join(rows, "\r\n")))
		return e.Snapshot()
	}
	pad := func(left, right string) string {
		return left + strings.Repeat(" ", 100-len([]rune(left))-len([]rune(right))) + right
	}
	for _, c := range []struct {
		name                string
		g                   vt.Grid
		mode, model, effort string
		ok                  bool
	}{
		{"auto", screen("> ", pad("? for shortcuts", "auto · Gemini 3.8 Flash · low")), "auto", "Gemini 3.8 Flash", "low", true},
		{"busy, accept edits", screen(pad("esc to cancel", "accept-edits · Claude Sonnet 5.5 · medium")), "accept-edits", "Claude Sonnet 5.5", "medium", true},
		{"default mode", screen(pad("? for shortcuts", "Gemini 4 Neon · xhigh")), "default", "Gemini 4 Neon", "xhigh", true},
		{"picker", screen("Keyboard: ↑/↓ Navigate  ←/→ Effort  enter Select  esc Go Back"), "", "", "", false},
		{"prose with dots", screen(pad("", "a · b · c")), "", "", "", false},
	} {
		mode, mdl, eff, ok := AgyFooter(c.g)
		if mode != c.mode || mdl != c.model || eff != c.effort || ok != c.ok {
			t.Errorf("%s: %q %q %q %v", c.name, mode, mdl, eff, ok)
		}
	}
}
