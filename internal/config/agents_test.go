package config

import (
	"slices"
	"strings"
	"testing"
)

func TestAgentPresets(t *testing.T) {
	p := write(t, t.TempDir(), "config.toml", `[agents]
guard_workers = true
guard_deny = ["npm publish *"]

[[agents.preset]]
name = "opus high"
agent = "claude"
args = ["--model", "opus", "--effort", "high"]
env = ["FOO=bar"]
guard = true

[[agents.preset]]
name = "opus high"
agent = "codex"

[[agents.preset]]
name = "no agent"

[[agents.preset]]
name = "bad env"
agent = "pi"
env = ["FOO"]
`)
	s, probs := LoadFile(p)
	if len(s.Presets) != 1 {
		t.Fatalf("presets = %+v", s.Presets)
	}
	got, ok := s.FindPreset("opus high")
	if !ok || got.Agent != "claude" || !slices.Equal(got.Args, []string{"--model", "opus", "--effort", "high"}) || !slices.Equal(got.Env, []string{"FOO=bar"}) || !got.Guard {
		t.Errorf("preset = %+v", got)
	}
	if _, ok := s.FindPreset("bad env"); ok {
		t.Error("a preset with a problem was kept")
	}
	if !s.GuardWorkers || !slices.Equal(s.GuardDeny, []string{"npm publish *"}) {
		t.Errorf("guard_workers %v, guard_deny %q", s.GuardWorkers, s.GuardDeny)
	}
	var msgs []string
	for _, pr := range probs {
		msgs = append(msgs, pr.Msg)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{`a second preset is named "opus high"`, `preset "no agent" needs an agent`, `preset "bad env": env takes`} {
		if !strings.Contains(all, want) {
			t.Errorf("problems %q lack %q", msgs, want)
		}
	}
}

func TestPresetArgs(t *testing.T) {
	for _, tc := range []struct {
		agent, model, effort string
		rest, args           []string
	}{
		{"claude", "opus", "high", []string{"--verbose"}, []string{"--model", "opus", "--effort", "high", "--verbose"}},
		{"codex", "gpt-x", "xhigh", nil, []string{"-m", "gpt-x", "-c", "model_reasoning_effort=xhigh"}},
		{"pi", "anthropic/claude-x", "high", nil, []string{"--model", "anthropic/claude-x:high"}},
		{"opencode", "openai/gpt-x", "low", []string{"--port", "4096"}, []string{"--model", "openai/gpt-x#low", "--port", "4096"}},
		{"opencode", "openai/gpt-x", "", nil, []string{"--model", "openai/gpt-x"}},
		{"claude", "", "max", nil, []string{"--effort", "max"}},
		{"gemini", "", "", []string{"-m", "x"}, []string{"-m", "x"}},
	} {
		args := PresetArgs(tc.agent, tc.model, tc.effort, tc.rest)
		if !slices.Equal(args, tc.args) {
			t.Errorf("%s args %q, want %q", tc.agent, args, tc.args)
		}
		m, e, rest := SplitArgs(tc.agent, args)
		if m != tc.model || e != tc.effort || !slices.Equal(rest, tc.rest) {
			t.Errorf("%s split %q: %q %q %q", tc.agent, args, m, e, rest)
		}
	}
	// Hand-written args: other spellings parse, and what does not stays in rest.
	for _, tc := range []struct {
		agent         string
		args          []string
		model, effort string
		rest          []string
	}{
		{"codex", []string{"--model", "gpt-x", "-c", `model_reasoning_effort="high"`, "-c", "x=1"}, "gpt-x", "high", []string{"-c", "x=1"}},
		{"/usr/bin/claude", []string{"--effort", "huge", "--model", "opus"}, "opus", "", []string{"--effort", "huge"}},
		{"pi", []string{"--model", "openai/gpt-x:turbo"}, "openai/gpt-x:turbo", "", nil},
		{"opencode", []string{"-m", "a/b#max"}, "a/b", "max", nil},
	} {
		m, e, rest := SplitArgs(tc.agent, tc.args)
		if m != tc.model || e != tc.effort || !slices.Equal(rest, tc.rest) {
			t.Errorf("%s split %q: %q %q %q", tc.agent, tc.args, m, e, rest)
		}
	}
}

func TestSplitShell(t *testing.T) {
	for in, want := range map[string][]string{
		``:                         nil,
		`a  b`:                     {"a", "b"},
		`--x 'a b' "c \"d\"" e\ f`: {"--x", "a b", `c "d"`, "e f"},
		`'' "a\b"`:                 {"", `a\b`},
	} {
		got, err := SplitShell(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("SplitShell(%q) = %q, %v", in, got, err)
		}
		if back, _ := SplitShell(JoinShell(got)); !slices.Equal(back, got) {
			t.Errorf("JoinShell(%q) = %q reads back as %q", got, JoinShell(got), back)
		}
	}
	if _, err := SplitShell(`a "b`); err == nil {
		t.Error("an open quote splits")
	}
}
