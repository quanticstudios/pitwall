//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestDaemonRow(t *testing.T) {
	for _, tc := range []struct {
		m    proto.StateMsg
		err  error
		ok   bool
		want string
	}{
		{proto.StateMsg{Level: proto.Level, Build: versionString()}, nil, true, versionString()},
		{proto.StateMsg{Level: proto.Level, Build: "v0.0.1"}, nil, false, "this pitwall is"},
		{proto.StateMsg{Level: proto.Level - 1, Build: versionString()}, nil, false, "this pitwall is"},
		{proto.StateMsg{}, errors.New(proto.Refusal().Message), false, "speaks protocol"},
		{proto.StateMsg{}, errors.New("broken pipe"), false, "broken pipe"},
	} {
		ok, detail, fix := daemonRow(tc.m, tc.err)
		if ok != tc.ok || !strings.Contains(detail, tc.want) || ok != (fix == "") {
			t.Errorf("%+v, %v: %v %q %q", tc.m, tc.err, ok, detail, fix)
		}
	}
}

// doctor fails until pitwall is on PATH and the hooks of the installed
// agents are in place, and skips an agent that is not installed.
func TestDoctor(t *testing.T) {
	home := hooksHome(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PITWALL_SOCKET", sockPath(t, "none.sock"))
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("SSH_CONNECTION", "")
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	doctor := func() (int, string) {
		var b bytes.Buffer
		code := runDoctor(&b)
		return code, b.String()
	}
	code, out := doctor()
	for _, want := range []string{"FAIL pitwall on PATH: not found", "info daemon not running", "FAIL hooks: ~/.claude/settings.json needs: added", "fix: pitwall hooks install", "FAIL guard: no settings.json", "ok   config"} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("exit %d, want 1 and %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, ".codex") {
		t.Errorf("codex is not installed, but:\n%s", out)
	}
	// A host's login check, for the agent here: a note, not a failure.
	if runtime.GOOS != "darwin" && !strings.Contains(out, "info no claude login in "+filepath.Join(home, ".claude")+"; start claude once and log in") {
		t.Errorf("no login note:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	self, _ := os.Executable()
	if err := os.Symlink(self, filepath.Join(bin, "pitwall")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := runHooks([]string{"install"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The hooks run testBin, which does not exist, so the guard's probe cannot run it.
	code, out = doctor()
	fails := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "FAIL") {
			fails++
			if !strings.HasPrefix(l, "FAIL guard: ") || !strings.Contains(l, testBin+" cannot run") {
				t.Errorf("after setup: %s", l)
			}
		}
	}
	if code != 1 || fails != 2 || !strings.Contains(out, "ok   hooks: ~/.claude/skills/pitwall/SKILL.md") {
		t.Errorf("exit %d after setup:\n%s", code, out)
	}
}

// TestDoctorGuard: the guard row is ok where a guarded tab may start.
func TestDoctorGuard(t *testing.T) {
	home := hooksHome(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeHooksTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks": `+string(agent.ClaudeHooks(self))+`}`)
	var rows []string
	hooksRows(home, func(s string) string { return s }, func(ok bool, what, detail, fix string) {
		if what == "guard" {
			rows = append(rows, fmt.Sprint(ok, " ", detail))
		}
	})
	if want := "true claude in " + filepath.Join(home, ".claude"); len(rows) != 1 || rows[0] != want {
		t.Errorf("guard rows %q, want %q", rows, want)
	}
}

func TestDoctorGuardExtensions(t *testing.T) {
	home := hooksHome(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeHooksTestFile(t, filepath.Join(agent.PiDir(home), "extensions", "pitwall.ts"), string(agent.PiExtension(self)))
	writeHooksTestFile(t, filepath.Join(agent.OmpDir(home), "extensions", "pitwall.ts"), string(agent.OmpExtension(self)))
	writeHooksTestFile(t, filepath.Join(agent.OpenCodeDir(home), "plugins", "pitwall.js"), string(agent.OpenCodePlugin(self)))
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nprintf '%s\\n' \"$GUARD_TEST_VERSION\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, version := range []string{"1.4.0", "2.0.0"} {
		t.Setenv("GUARD_TEST_VERSION", version)
		var rows []string
		hooksRows(home, func(s string) string { return s }, func(ok bool, what, detail, fix string) {
			if what == "guard" {
				rows = append(rows, fmt.Sprint(ok, " ", detail))
			}
		})
		if len(rows) != 3 || !strings.HasPrefix(rows[0], "true pi in ") || !strings.HasPrefix(rows[1], "true omp in ") {
			t.Fatalf("rows %v", rows)
		}
		if version == "1.4.0" && !strings.HasPrefix(rows[2], "true opencode in ") {
			t.Fatalf("OpenCode 1 rows %v", rows)
		}
		if version == "2.0.0" && (!strings.HasPrefix(rows[2], "false ") || !strings.Contains(rows[2], "OpenCode 2")) {
			t.Fatalf("OpenCode 2 rows %v", rows)
		}
	}
}
