//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	bin := t.TempDir()
	self, _ := os.Executable()
	if err := os.Symlink(self, filepath.Join(bin, "pitwall")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := runHooks([]string{"install"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The hooks run testBin, which does not exist, so a guarded tab would run unguarded.
	code, out = doctor()
	fails := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "FAIL") {
			fails++
			if !strings.HasPrefix(l, "FAIL guard: ") || !strings.Contains(l, "runs an existing pitwall") {
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
