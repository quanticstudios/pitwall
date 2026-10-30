package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
)

func TestProfileStatusline(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for i, name := range []string{"", "work", "personal"} {
		t.Setenv("PITWALL_PROFILE", name)
		in := `{"rate_limits":{"five_hour":{"used_percentage":` + string(rune('1'+i)) + `0}}}`
		var out bytes.Buffer
		if code := runStatusline(nil, strings.NewReader(in), &out, &out); code != 0 {
			t.Fatal(code)
		}
	}
	for i, name := range []string{"", "work", "personal"} {
		data, err := os.ReadFile(flow.ClaudeLimitsPath(stateDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		var l flow.Limit
		if json.Unmarshal(data, &l) != nil || l.Profile != name || l.Windows[0].Used != float64((i+1)*10) {
			t.Fatalf("wrong account saved: %s", data)
		}
	}
}

func TestHooksProfileDirectories(t *testing.T) {
	home := hooksHome(t)
	claude, codex := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	var out bytes.Buffer
	if err := runHooks([]string{"install", "--statusline"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(claude, "settings.json"), filepath.Join(codex, "hooks.json"), filepath.Join(claude, "skills", "pitwall", "SKILL.md"), filepath.Join(codex, "skills", "pitwall", "SKILL.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".codex")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("profile install wrote default directory %s", dir)
		}
	}
	if agent.ClaudeDir(home) != claude || agent.CodexDir(home) != codex {
		t.Fatal("profile directory helper ignored overrides")
	}
}
