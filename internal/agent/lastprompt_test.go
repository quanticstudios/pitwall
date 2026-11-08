package agent

import "testing"

func TestPromptSubmitted(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"hook_event_name":"UserPromptSubmit","prompt":"go"}`, true},
		{`{"hook_event_name":"UserPromptSubmit","prompt":"/clear"}`, true},
		{`{"hook_event_name":"UserPromptSubmit"}`, true},
		{`{"hook_event_name":"BeforeAgent","prompt":"go"}`, true},
		{`{"event":"before_agent_start","prompt":"go"}`, true},
		{`{"hook_event_name":"UserPromptSubmit","transcript_path":null}`, false},
		{`{"hook_event_name":"PostToolUse","prompt":"go"}`, false},
		{`{"hook_event_name":"Stop"}`, false},
		{`invalid`, false},
	} {
		if got := PromptSubmitted([]byte(tc.payload)); got != tc.want {
			t.Errorf("%s: got %v", tc.payload, got)
		}
	}
}
