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
