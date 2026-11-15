package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// init answers GuardHooked's probe as a working `pitwall guard` does, for
// the tests that name this test binary as pitwall.
func init() {
	if len(os.Args) == 2 && os.Args[1] == "guard" {
		io.Copy(io.Discard, os.Stdin)
		os.Exit(2)
	}
}

// TestGuardHooked: the guard holds only where the settings `pitwall hooks
// install` writes run an existing pitwall on every PreToolUse.
func TestGuardHooked(t *testing.T) {
	dir := t.TempDir()
	// The probe executes the fixture directly, including on Windows. Copy
	// this test executable so the apostrophe and space still exercise quoting.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "it's pitwall"+filepath.Ext(exe))
	if err := os.WriteFile(bin, body, 0o700); err != nil {
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
		{"gone binary", model.ProviderClaude, "settings.json", settings(ClaudeHooks(filepath.Join(dir, "gone")), ""), "gone cannot run"},
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

// TestGuardProbe: a guarded start needs a hook binary that runs and denies
// a command every guard denies; a file that merely exists is not enough.
func TestGuardProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures are shell scripts")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	denies := script("denies", "#!/bin/sh\ncase \"$1 $(cat)\" in guard*'\"git push origin main\"'*) exit 2;; esac\nexit 0\n")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(denies, link); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "folder")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, bin, want string // want "" for nil
	}{
		{"denies", denies, ""},
		{"link to one that denies", link, ""},
		{"missing interpreter", script("nointerp", "#!/nonexistent/sh\nexit 2\n"), "cannot run"},
		{"exits 0", script("allows", "#!/bin/sh\nexit 0\n"), "allowed git push origin main"},
		{"exits 1", script("fails", "#!/bin/sh\nexit 1\n"), "exited 1"},
		{"killed", script("killed", "#!/bin/sh\nkill -9 $$\n"), "died"},
		{"hangs", script("hangs", "#!/bin/sh\nexec sleep 30\n"), "did not answer in 3s"},
		{"directory", folder, "cannot run"},
		{"missing", filepath.Join(dir, "gone"), "cannot run"},
	} {
		for _, a := range []struct {
			provider model.Provider
			file     string
			body     func(bin string) []byte
		}{
			{model.ProviderClaude, "settings.json", func(bin string) []byte { return []byte(`{"hooks": ` + string(ClaudeHooks(bin)) + `}`) }},
			{model.ProviderPi, "extensions/pitwall.ts", PiExtension},
		} {
			t.Run(c.name+"/"+string(a.provider), func(t *testing.T) {
				t.Parallel()
				cfg := t.TempDir()
				path := filepath.Join(cfg, a.file)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, a.body(c.bin), 0o600); err != nil {
					t.Fatal(err)
				}
				err := GuardHooked(a.provider, cfg)
				switch {
				case c.want == "" && err != nil:
					t.Fatalf("GuardHooked = %v, want nil", err)
				case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), c.bin)):
					t.Fatalf("GuardHooked = %v, want %q naming %s", err, c.want, c.bin)
				}
			})
		}
	}
}

// TestGuardOldGenerated: a copy from v0.1.3 to v0.2.13, whose guard allowed
// what it could not check, refuses a guarded start until hooks install,
// which recognizes it as pitwall's, replaces it.
func TestGuardOldGenerated(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(bin)
	for _, a := range []struct {
		provider  model.Provider
		file      string
		fixture   string
		generated func([]byte) bool
	}{
		{model.ProviderPi, "extensions/pitwall.ts", "testdata/pi_extension_v0.1.3.ts", IsPiExtension},
		{model.ProviderOpenCode, "plugins/pitwall.js", "testdata/opencode_plugin_v0.1.3.js", IsOpenCodePlugin},
	} {
		old, err := os.ReadFile(a.fixture)
		if err != nil {
			t.Fatal(err)
		}
		old = []byte(strings.Replace(string(old), piBinToken, string(quoted), 1))
		if !a.generated(old) {
			t.Fatalf("%s: hooks install would not replace the old copy", a.provider)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, a.file)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, old, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := GuardHooked(a.provider, dir); err == nil || !strings.Contains(err.Error(), "install the current unedited copy") {
			t.Fatalf("%s: GuardHooked = %v, want the reinstall message", a.provider, err)
		}
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
