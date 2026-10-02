package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
)

func hooksHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	previous := hookExecutable
	hookExecutable = func() (string, error) { return "/opt/pitwall/bin/pitwall", nil }
	t.Cleanup(func() { hookExecutable = previous })
	return home
}

func writeHooksTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}
}

func readHooksTestFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func equalHookJSON(t *testing.T, got, want []byte) {
	t.Helper()
	decode := func(data []byte) any {
		var value any
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(got), decode(want)) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestHooksInstallUninstall(t *testing.T) {
	home := hooksHome(t)
	original := `{"model":"keep","number":9007199254740993,"hooks":{"Stop":[{"matcher":"*","extra":true,"hooks":[{"type":"command","command":"other hook claude","timeout":42}]}],"Custom":[]}}`
	paths := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json")}
	for _, path := range paths {
		writeHooksTestFile(t, path, original)
	}
	var out bytes.Buffer
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "added Stop:") || !strings.Contains(out.String(), "/hooks once") {
		t.Fatal(out.String())
	}
	installed := make([][]byte, len(paths))
	for i, path := range paths {
		installed[i] = readHooksTestFile(t, path)
		root, _ := hookJSON[hookObject](installed[i])
		if string(root["number"]) != "9007199254740993" || string(root["model"]) != `"keep"` {
			t.Fatalf("unrelated keys changed: %s", installed[i])
		}
		events, _ := hookJSON[hookObject](root["hooks"])
		groups, _ := hookJSON[[]hookObject](events["Stop"])
		if len(groups) != 2 || string(groups[0]["extra"]) != "true" {
			t.Fatalf("existing hook lost: %s", events["Stop"])
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o640 {
			t.Fatal("file mode changed")
		}
		backups, _ := filepath.Glob(path + ".pitwall-backup-*")
		if len(backups) != 1 || string(readHooksTestFile(t, backups[0])) != original {
			t.Fatal("backup does not contain original bytes")
		}
	}
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		if !bytes.Equal(readHooksTestFile(t, path), installed[i]) {
			t.Fatal("second install changed file")
		}
		backups, _ := filepath.Glob(path + ".pitwall-backup-*")
		if len(backups) != 1 {
			t.Fatal("second install created a backup")
		}
	}
	if err := runHooks([]string{"uninstall"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		equalHookJSON(t, readHooksTestFile(t, path), []byte(original))
		backups, _ := filepath.Glob(path + ".pitwall-backup-*")
		if len(backups) != 2 {
			t.Fatal("uninstall did not preserve both backups")
		}
	}
}

func TestHooksDryRunAndMissingFiles(t *testing.T) {
	home := hooksHome(t)
	var out bytes.Buffer
	for _, action := range []string{"install", "uninstall"} {
		if err := runHooks([]string{action, "--dry-run"}, &out); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{".claude", ".codex"} {
			if _, err := os.Stat(filepath.Join(home, dir)); !os.IsNotExist(err) {
				t.Fatal("dry-run wrote a directory")
			}
		}
	}
	if !strings.Contains(out.String(), `"command": "'/opt/pitwall/bin/pitwall' hook codex"`) {
		t.Fatal("dry-run omitted resulting JSON")
	}
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(home, ".claude", "settings.json")
	before := readHooksTestFile(t, claude)
	if err := runHooks([]string{"uninstall", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readHooksTestFile(t, claude)) {
		t.Fatal("uninstall dry-run changed config")
	}
	if backups, _ := filepath.Glob(claude + ".pitwall-backup-*"); len(backups) != 0 {
		t.Fatal("dry-run made a backup")
	}
	if err := runHooks([]string{"uninstall"}, &out); err != nil {
		t.Fatal(err)
	}
	equalHookJSON(t, readHooksTestFile(t, claude), []byte(`{}`))
}

func TestHooksRefuseTemporaryExecutable(t *testing.T) {
	home := hooksHome(t)
	t.Setenv("GOCACHE", filepath.Join(home, "custom-cache"))
	for _, bin := range []string{filepath.Join(t.TempDir(), "pitwall"), filepath.Join(home, ".cache", "go-build", "pitwall"), filepath.Join(home, "custom-cache", "pitwall")} {
		t.Run(bin, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			hookExecutable = func() (string, error) { return bin, nil }
			err := runHooks([]string{"install"}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "scripts/install.sh") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestHooksMergeMixedGroups(t *testing.T) {
	bin := "/opt/pitwall/bin/pitwall"
	original := []byte(`{"hooks":{"Stop":[{"matcher":"*","extra":true,"hooks":[{"command":"'/opt/pitwall/bin/pitwall' hook claude"},{"command":"'/opt/other/pitwall' hook claude"},{"command":"echo /opt/pitwall/bin/pitwall hook claude"}]}],"Empty":[]}}`)
	installed, changes, err := mergeHooks(original, agent.ClaudeHooks(bin), bin, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change, "added Stop:") {
			t.Fatal("duplicated command inside existing group")
		}
	}
	removed, _, err := mergeHooks(installed, nil, bin, false)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := hookJSON[hookObject](removed)
	events, _ := hookJSON[hookObject](root["hooks"])
	groups, _ := hookJSON[[]hookObject](events["Stop"])
	handlers, _ := hookJSON[[]hookObject](groups[0]["hooks"])
	if len(handlers) != 2 || string(groups[0]["extra"]) != "true" || string(events["Empty"]) != "[]" {
		t.Fatalf("removed unrelated entries: %s", removed)
	}
}

func TestHooksInvalidConfigWritesNothing(t *testing.T) {
	home := hooksHome(t)
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "hooks.json")
	writeHooksTestFile(t, claude, `{"keep":true}`)
	for _, invalid := range []string{`{`, `null`, `[]`, `{"hooks":null}`, `{"hooks":{"Stop":{}}}`, `{"hooks":{"Stop":[{}]}}`} {
		writeHooksTestFile(t, codex, invalid)
		if err := runHooks([]string{"install"}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
		if !reflect.DeepEqual(readHooksTestFile(t, claude), []byte(`{"keep":true}`)) {
			t.Fatal("changed Claude config before validating Codex config")
		}
	}
}

func TestIsPitwallHook(t *testing.T) {
	for _, tc := range []struct {
		command, bin string
		want         bool
	}{
		{`'/opt/pitwall' hook claude`, "/opt/pitwall", true},
		{`"/opt/pitwall" hook codex`, "/opt/pitwall", true},
		{`/opt/pitwall hook claude`, "/opt/pitwall", true},
		{`echo /opt/pitwall hook claude`, "/opt/pitwall", false},
		{`/opt/pitwall-other hook claude`, "/opt/pitwall", false},
		{`/opt/pitwall hooks install`, "/opt/pitwall", false},
		{`'/opt/my pitwall' hook claude`, "/opt/my pitwall", true},
		{`/opt/my pitwall hook claude`, "/opt/my pitwall", false},
		{`'/opt/it'\''s pitwall' hook claude`, "/opt/it's pitwall", true},
		{`"/opt/\$pitwall" hook codex`, "/opt/$pitwall", true},
		{`"/opt/$pitwall" hook codex`, "/opt/$pitwall", false},
	} {
		if got := isPitwallHook(tc.command, tc.bin); got != tc.want {
			t.Errorf("isPitwallHook(%q, %q) = %v, want %v", tc.command, tc.bin, got, tc.want)
		}
	}
}

func TestHooksConcurrentEdit(t *testing.T) {
	for _, action := range []string{"install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			home := hooksHome(t)
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
			paths := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json")}
			if err := runHooks([]string{"install"}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if action == "install" {
				if err := runHooks([]string{"uninstall"}, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			}
			fresh := map[string][]byte{}
			previous := hookBeforeWrite
			hookBeforeWrite = func(path string) {
				lock, err := os.OpenFile(filepath.Join(stateDir(), "hooks.lock"), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
					t.Fatalf("hook update does not hold the lock: %v", err)
				}
				root, err := hookJSON[hookObject](readHooksTestFile(t, path))
				if err != nil {
					t.Fatal(err)
				}
				root["concurrent"] = []byte(`"keep me"`)
				fresh[path], err = json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, fresh[path], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { hookBeforeWrite = previous })
			if err := runHooks([]string{action}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				root, err := hookJSON[hookObject](readHooksTestFile(t, path))
				if err != nil {
					t.Fatal(err)
				}
				if string(root["concurrent"]) != `"keep me"` {
					t.Fatalf("concurrent edit lost: %s", root)
				}
				backups, err := filepath.Glob(path + ".pitwall-backup-*")
				if err != nil || len(backups) == 0 {
					t.Fatalf("backups: %v, %v", backups, err)
				}
				if !bytes.Equal(readHooksTestFile(t, backups[len(backups)-1]), fresh[path]) {
					t.Fatal("backup lost concurrent edit")
				}
			}
		})
	}
}

func TestHooksConcurrentInvalidEdit(t *testing.T) {
	home := hooksHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeHooksTestFile(t, path, `{}`)
	previous := hookBeforeWrite
	hookBeforeWrite = func(path string) { writeHooksTestFile(t, path, `{`) }
	t.Cleanup(func() { hookBeforeWrite = previous })
	err := runHooks([]string{"install"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "changed during hook update") {
		t.Fatalf("got %v", err)
	}
	if string(readHooksTestFile(t, path)) != "{" {
		t.Fatal("overwrote concurrent invalid edit")
	}
	if backups, _ := filepath.Glob(path + ".pitwall-backup-*"); len(backups) != 0 {
		t.Fatal("backed up stale config")
	}
}
