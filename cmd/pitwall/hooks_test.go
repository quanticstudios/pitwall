package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/agent"
)

func hooksHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	// why: pi is skipped unless on PATH or configured; tests opt in through its dir.
	t.Setenv("PATH", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
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
	installed, changes, err := mergeHooks(original, agent.ClaudeHooks(bin), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change, "added Stop:") {
			t.Fatal("duplicated command inside existing group")
		}
	}
	removed, _, err := mergeHooks(installed, agent.ClaudeHooks(bin), false)
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
				if ok, err := tryLock(lock); ok || err != nil {
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

func TestHooksUninstallExactCommands(t *testing.T) {
	home := hooksHome(t)
	bin := "/opt/pitwall/bin/pitwall"
	for _, provider := range []string{"claude", "codex"} {
		path := filepath.Join(home, ".claude", "settings.json")
		other := "codex"
		if provider == "codex" {
			path = filepath.Join(home, ".codex", "hooks.json")
			other = "claude"
		}
		exact := "'" + bin + "' hook " + provider
		commands := []string{exact, exact + " && ~/bin/audit-agent", exact + " --custom", "'" + bin + "' hook " + other, bin + " hook " + provider, `"` + bin + `" hook ` + provider}
		handlers := []hookObject{}
		for _, command := range commands {
			raw, _ := json.Marshal(command)
			handlers = append(handlers, hookObject{"command": raw})
		}
		raw, _ := json.Marshal(handlers)
		writeHooksTestFile(t, path, `{"hooks":{"Stop":[{"hooks":`+string(raw)+`}]}}`)
		if err := runHooks([]string{"uninstall"}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		root, _ := hookJSON[hookObject](readHooksTestFile(t, path))
		events, _ := hookJSON[hookObject](root["hooks"])
		groups, _ := hookJSON[[]hookObject](events["Stop"])
		kept, _ := hookJSON[[]hookObject](groups[0]["hooks"])
		if !reflect.DeepEqual(kept, handlers[1:]) {
			t.Fatalf("uninstall removed user commands: %s", events["Stop"])
		}
	}
}

func TestHooksPreserveSymlinks(t *testing.T) {
	home := hooksHome(t)
	paths := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "hooks.json")}
	original := `{"keep":true}`
	for _, path := range paths {
		target := filepath.Join(home, "dotfiles", filepath.Base(filepath.Dir(path)), filepath.Base(path))
		writeHooksTestFile(t, target, original)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(filepath.Dir(path), target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(relative, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"install", "uninstall"} {
		var out bytes.Buffer
		if err := runHooks([]string{action}, &out); err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("config is no longer a symlink: %v", err)
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err = os.Stat(target)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("target mode changed: %v", err)
			}
			data := readHooksTestFile(t, target)
			root, err := hookJSON[hookObject](data)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := root["hooks"]; ok != (action == "install") {
				t.Fatalf("target hooks: %s", data)
			}
			backups, _ := filepath.Glob(target + ".pitwall-backup-*")
			if len(backups) == 0 || string(readHooksTestFile(t, backups[0])) != original {
				t.Fatal("backup is missing beside target")
			}
			if !strings.Contains(out.String(), "Backup: "+target+".pitwall-backup-") {
				t.Fatal("output omitted target backup")
			}
			if backups, _ := filepath.Glob(path + ".pitwall-backup-*"); len(backups) != 0 {
				t.Fatal("backup is beside symlink")
			}
		}
	}
}

