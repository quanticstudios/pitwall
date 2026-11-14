package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestGuardHooked: the guard holds only where the settings `pitwall hooks
// install` writes run an existing pitwall on every PreToolUse.
func TestGuardHooked(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "it's pitwall")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := func(hooks []byte, extra string) string {
		return `{` + extra + `"hooks": ` + string(hooks) + `}`
	}
	unclosed, err := json.Marshal("'" + bin + " hook claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		provider model.Provider
		file     string // "" writes none
		body     string
		want     string // "" for nil
	}{
		{"claude", model.ProviderClaude, "settings.json", settings(ClaudeHooks(bin), ""), ""},
		{"codex", model.ProviderCodex, "hooks.json", settings(CodexHooks(bin), ""), ""},
		{"no file", model.ProviderClaude, "", "", "no settings.json"},
		{"codex hooks in claude's file", model.ProviderClaude, "settings.json", settings(CodexHooks(bin), ""), "no PreToolUse hook"},
		{"hooks off", model.ProviderClaude, "settings.json", settings(ClaudeHooks(bin), `"disableAllHooks": true, `), "disableAllHooks"},
		{"gone binary", model.ProviderClaude, "settings.json", settings(ClaudeHooks(filepath.Join(dir, "gone")), ""), "no PreToolUse hook"},
		{"some tools", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"hooks": [`, `"matcher": "Bash", "hooks": [`), "no PreToolUse hook"},
		{"conditional", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"type": "command"`, `"type": "command", "if": "Bash(git *)"`), "no PreToolUse hook"},
		{"async", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"type": "command"`, `"type": "command", "async": true`), "no PreToolUse hook"},
		{"async rewake", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"type": "command"`, `"type": "command", "asyncRewake": true`), "no PreToolUse hook"},
		{"exec form", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"type": "command"`, `"type": "command", "args": []`), "no PreToolUse hook"},
		{"unclosed quote", model.ProviderClaude, "settings.json", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":` + string(unclosed) + `}]}]}}`, "no PreToolUse hook"},
		{"wrong shell", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"type": "command"`, `"type": "command", "shell": "powershell"`), "no PreToolUse hook"},
		{"no hooks", model.ProviderClaude, "settings.json", `{"model": "opus"}`, "no PreToolUse hook"},
		{"broken", model.ProviderClaude, "settings.json", `{`, "settings.json"},
		{"unsupported", model.ProviderAider, "", "", "does not cover"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := t.TempDir()
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(cfg, c.file), []byte(c.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := GuardHooked(c.provider, cfg)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("GuardHooked = %v, want nil", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("GuardHooked = %v, want %q", err, c.want)
			}
		})
	}
}

func TestGuardGenerated(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		provider model.Provider
		file     string
		generate func(string) []byte
	}{
		{model.ProviderPi, "extensions/pitwall.ts", PiExtension},
		{model.ProviderOmp, "extensions/pitwall.ts", OmpExtension},
		{model.ProviderOpenCode, "plugins/pitwall.js", OpenCodePlugin},
	} {
		for _, tc := range []struct {
			name string
			body []byte
			want bool
		}{
			{"current", a.generate(bin), true},
			{"missing", nil, false},
			{"edited", append(a.generate(bin), '\n'), false},
			{"missing executable", a.generate(filepath.Join(t.TempDir(), "gone")), false},
			{"without guard", []byte(strings.ReplaceAll(string(a.generate(bin)), `spawn(bin, ["guard"]`, `spawn(bin, ["hook"]`)), false},
		} {
			t.Run(string(a.provider)+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if tc.body != nil {
					path := filepath.Join(dir, a.file)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, tc.body, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				err := GuardHooked(a.provider, dir)
				if (err == nil) != tc.want {
					t.Fatalf("GuardHooked = %v, want success %v", err, tc.want)
				}
			})
		}
	}
	// hooks install intentionally shares pi's copy with omp when
	// PI_CODING_AGENT_DIR names the same directory for both agents.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "extensions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions", "pitwall.ts"), PiExtension(bin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := GuardHooked(model.ProviderOmp, dir); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("testdata/pi_extension_alpha7.ts")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(bin)
	old = []byte(strings.Replace(string(old), piBinToken, string(quoted), 1))
	if !IsPiExtension(old) {
		t.Fatal("fixture is not a recognized historical pi extension")
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions", "pitwall.ts"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := GuardHooked(model.ProviderPi, dir); err == nil {
		t.Fatal("accepted a recognized pre-guard extension")
	}
}

func TestGuardOpenCodeVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"1.4.0\n", true}, {"1.15.2", true}, {"v1.4.1-preview", true},
		{"2.0.0", false}, {"1.3.9", false}, {"unknown", false},
		{"1.4.invalid", false}, {"1.4.0 extra", false}, {"1.4.0.1", false},
	} {
		if got := openCodeGuardedVersion(tc.version); got != tc.want {
			t.Errorf("version %q = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestGuardOpenCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a shell script")
	}
	dir := t.TempDir()
	program := filepath.Join(dir, "opencode")
	// The version probe must see the same working directory and env as
	// the requested agent, and must pass only --version.
	if err := os.WriteFile(program, []byte("#!/bin/sh\n[ \"$1\" = --version ] && [ \"$#\" = 1 ] && [ -f marker ] || exit 1\nprintf '%s\\n' \"$GUARD_TEST_VERSION\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := GuardOpenCode(program, dir, []string{"GUARD_TEST_VERSION=1.4.0"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ program, cwd, version string }{
		{program, dir, "2.0.0"}, {program, dir, "unknown"},
		{program, t.TempDir(), "1.4.0"}, {filepath.Join(dir, "gone"), dir, "1.4.0"},
	} {
		if err := GuardOpenCode(tc.program, tc.cwd, []string{"GUARD_TEST_VERSION=" + tc.version}); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}
