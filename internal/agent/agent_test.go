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
)

const (
	claudeSID = "0f6c1a52-8d0e-4c1b-9a51-2b7f7d3e9a10"
	codexSID  = "019a3c2e-7b41-7d20-9f3e-5c8a1b2d4e6f"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("%s: invalid JSON", name)
	}
	return b
}

func TestDerive(t *testing.T) {
	const (
		claude = model.ProviderClaude
		codex  = model.ProviderCodex
		none   = model.AgentState("")
	)
	tests := []struct {
		fixture  string
		provider model.Provider
		prev     model.AgentState // "" means no previous activity
		ok       bool
		want     model.AgentState
		detail   string
	}{
		{"claude_session_start", claude, none, false, none, ""},
		{"claude_session_start", claude, model.StateCompleted, false, none, ""},
		{"claude_user_prompt_submit", claude, none, true, model.StateWorking, ""},
		{"claude_user_prompt_submit", claude, model.StateCompleted, true, model.StateWorking, ""},
		{"claude_pre_tool_use", claude, none, true, model.StateWorking, ""},
		{"claude_pre_tool_use", claude, model.StateWorking, false, none, ""}, // unchanged
		{"claude_post_tool_use", claude, model.StatePendingApproval, true, model.StateWorking, ""},
		{"claude_post_tool_use_failure", claude, model.StatePendingApproval, true, model.StateWorking, ""},
		{"claude_ask_user_question", claude, model.StateWorking, true, model.StateAwaitingInput, "Which database should the cache use?"},
		{"claude_permission_request_ask", claude, model.StateWorking, true, model.StateAwaitingInput, "Which database should the cache use?"},
		{"claude_exit_plan_mode", claude, model.StateWorking, true, model.StatePlanReady, ""},
		{"claude_permission_request", claude, model.StateWorking, true, model.StatePendingApproval, "Remove node_modules directory"},
		{"claude_notification_permission", claude, model.StateWorking, true, model.StatePendingApproval, "Claude needs your permission to use Bash"},
		{"claude_notification_permission", claude, model.StateAwaitingInput, false, none, ""},
		{"claude_notification_permission", claude, model.StatePlanReady, false, none, ""},
		{"claude_notification_elicitation", claude, model.StateWorking, true, model.StateAwaitingInput, "github needs your input"},
		{"claude_notification_idle", claude, model.StateCompleted, false, none, ""},
		{"claude_notification_idle", claude, none, false, none, ""},
		{"claude_stop", claude, model.StateWorking, true, model.StateCompleted, "Fixed the token check. All tests pass."},
		{"claude_stop_question", claude, model.StateWorking, true, model.StateCompleted, "Refactored the auth module. Should I also update the tests?"},
		{"claude_stop_background", claude, model.StateWorking, false, none, ""}, // stays working
		{"claude_stop_background", claude, model.StatePendingApproval, true, model.StateWorking, ""},
		{"claude_stop_failure", claude, model.StateWorking, true, model.StateError, "API Error: Rate limit reached"},
		{"claude_subagent_stop", claude, model.StateWorking, false, none, ""},
		{"claude_subagent_stop", claude, model.StateCompleted, false, none, ""},
		{"claude_session_end", claude, model.StateCompleted, true, none, ""},
		{"claude_session_end", claude, none, false, none, ""},
		{"codex_notify", codex, model.StateWorking, true, model.StateCompleted, "Rename complete and verified cargo build succeeds."},
		{"codex_user_prompt_submit", codex, none, true, model.StateWorking, ""},
		{"codex_pre_tool_use", codex, model.StatePendingApproval, true, model.StateWorking, ""},
		{"codex_permission_request", codex, model.StateWorking, true, model.StatePendingApproval, "Allow network access to push?"},
		{"codex_stop", codex, model.StateWorking, true, model.StateCompleted, "Done. Want me to open a PR?"},
		{"codex_interrupt", codex, model.StateWorking, true, none, ""},
		{"codex_side_fork", codex, model.StateWorking, false, none, ""},
		{"codex_session_end", codex, model.StateCompleted, true, none, ""},
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.fixture+"/from-"+string(tt.prev), func(t *testing.T) {
			var prev *model.Activity
			if tt.prev != none {
				sid := claudeSID
				if tt.provider == codex {
					sid = codexSID
				}
				prev = &model.Activity{PaneID: "p1", WorkspaceID: "w1", Provider: tt.provider, SessionID: sid, State: tt.prev, UpdatedAt: now.Add(-time.Minute)}
			}
			next, ok := Derive(prev, tt.provider, fixture(t, tt.fixture), now)
			if ok != tt.ok || next.State != tt.want || next.Detail != tt.detail {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", next.State, next.Detail, ok, tt.want, tt.detail, tt.ok)
			}
			if !ok || next.State == none {
				return
			}
			if next.Provider != tt.provider || !next.UpdatedAt.Equal(now) || next.SessionID == "" {
				t.Fatalf("bad metadata: %+v", next)
			}
			if prev != nil && (next.PaneID != "p1" || next.WorkspaceID != "w1") {
				t.Fatalf("lost pane/workspace ids: %+v", next)
			}
		})
	}
}