func TestHooksPiExtension(t *testing.T) {
	home := hooksHome(t)
	var out bytes.Buffer
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) || strings.Contains(out.String(), "pitwall.ts") {
		t.Fatalf("pi set up without pi: %s", out.String())
	}

	dir := filepath.Join(t.TempDir(), "pi agent")
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "extensions", "pitwall.ts")
	want := agent.PiExtension("/opt/pitwall/bin/pitwall")
	out.Reset()
	if err := runHooks([]string{"install", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), string(want)) {
		t.Fatalf("dry-run omitted the extension: %s", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote the extension")
	}
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readHooksTestFile(t, path), want) {
		t.Fatal("install wrote something else")
	}
	out.Reset()
	if err := runHooks([]string{"install"}, &out); err != nil || !strings.Contains(out.String(), path+": unchanged") {
		t.Fatalf("second install: %v %s", err, out.String())
	}

	// An older binary's file is replaced; an edited one is never touched.
	writeHooksTestFile(t, path, string(agent.PiExtension("/old/pitwall")))
	if err := runHooks([]string{"install"}, &out); err != nil || !bytes.Equal(readHooksTestFile(t, path), want) {
		t.Fatalf("install over an old binary's file: %v", err)
	}
	edited := string(want) + "// mine\n"
	writeHooksTestFile(t, path, edited)
	claude := filepath.Join(home, ".claude", "settings.json")
	if err := os.Remove(claude); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatalf("an edited extension stopped the install: %v", err)
	}
	if string(readHooksTestFile(t, path)) != edited || !strings.Contains(out.String(), path+": skipped: edited") {
		t.Fatalf("install touched an edited extension or did not warn: %s", out.String())
	}
	if !strings.Contains(string(readHooksTestFile(t, claude)), "hook claude") {
		t.Fatal("an edited extension kept Claude's hooks from installing")
	}
	out.Reset()
	if err := runHooks([]string{"install", "--dry-run"}, &out); err != nil || !strings.Contains(out.String(), "skipped: edited") {
		t.Fatalf("dry-run did not warn: %v %s", err, out.String())
	}
	if err := runHooks([]string{"uninstall"}, &out); err != nil || string(readHooksTestFile(t, path)) != edited {
		t.Fatalf("uninstall touched an edited extension: %v", err)
	}
	writeHooksTestFile(t, path, string(agent.PiExtension("/old/pitwall")))
	if err := runHooks([]string{"uninstall"}, &out); err != nil || !agent.IsPiExtension(readHooksTestFile(t, path)) {
		t.Fatalf("uninstall removed another binary's extension: %v", err)
	}

	writeHooksTestFile(t, path, string(want))
	if err := runHooks([]string{"uninstall", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("uninstall dry-run removed the extension")
	}
	out.Reset()
	if err := runHooks([]string{"uninstall"}, &out); err != nil || !strings.Contains(out.String(), "removed pi extension") {
		t.Fatalf("uninstall: %v %s", err, out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("uninstall left the extension")
	}
}

func TestHooksGeminiAndOpenCode(t *testing.T) {
	home := hooksHome(t)
	var out bytes.Buffer
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), ".gemini") || strings.Contains(out.String(), "opencode") {
		t.Fatalf("set up agents that are not installed: %s", out.String())
	}

	gemini := filepath.Join(home, ".gemini", "settings.json")
	original := `{"theme":"Dracula","hooksConfig":{"enabled":true},"hooks":{"BeforeTool":[{"matcher":"write_file","hooks":[{"type":"command","command":"lint.sh"}]}]}}`
	writeHooksTestFile(t, gemini, original)
	cfg := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(cfg, "opencode", "plugins", "pitwall.js")
	out.Reset()
	if err := runHooks([]string{"install", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "# "+plugin) || !strings.Contains(out.String(), `hook gemini"`) || string(readHooksTestFile(t, gemini)) != original {
		t.Fatalf("dry-run: %s", out.String())
	}
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	root, _ := hookJSON[hookObject](readHooksTestFile(t, gemini))
	events, _ := hookJSON[hookObject](root["hooks"])
	if string(root["theme"]) != `"Dracula"` || len(events) != 7 || !strings.Contains(string(events["BeforeTool"]), "lint.sh") || !strings.Contains(string(events["AfterAgent"]), `"timeout": 5000`) {
		t.Fatalf("Gemini settings: %s", readHooksTestFile(t, gemini))
	}
	if !bytes.Equal(readHooksTestFile(t, plugin), agent.OpenCodePlugin("/opt/pitwall/bin/pitwall")) {
		t.Fatal("install wrote another plugin")
	}
	if err := runHooks([]string{"uninstall"}, &out); err != nil {
		t.Fatal(err)
	}
	equalHookJSON(t, readHooksTestFile(t, gemini), []byte(original))
	if _, err := os.Stat(plugin); !os.IsNotExist(err) {
		t.Fatal("uninstall left the plugin")
	}

	// Gemini's settings may carry comments: they are skipped with a
	// warning, and the rest installs. An edited plugin is left alone.
	commented := "// mine\n" + original
	writeHooksTestFile(t, gemini, commented)
	writeHooksTestFile(t, plugin, "export const Mine = async () => ({})\n")
	out.Reset()
	if err := runHooks([]string{"install"}, &out); err != nil {
		t.Fatal(err)
	}
	if string(readHooksTestFile(t, gemini)) != commented || !strings.Contains(out.String(), gemini+": skipped:") || !strings.Contains(out.String(), plugin+": skipped: edited") {
		t.Fatalf("install touched a file it cannot merge: %s", out.String())
	}
	if !strings.Contains(string(readHooksTestFile(t, filepath.Join(home, ".claude", "settings.json"))), "hook claude") {
		t.Fatal("a skipped agent kept Claude's hooks from installing")
	}
}

func TestHomebrewOpt(t *testing.T) {
	for bin, want := range map[string]string{
		"/opt/homebrew/Cellar/pitwall/0.1.0-alpha.24/bin/pitwall":              "/opt/homebrew/opt/pitwall/bin/pitwall",
		"/home/linuxbrew/.linuxbrew/Cellar/pitwall/0.1.0-alpha.24/bin/pitwall": "/home/linuxbrew/.linuxbrew/opt/pitwall/bin/pitwall",
		"/home/me/.local/bin/pitwall":                                          "/home/me/.local/bin/pitwall",
		"/opt/homebrew/Cellar/pitwall":                                         "/opt/homebrew/Cellar/pitwall",
	} {
		if got := homebrewOpt(bin); got != want {
			t.Errorf("homebrewOpt(%q) = %q, want %q", bin, got, want)
		}
	}
}
