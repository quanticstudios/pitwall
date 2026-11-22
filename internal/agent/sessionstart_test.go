package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestIsSessionStart(t *testing.T) {
	cases := []struct {
		provider model.Provider
		payload  string
		want     bool
	}{
		{model.ProviderClaude, `{"hook_event_name":"SessionStart","source":"startup"}`, true},
		{model.ProviderCodex, `{"hook_event_name":"SessionStart","session_id":"abc"}`, true},
		{model.ProviderClaude, `{"hook_event_name":"SessionStart","transcript_path":null}`, false},
		{model.ProviderClaude, `{"hook_event_name":"Stop"}`, false},
		{model.ProviderGemini, `{"hook_event_name":"SessionStart"}`, false},
		{model.ProviderClaude, `not-json`, false},
	}
	for _, tc := range cases {
		if got := IsSessionStart(tc.provider, []byte(tc.payload)); got != tc.want {
			t.Errorf("IsSessionStart(%s, %s) = %v, want %v", tc.provider, tc.payload, got, tc.want)
		}
	}
}

func TestSessionStartOutput(t *testing.T) {
	raw := SessionStartOutput("p7c1", "#3", "fix-login", "work")
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid json %q: %v", raw, err)
	}
	if parsed.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q", parsed.HookSpecificOutput.HookEventName)
	}
	ctx := parsed.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"@p7c1", `tab #3 "fix-login"`, `session "work"`, "pitwall commands"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context %q missing %q", ctx, want)
		}
	}

	fallback := SessionStartContext("p7c1", "", "", "")
	if !strings.Contains(fallback, "@p7c1") || !strings.Contains(fallback, "pitwall commands") {
		t.Fatalf("fallback context = %q", fallback)
	}
}
