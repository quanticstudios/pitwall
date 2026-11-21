//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// TestDoctorCore runs doctor as pitwall-core: with no pitwall on PATH it
// prints the ln -s that makes one, a pitwall link to it is this pitwall,
// and a desktop pitwall of another version in its folder is a warning,
// not a failure.
func TestDoctorCore(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(dir, "pitwall-core")
	// why: os.Executable resolves links, so the copy must be a file of its own.
	if err := os.Link(self, core); err != nil {
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	doctor := func() string {
		cmd := exec.Command(core, "doctor")
		cmd.Env = []string{"PITWALL_TEST_MAIN=1", "HOME=" + home, "PATH=" + dir,
			"XDG_RUNTIME_DIR=" + t.TempDir(), "XDG_STATE_HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + t.TempDir(), "XDG_CACHE_HOME=" + t.TempDir(),
			"PITWALL_SOCKET=" + sockPath(t, "none.sock"), "GORACE=atexit_sleep_ms=0"}
		out, _ := cmd.Output()
		return string(out)
	}
	link := filepath.Join(dir, "pitwall")
	steps := []struct {
		name  string
		setup func()
		want  []string
		not   string
	}{
		{"core alone", func() {}, []string{"FAIL pitwall on PATH: not found", "fix: ln -s pitwall-core '" + link + "'; agents and hooks run pitwall"}, "warn "},
		{"link", func() {
			if err := os.Symlink("pitwall-core", link); err != nil {
				t.Fatal(err)
			}
		}, []string{"ok   pitwall on PATH: " + link + "\n"}, "warn "},
		{"older desktop", func() {
			os.Remove(link)
			if err := os.WriteFile(link, []byte("#!/bin/sh\necho 'pitwall v0.0.1'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"ok   pitwall on PATH: " + link + ", the pitwall next to this pitwall-core", "warn " + link + " is v0.0.1 and this pitwall-core is " + versionString(), "fix: " + link + " update"}, "FAIL pitwall on PATH"},
	}
	for _, s := range steps {
		s.setup()
		out := doctor()
		for _, want := range s.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: no %q in:\n%s", s.name, want, out)
			}
		}
		if strings.Contains(out, s.not) {
			t.Errorf("%s: %q in:\n%s", s.name, s.not, out)
		}
	}
}

// TestCLIVersionRefusal: a daemon of another proto.Version, fakeOldDaemon,
// turns the CLI away, and the CLI names both versions and the fix.
func TestCLIVersionRefusal(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("PITWALL_PANE", "")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PITWALL_TEST_OLD=error", "GORACE=atexit_sleep_ms=0")
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if _, err := io.ReadFull(ready, make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runCLI([]string{"ls"}, nil, &out, &errOut)
	want := fmt.Sprintf("pitwall: the daemon speaks protocol version 15 and this %s %s speaks %d; run %s update to bring both to one version\n", binName, versionString(), proto.Version, binName)
	if code != 1 || errOut.String() != want {
		t.Errorf("exit %d\n got %q\nwant %q", code, errOut.String(), want)
	}
}

func TestPathFixOtherCore(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	core := filepath.Join(dir, "pitwall-core")
	for _, path := range []string{core, filepath.Join(other, "pitwall-core")} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", other)
	if got := pathFix(core); !strings.Contains(got, ", and add "+dir+" to PATH") {
		t.Fatalf("another core on PATH hides this install's PATH fix: %s", got)
	}
	t.Setenv("PATH", dir)
	if got := pathFix(core); strings.Contains(got, ", and add ") {
		t.Fatalf("this core is already on PATH: %s", got)
	}
}

// A Codex hooks.json whose SessionStart runs only another tool's script:
// doctor says pitwall's SessionStart is missing, and install adds it
// beside that script, which stays.
func TestDoctorSessionStart(t *testing.T) {
	home := hooksHome(t)
	path := filepath.Join(home, ".codex", "hooks.json")
	var events map[string]json.RawMessage
	if err := json.Unmarshal(agent.CodexHooks(testBin), &events); err != nil {
		t.Fatal(err)
	}
	other := `[{"hooks":[{"type":"command","command":"/opt/other/session-start.sh","timeout":5}]}]`
	events["SessionStart"] = json.RawMessage(other)
	data, _ := json.Marshal(map[string]any{"hooks": events})
	writeHooksTestFile(t, path, string(data))
	row := func() (bool, string) {
		var ok bool
		var detail string
		hooksRows(home, func(s string) string { return s }, func(o bool, what, d, fix string) {
			if what == "hooks" && strings.HasPrefix(d, path) {
				ok, detail = o, d
			}
		})
		return ok, detail
	}
	if ok, detail := row(); ok || !strings.Contains(detail, "lacks pitwall's SessionStart entry") {
		t.Fatalf("doctor row %v %q", ok, detail)
	}
	if _, err := installHooks(false, false); err != nil {
		t.Fatal(err)
	}
	if ok, detail := row(); !ok {
		t.Errorf("after install: %q", detail)
	}
	var got struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(readHooksTestFile(t, path), &got); err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, g := range got.Hooks["SessionStart"] {
		for _, h := range g.Hooks {
			cmds = append(cmds, h.Command)
		}
	}
	if len(cmds) != 2 || cmds[0] != "/opt/other/session-start.sh" || !strings.HasSuffix(cmds[1], " hook codex") {
		t.Errorf("SessionStart commands %q, want the other tool's, then pitwall's", cmds)
	}
}
