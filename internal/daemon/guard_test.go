package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// hookedDir has current pitwall hooks, extensions and a plugin, all using
// this test binary.
func hookedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{
		"settings.json":         []byte(`{"hooks": ` + string(agent.ClaudeHooks(self)) + `}`),
		"hooks.json":            []byte(`{"hooks": ` + string(agent.CodexHooks(self)) + `}`),
		"extensions/pitwall.ts": agent.PiExtension(self),
		"plugins/pitwall.js":    agent.OpenCodePlugin(self),
	} {
		path := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGuardGateExtensions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"PI_CODING_AGENT_DIR", "OPENCODE_CONFIG_DIR", "XDG_CONFIG_HOME", "PITWALL_GUARD"} {
		t.Setenv(key, "")
	}
	hooked, empty := hookedDir(t), t.TempDir()
	for _, a := range []struct{ name, key, defaultDir, file string }{
		{"pi", "PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"), "extensions/pitwall.ts"},
		{"omp", "PI_CODING_AGENT_DIR", filepath.Join(home, ".omp", "agent"), "extensions/pitwall.ts"},
		{"opencode", "OPENCODE_CONFIG_DIR", filepath.Join(home, ".config", "opencode"), "plugins/pitwall.js"},
	} {
		program := a.name
		if a.name == "opencode" {
			if runtime.GOOS == "windows" {
				continue
			} // version fixture is a shell script
			program = filepath.Join(t.TempDir(), "opencode")
			if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' \"${GUARD_TEST_VERSION:-1.4.0}\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(filepath.Join(hooked, a.file))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(a.defaultDir, a.file)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(home, hooked)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, daemon string
			cmd, env     []string
			want         bool
		}{
			{"default", "", []string{program}, nil, true},
			{"request", "", []string{program}, []string{a.key + "=" + hooked}, true},
			{"daemon", hooked, []string{program}, nil, true},
			{"missing overrides daemon", hooked, []string{program}, []string{a.key + "=" + empty}, false},
			{"relative", "", []string{program}, []string{a.key + "=" + relative}, true},
			{"tilde", "", []string{program}, []string{a.key + "=~/" + filepath.ToSlash(relative)}, true},
			{"no extensions", "", []string{program, "--no-extensions"}, []string{a.key + "=" + hooked}, false},
			{"no extensions short", "", []string{program, "-ne"}, []string{a.key + "=" + hooked}, false},
			{"OpenCode2", "", []string{program}, []string{a.key + "=" + hooked, "GUARD_TEST_VERSION=2.0.0"}, false},
			{"unknown version", "", []string{program}, []string{a.key + "=" + hooked, "GUARD_TEST_VERSION=unknown"}, false},
		} {
			if a.name == "opencode" && (strings.HasPrefix(tc.name, "no extensions") || tc.name == "tilde") {
				continue
			}
			if a.name != "opencode" && (tc.name == "OpenCode2" || tc.name == "unknown version") {
				continue
			}
			t.Run(a.name+"/"+tc.name, func(t *testing.T) {
				t.Setenv(a.key, tc.daemon)
				f := &fakes{statsCalls: map[string]int{}}
				d, err := NewWith(f.options())
				if err != nil {
					t.Fatal(err)
				}
				err = d.handle(context.Background(), proto.NewSession{Cwd: home, Cmd: tc.cmd, Env: append([]string{"PITWALL_GUARD=1"}, tc.env...)})
				if (err == nil) != tc.want {
					t.Fatalf("start = %v, want success %v", err, tc.want)
				}
				if !tc.want && (!errors.Is(err, errUnguarded) || len(snapshot(d).Panes) != 0 || len(f.panes) != 0) {
					t.Fatalf("refusal left a pane: %v", err)
				}
			})
		}
	}
}

func TestGuardGateOpenCodeXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("version fixture is a shell script")
	}
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(xdg, "opencode", "plugins", "pitwall.js")
	if err := os.MkdirAll(filepath.Dir(plugin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin, agent.OpenCodePlugin(self), 0o600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(home, "opencode")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 1.4.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{xdg, true}, {t.TempDir(), false}, {"relative-config", false},
	} {
		err := guardGate([]string{program}, []string{"PITWALL_GUARD=1", "XDG_CONFIG_HOME=" + tc.value}, home)
		if (err == nil) != tc.want {
			t.Fatalf("XDG_CONFIG_HOME=%q: %v, want success %v", tc.value, err, tc.want)
		}
	}
}

// The config directory is relative to the pane's cwd and default home,
// including environment overrides, rather than the daemon's.
func TestGuardGateLaunchDirectory(t *testing.T) {
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	t.Setenv("PITWALL_GUARD", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	hooked := hookedDir(t)
	t.Chdir(filepath.Dir(hooked))
	empty := t.TempDir()
	for _, a := range []struct{ cmd, key, dir, file string }{
		{"claude", "CLAUDE_CONFIG_DIR", ".claude", "settings.json"},
		{"codex", "CODEX_HOME", ".codex", "hooks.json"},
	} {
		data, err := os.ReadFile(filepath.Join(hooked, a.file))
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(filepath.Dir(hooked), a.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, a.file), data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, cwd, config string
			home              string
			want              bool
		}{
			{"daemon relative hooks", empty, filepath.Base(hooked), "", false},
			{"pane relative hooks", filepath.Dir(hooked), filepath.Base(hooked), "", true},
			{"pane home without hooks", empty, "", empty, false},
			{"pane home with hooks", empty, "", filepath.Dir(hooked), true},
		} {
			t.Run(a.cmd+"/"+tc.name, func(t *testing.T) {
				t.Setenv(homeKey, filepath.Dir(hooked))
				vars := []string{"PITWALL_GUARD=1", a.key + "=" + tc.config}
				if tc.home != "" {
					vars = append(vars, homeKey+"="+tc.home)
				}
				f := &fakes{statsCalls: map[string]int{}}
				d, err := NewWith(f.options())
				if err != nil {
					t.Fatal(err)
				}
				err = d.handle(context.Background(), proto.NewSession{Cwd: tc.cwd, Cmd: []string{a.cmd}, Env: vars})
				if tc.want && err != nil {
					t.Fatal(err)
				}
				if !tc.want && !errors.Is(err, errUnguarded) {
					t.Fatalf("start = %v, want guard refusal", err)
				}
			})
		}
	}
}

