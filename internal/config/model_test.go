package config

import (
	"slices"
	"testing"
)

func TestWithModel(t *testing.T) {
	for _, tc := range []struct {
		name, agent, selection string
		args, want             []string
	}{
		{"claude alias", "claude", "opus", []string{"--permission-mode", "plan", "go"}, []string{"--model", "opus", "--permission-mode", "plan", "go"}},
		{"claude full ID", "claude", "claude-custom-20261030", nil, []string{"--model", "claude-custom-20261030"}},
		{"codex", "codex", "gpt-custom", []string{"-s", "read-only", "go"}, []string{"-m", "gpt-custom", "-s", "read-only", "go"}},
		{"windows codex", "codex.exe", "gpt-custom", nil, []string{"-m", "gpt-custom"}},
		{"other agent", "gemini", "custom", []string{"-i", "go"}, []string{"--model", "custom", "-i", "go"}},
		{"unset preserves preset", "claude", "", []string{"--model", "opus", "go"}, []string{"--model", "opus", "go"}},
		{"default", "claude", "default", []string{"go"}, []string{"go"}},
		{"default clears preset", "claude", "default", []string{"--model", "opus", "--effort", "high", "go"}, []string{"--effort", "high", "go"}},
		{"replace preset", "codex", " gpt-custom ", []string{"--model=old", "-m", "older", "-c", "x=1", "go"}, []string{"-m", "gpt-custom", "-c", "x=1", "go"}},
		{"after separator", "claude", "sonnet", []string{"--", "--model", "literal"}, []string{"--model", "sonnet", "--", "--model", "literal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.args)
			got := WithModel(tc.agent, tc.args, tc.selection)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if !slices.Equal(tc.args, before) {
				t.Fatal("mutated arguments")
			}
			if len(got) > 0 {
				got[0] = "changed"
			}
			if !slices.Equal(tc.args, before) {
				t.Fatal("result aliases arguments")
			}
		})
	}
}