func TestDeriveRejectsGarbage(t *testing.T) {
	for _, b := range []string{"", "not json", `{"type":"other"}`, `{"hook_event_name":"FileChanged"}`} {
		if _, ok := Derive(nil, model.ProviderClaude, []byte(b), time.Now()); ok {
			t.Errorf("%q: ok", b)
		}
	}
}

func TestSessionID(t *testing.T) {
	for name, want := range map[string]string{
		"claude_stop":        claudeSID,
		"codex_stop":         codexSID,
		"codex_notify":       codexSID,
		"claude_session_end": claudeSID,
	} {
		p := model.ProviderClaude
		if name[:5] == "codex" {
			p = model.ProviderCodex
		}
		if got := SessionID(p, fixture(t, name)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if got := SessionID(model.ProviderCodex, fixture(t, "codex_side_fork")); got != "" {
		t.Errorf("side fork: %q", got)
	}
	if got := SessionID(model.ProviderClaude, []byte("nope")); got != "" {
		t.Errorf("garbage: %q", got)
	}
}

func TestPrompt(t *testing.T) {
	for name, want := range map[string]string{
		"claude_user_prompt_submit": "fix the failing auth test",
		"codex_user_prompt_submit":  "rename foo to bar",
		"claude_stop":               "",
		"codex_notify":              "",
	} {
		p := model.ProviderClaude
		if name[:5] == "codex" {
			p = model.ProviderCodex
		}
		if got := Prompt(p, fixture(t, name)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	side := `{"session_id":"s","transcript_path":null,"hook_event_name":"UserPromptSubmit","prompt":"aside"}`
	if got := Prompt(model.ProviderCodex, []byte(side)); got != "" {
		t.Errorf("side fork: %q", got)
	}
	if got := Prompt(model.ProviderClaude, []byte("nope")); got != "" {
		t.Errorf("garbage: %q", got)
	}
	slash := `{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":" /model opus"}`
	if got := Prompt(model.ProviderClaude, []byte(slash)); got != "" {
		t.Errorf("slash command: %q", got)
	}
}

func TestClaudeHooks(t *testing.T) {
	var got map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type, Command string
			Timeout       int
		} `json:"hooks"`
	}
	if err := json.Unmarshal(ClaudeHooks("/opt/my bin/pitwall"), &got); err != nil {
		t.Fatal(err)
	}
	for _, e := range claudeEvents {
		g := got[e.name]
		if len(g) != 1 || len(g[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", e.name, g)
		}
		h := g[0].Hooks[0]
		if h.Type != "command" || h.Command != commandPath(runtime.GOOS, "/opt/my bin/pitwall")+" hook claude" || h.Timeout == 0 {
			t.Errorf("%s: %+v", e.name, h)
		}
	}
	if len(got) != len(claudeEvents) {
		t.Errorf("registered %d events, want %d", len(got), len(claudeEvents))
	}
	if m := got["Notification"][0].Matcher; m != "permission_prompt|elicitation_dialog|elicitation_url_dialog" {
		t.Errorf("Notification matcher %q", m)
	}
	if _, ok := got["SessionStart"]; ok {
		t.Error("SessionStart registered but Derive ignores it")
	}
	if !json.Valid(CodexHooks("pitwall")) {
		t.Error("CodexHooks: invalid JSON")
	}
}

func TestCodexNotify(t *testing.T) {
	if got, want := CodexNotify("/usr/local/bin/pitwall"), `notify = ["/usr/local/bin/pitwall", "hook", "codex"]`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestCommandPath(t *testing.T) {
	for _, tc := range []struct{ goos, bin, want string }{
		{"linux", "/opt/my bin/pitwall", `'/opt/my bin/pitwall'`},
		{"darwin", "/Users/o'neil/bin/pitwall", `'/Users/o'\''neil/bin/pitwall'`},
		{"windows", `C:\Users\me\AppData\Local\pitwall\bin\pitwall.exe`, `C:/Users/me/AppData/Local/pitwall/bin/pitwall.exe`},
		{"windows", `C:\Users\Jane Doe\AppData\Local\pitwall\bin\pitwall.exe`, `"C:/Users/Jane Doe/AppData/Local/pitwall/bin/pitwall.exe"`},
	} {
		if got := commandPath(tc.goos, tc.bin); got != tc.want {
			t.Errorf("commandPath(%s, %q) = %s, want %s", tc.goos, tc.bin, got, tc.want)
		}
	}
}

func TestRequest(t *testing.T) {
	ev, tool, input, cwd, ok := Request(fixture(t, "claude_permission_request"))
	if !ok || ev != "PermissionRequest" || tool != "Bash" || cwd != "/home/u/repo" || !strings.Contains(string(input), "rm -rf node_modules") {
		t.Errorf("claude: %q %q %s %q %v", ev, tool, input, cwd, ok)
	}
	if _, tool, _, _, ok := Request(fixture(t, "codex_permission_request")); !ok || tool != "Bash" {
		t.Errorf("codex: %q %v", tool, ok)
	}
	if _, _, _, _, ok := Request(fixture(t, "claude_stop")); ok {
		t.Error("stop is no request")
	}
	if m := LastMessage(fixture(t, "codex_notify")); m != "Rename complete and verified cargo build succeeds." {
		t.Errorf("codex notify last message = %q", m)
	}
	if p := UserPrompt(fixture(t, "claude_user_prompt_submit")); p == "" {
		t.Error("no prompt")
	}
}

// A token in a turn's last message never reaches Detail, which the sidebar
// and desktop notifications show, even where the summary is cut.
func TestSummaryRedacts(t *testing.T) {
	token := "ghp_" + strings.Repeat("Q", 36)
	for _, msg := range []string{
		"Pushed with " + token + " as asked.",
		strings.Repeat("word ", summaryLen/5-6) + "export GITHUB_TOKEN=" + token + " and more after it",
	} {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s", "last_assistant_message": msg})
		next, ok := Derive(&model.Activity{State: model.StateWorking}, model.ProviderClaude, b, time.Now())
		if !ok || next.State != model.StateCompleted || strings.Contains(next.Detail, "QQQQ") || strings.Contains(next.Detail, "ghp_") {
			t.Errorf("Detail %q", next.Detail)
		}
	}
	if got := Summary("deployed with key exact-known-1234", "exact-known-1234"); strings.Contains(got, "exact-known") {
		t.Errorf("known key in summary: %q", got)
	}
}

func TestDerivePi(t *testing.T) {
	const none = model.AgentState("")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		payload string
		prev    model.AgentState
		ok      bool
		want    model.AgentState
		detail  string
	}{
		{`{"event":"session_shutdown","session_id":"old"}`, model.StateWorking, false, none, ""}, // a late report from the previous session
		{`{"event":"session_shutdown","session_id":"s1"}`, model.StateWorking, true, none, ""},
		{`{"event":"session_start","session_id":"s1"}`, none, false, none, ""},
		{`{"event":"before_agent_start","prompt":"fix it"}`, none, true, model.StateWorking, ""},
		{`{"event":"agent_start"}`, model.StateCompleted, true, model.StateWorking, ""},
		{`{"event":"agent_start"}`, model.StateWorking, false, none, ""},
		{`{"event":"tool_call","tool_name":"bash"}`, model.StateWorking, true, model.StateWorking, "bash"},
		{`{"event":"agent_settled","stop_reason":"stop","message":"Done: tests pass."}`, model.StateWorking, true, model.StateCompleted, "Done: tests pass."},
		{`{"event":"agent_settled"}`, model.StateWorking, true, model.StateCompleted, ""},
		{`{"event":"agent_settled","stop_reason":"error","error":"429 rate limited","message":""}`, model.StateWorking, true, model.StateError, "429 rate limited"},
		{`{"event":"agent_settled","stop_reason":"aborted"}`, model.StateWorking, true, none, ""},
		{`{"event":"session_shutdown"}`, model.StateCompleted, true, none, ""},
		{`{"event":"session_shutdown"}`, none, false, none, ""},
		{`{"event":"turn_start"}`, model.StateWorking, false, none, ""},
	}
	for _, tt := range tests {
		var prev *model.Activity
		if tt.prev != none {
			prev = &model.Activity{PaneID: "p1", Provider: model.ProviderPi, SessionID: "s1", State: tt.prev}
		}
		next, ok := Derive(prev, model.ProviderPi, []byte(tt.payload), now)
		if ok != tt.ok || next.State != tt.want || next.Detail != tt.detail {
			t.Errorf("%s from %q: got (%q, %q, %v), want (%q, %q, %v)", tt.payload, tt.prev, next.State, next.Detail, ok, tt.want, tt.detail, tt.ok)
		}
		if ok && next.State != none && next.Provider != model.ProviderPi {
			t.Errorf("%s: provider %q", tt.payload, next.Provider)
		}
	}
	if got := SessionID(model.ProviderPi, []byte(`{"event":"session_start","session_id":"s1"}`)); got != "s1" {
		t.Errorf("SessionID = %q", got)
	}
	for _, b := range []string{
		`{"event":"session_start","session_id":""}`,
		`{"event":"session_start","session_id":"s1","ephemeral":true}`,
		`{"event":"session_shutdown","session_id":"s1"}`,
	} {
		if got := SessionID(model.ProviderPi, []byte(b)); got != "" {
			t.Errorf("SessionID(%s) = %q, want none", b, got)
		}
	}
	if got := SessionID(model.ProviderPi, []byte(`{"event":"tool_call","session_id":"s1","tool_name":"bash"}`)); got != "s1" {
		t.Errorf("tool_call SessionID = %q", got)
	}
	if got := Prompt(model.ProviderPi, []byte(`{"event":"before_agent_start","prompt":"fix the login"}`)); got != "fix the login" {
		t.Errorf("Prompt = %q", got)
	}
	if got := Prompt(model.ProviderPi, []byte(`{"event":"agent_start","prompt":"x"}`)); got != "" {
		t.Errorf("Prompt of agent_start = %q", got)
	}
	if got := UserPrompt([]byte(`{"event":"before_agent_start","prompt":"fix it"}`)); got != "fix it" {
		t.Errorf("UserPrompt = %q", got)
	}
	if got := LastMessage([]byte(`{"event":"agent_settled","stop_reason":"stop","message":"done"}`)); got != "done" {
		t.Errorf("LastMessage = %q", got)
	}
	// Completed and error Details go through the same redaction as Claude's and Codex's.
	token := "ghp_" + strings.Repeat("Q", 36)
	for _, fields := range []map[string]any{
		{"stop_reason": "stop", "message": "Pushed with " + token},
		{"stop_reason": "error", "error": "401 for Pushed with " + token},
		{"stop_reason": "error", "message": "Pushed with " + token},
	} {
		fields["event"] = "agent_settled"
		b, _ := json.Marshal(fields)
		if next, ok := Derive(&model.Activity{State: model.StateWorking}, model.ProviderPi, b, now); !ok || strings.Contains(next.Detail, "QQQQ") || !strings.Contains(next.Detail, "Pushed with") {
			t.Errorf("pi Detail %q for %v", next.Detail, fields["stop_reason"])
		}
	}
	if rt, remove := PiReport([]byte(`{"event":"session_shutdown","runtime":"r1"}`)); rt != "r1" || !remove {
		t.Errorf("PiReport(shutdown) = %q, %v", rt, remove)
	}
	if rt, remove := PiReport([]byte(`{"event":"agent_start","runtime":"r1"}`)); rt != "r1" || remove {
		t.Errorf("PiReport(agent_start) = %q, %v", rt, remove)
	}
	if rt, _ := PiReport(fixture(t, "claude_stop")); rt != "" {
		t.Errorf("PiReport(claude) = %q", rt)
	}
}

func TestPiExtension(t *testing.T) {
	for _, bin := range []string{"/usr/local/bin/pitwall", `/opt/my "odd" bin/it's \pitwall`, `C:\Users\Jane Doe\pitwall.exe`, "/tmp/a\u2028b/pitwall"} {
		src := PiExtension(bin)
		q, _ := json.Marshal(bin)
		if !strings.Contains(string(src), "const bin = "+string(q)+";\n") || strings.Contains(string(src), piBinToken) {
			t.Errorf("%s: bin not substituted as a quoted literal", bin)
		}
		var back string
		line := strings.SplitN(strings.SplitN(string(src), "const bin = ", 2)[1], ";\n", 2)[0]
		if err := json.Unmarshal([]byte(line), &back); err != nil || back != bin {
			t.Errorf("%s: literal reads back as %q (%v)", bin, back, err)
		}
		if !IsPiExtension(src) {
			t.Errorf("%s: IsPiExtension false for generated file", bin)
		}
		if IsPiExtension(append(src, '\n')) || IsPiExtension([]byte(strings.Replace(string(src), "5000", "50", 1))) {
			t.Errorf("%s: IsPiExtension true for an edited file", bin)
		}
	}
	src := string(PiExtension("pitwall"))
	for _, s := range []string{`spawn(bin, ["hook", "pi"]`, `kill("SIGKILL")`, "const limit = 32;", "pending.splice(0)", "runtime = randomUUID()", "{ event, runtime,", "Array.from(", "kill(current);\n\t});", `"agent_settled"`, `"session_shutdown"`, `"tool_call"`} {
		if !strings.Contains(src, s) {
			t.Errorf("extension lacks %s", s)
		}
	}
	if i, j := strings.Index(src, "if (!process.env.PITWALL_PANE) return;"), strings.Index(src, "pi.on("); i < 0 || j < i {
		t.Error("extension registers handlers outside a pitwall pane")
	}
	if strings.Contains(src, "shell: true") || strings.Contains(src, "event.input") {
		t.Error("extension uses a shell or sends tool arguments")
	}
	if IsPiExtension(nil) || IsPiExtension([]byte("export default function () {}\n")) {
		t.Error("IsPiExtension true for a foreign file")
	}
}