// TestGuardGate: a pane with the guard on starts only for Claude Code or
// Codex with pitwall's hooks in the config dir its final environment
// names, else the daemon's, else home's.
func TestGuardGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PITWALL_GUARD", "")
	hooked, empty := hookedDir(t), t.TempDir()
	casingWant := ""
	if runtime.GOOS == "windows" {
		casingWant = empty
	}
	for _, c := range []struct {
		name   string
		daemon string // the daemon's own CLAUDE_CONFIG_DIR
		cmd    []string
		env    []string
		want   string // "" starts
	}{
		{"no hooks anywhere", "", []string{"claude"}, []string{"PITWALL_GUARD=1"}, "no settings.json"},
		{"hooks in another dir", hooked, []string{"/usr/bin/claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + empty}, empty},
		{"a later entry wins", "", []string{"claude"}, []string{"CLAUDE_CONFIG_DIR=" + hooked, "PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + empty}, empty},
		{"environment name casing", "", []string{"claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked, "claude_config_dir=" + empty}, casingWant},
		{"another program", hooked, []string{"deepagent"}, []string{"PITWALL_GUARD=1"}, "not deepagent"},
		{"a shell", hooked, nil, []string{"PITWALL_GUARD=1"}, "not a shell"},
		{"guard set to anything", hooked, []string{"aider"}, []string{"PITWALL_GUARD=yes"}, "not aider"},
		{"hooks in the env's dir", "", []string{"claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}, ""},
		{"bare Claude", hooked, []string{"claude", "--bare"}, []string{"PITWALL_GUARD=1"}, "--bare"},
		{"safe mode Claude", hooked, []string{"claude", "--safe-mode"}, []string{"PITWALL_GUARD=1"}, "--safe-mode"},
		{"Claude without user settings", hooked, []string{"claude", "--setting-sources=project,local"}, []string{"PITWALL_GUARD=1"}, "--setting-sources"},
		{"Claude settings override", hooked, []string{"claude", "--settings", `{"disableAllHooks":true}`}, []string{"PITWALL_GUARD=1"}, "--settings"},
		{"simple Claude", hooked, []string{"claude"}, []string{"PITWALL_GUARD=1", "CLAUDE_CODE_SIMPLE=1"}, "CLAUDE_CODE_SIMPLE"},
		{"Claude with user settings", hooked, []string{"claude", "--setting-sources", "user,project", "--", "--bare"}, []string{"PITWALL_GUARD=1"}, ""},
		{"hooks in the daemon's dir", hooked, []string{"claude"}, []string{"PITWALL_GUARD=1"}, ""},
		{"codex", "", []string{"codex"}, []string{"PITWALL_GUARD=1", "CODEX_HOME=" + hooked}, ""},
		{"codex without", "", []string{"codex"}, []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}, filepath.Join(home, ".codex")},
		{"guard off", "", []string{"deepagent"}, []string{"PITWALL_GUARD="}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", c.daemon)
			f := &fakes{statsCalls: map[string]int{}}
			d, err := NewWith(f.options())
			if err != nil {
				t.Fatal(err)
			}
			err = d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir(), Cmd: c.cmd, Env: c.env})
			f.mu.Lock()
			started := len(f.panes)
			f.mu.Unlock()
			if c.want == "" {
				if err != nil || started != 1 {
					t.Fatalf("err %v, %d panes started; want one", err, started)
				}
				return
			}
			if !errors.Is(err, errUnguarded) || !strings.Contains(err.Error(), c.want) || started != 0 {
				t.Fatalf("err %v, %d panes started; want %q and none", err, started, c.want)
			}
			if st := snapshot(d); len(st.Panes) != 0 || len(st.Workspaces) != 0 {
				t.Fatalf("a refused start left %d panes, %d tabs", len(st.Panes), len(st.Workspaces))
			}
		})
	}
}

// TestGuardGateRestore: a guarded agent whose hooks went missing is not
// resumed after a restart, and the window says why.
func TestGuardGateRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	hooked := hookedDir(t)
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	must(t, d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"claude"}, Env: []string{"PITWALL_GUARD=1", "CLAUDE_CONFIG_DIR=" + hooked}}))
	f.saved = snapshot(d)
	f.saved.Panes[0].Provider, f.saved.Panes[0].SessionID = model.ProviderClaude, "s"
	if err := os.Remove(filepath.Join(hooked, "settings.json")); err != nil {
		t.Fatal(err)
	}
	d, err = NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	st := snapshot(d)
	if len(st.Panes) != 0 || len(f.panes) != 1 || !strings.Contains(st.Notice, "guarded tab not started") {
		t.Fatalf("restored %d panes (%d started), notice %q", len(st.Panes), len(f.panes), st.Notice)
	}
}
