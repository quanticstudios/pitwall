package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		{"claude_stop", claude, model.StateWorking, true, model.StateCompleted, ""},
		{"claude_stop_question", claude, model.StateWorking, true, model.StateCompleted, ""},
		{"claude_stop_background", claude, model.StateWorking, false, none, ""}, // stays working
		{"claude_stop_background", claude, model.StatePendingApproval, true, model.StateWorking, ""},
		{"claude_stop_failure", claude, model.StateWorking, true, model.StateError, "API Error: Rate limit reached"},
		{"claude_subagent_stop", claude, model.StateWorking, false, none, ""},
		{"claude_subagent_stop", claude, model.StateCompleted, false, none, ""},
		{"claude_session_end", claude, model.StateCompleted, true, none, ""},
		{"claude_session_end", claude, none, false, none, ""},
		{"codex_notify", codex, model.StateWorking, true, model.StateCompleted, ""},
		{"codex_user_prompt_submit", codex, none, true, model.StateWorking, ""},
		{"codex_pre_tool_use", codex, model.StatePendingApproval, true, model.StateWorking, ""},
		{"codex_permission_request", codex, model.StateWorking, true, model.StatePendingApproval, "Allow network access to push?"},
		{"codex_stop", codex, model.StateWorking, true, model.StateCompleted, ""},
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
		if h.Type != "command" || h.Command != `'/opt/my bin/pitwall' hook claude` || h.Timeout == 0 {
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
