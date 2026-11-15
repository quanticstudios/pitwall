package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestGuardPlugins: in a guarded tab, pi's extension blocks a tool call
// and OpenCode's plugin throws when `pitwall guard` denies its command;
// other commands, and every command in an unguarded tab, go through.
func TestGuardPlugins(t *testing.T) {
	node := guardNode(t)
	dir := t.TempDir()
	// A fake `pitwall`: guard denies git push as pitwall guard does, hook
	// swallows the report.
	fake := filepath.Join(dir, "pitwall")
	if err := os.WriteFile(fake, []byte(`#!/bin/sh
in=$(cat)
[ "$1" = guard ] || exit 0
case "$in" in *'"git push'*) echo "pitwall guard: no push" >&2; exit 2;; esac
exit 0
`), 0o700); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(dir, "pitwall.ts")
	plugin := filepath.Join(dir, "pitwall.mjs")
	os.WriteFile(ext, PiExtension(fake), 0o600)
	os.WriteFile(plugin, OpenCodePlugin(fake), 0o600)
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(`
const pi = (await import(process.argv[2])).default;
const on = {};
pi({ on: (name, fn) => { on[name] = fn; } });
const ctx = { sessionManager: { getSessionFile: () => "/s/s1.jsonl", getSessionId: () => "s1" } };
const piCall = async (command) => JSON.stringify((await on.tool_call({ toolName: "bash", input: { command } }, ctx)) ?? null);
const h = await (await import(process.argv[3])).default.server({});
const ocCall = async (command) => {
	try {
		await h["tool.execute.before"]({ tool: "bash", sessionID: "s1", callID: "c1" }, { args: { command } });
		return "ran";
	} catch (e) {
		return e.message;
	}
};
console.log([await piCall("git push"), await piCall("go test"), await ocCall("git push origin x"), await ocCall("go test")].join("\n"));
process.exit(0);
`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(guarded string) []string {
		cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext, plugin)
		cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "PITWALL_GUARD="+guarded)
		cmd.WaitDelay = 15 * time.Second
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, b)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	want := []string{`{"block":true,"reason":"pitwall guard: no push"}`, "null", "pitwall guard: no push", "ran"}
	if got := run("1"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("guarded:\n got %q\nwant %q", got, want)
	}
	want = []string{"null", "null", "ran", "ran"}
	if got := run(""); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("unguarded:\n got %q\nwant %q", got, want)
	}
}

// guardNode is node with TypeScript support. CI on Linux installs one, so
// there a missing node fails the test instead of skipping the guard's.
func guardNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err == nil && runtime.GOOS != "windows" && exec.Command(node, "--experimental-strip-types", "-e", "").Run() == nil {
		return node
	}
	if os.Getenv("CI") != "" && runtime.GOOS == "linux" {
		t.Fatal("CI on Linux must run the generated guards: install node 22.6 or later")
	}
	t.Skip("needs node with TypeScript support and a shell script as the hook binary")
	return ""
}

// TestGuardPluginsFailClosed: in a guarded tab, pi's extension and
// OpenCode's plugin deny a command when `pitwall guard` cannot answer: it
// is missing, deleted after they loaded, exits with neither 0 nor 2, is
// killed, or hangs past 5s.
func TestGuardPluginsFailClosed(t *testing.T) {
	node := guardNode(t)
	harness := filepath.Join(t.TempDir(), "harness.mjs")
	if err := os.WriteFile(harness, []byte(`
import { unlinkSync } from "node:fs";
const [ext, plugin, gone] = process.argv.slice(2);
const pi = (await import(ext)).default;
const on = {};
pi({ on: (name, fn) => { on[name] = fn; } });
const h = await (await import(plugin)).default.server({});
if (gone) unlinkSync(gone);
const ctx = { sessionManager: { getSessionFile: () => "/s/s1.jsonl", getSessionId: () => "s1" } };
const piCall = async () => JSON.stringify((await on.tool_call({ toolName: "bash", input: { command: "go test" } }, ctx)) ?? null);
const ocCall = async () => {
	try {
		await h["tool.execute.before"]({ tool: "bash", sessionID: "s1", callID: "c1" }, { args: { command: "go test" } });
		return "ran";
	} catch (e) {
		return e.message;
	}
};
console.log((await Promise.all([piCall(), ocCall()])).join("\n"));
process.exit(0);
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, script string // script "" for no binary
		deleted      bool
		why          string
	}{
		{"missing", "", false, "ENOENT"},
		{"deleted after load", "#!/bin/sh\nexit 0\n", true, "ENOENT"},
		{"exits 1", "#!/bin/sh\ncat >/dev/null\nexit 1\n", false, "exit 1"},
		{"killed", "#!/bin/sh\nkill -9 $$\n", false, "killed by SIGKILL"},
		{"hangs", "#!/bin/sh\nexec sleep 30\n", false, "no answer in 5s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "pitwall")
			if c.script != "" {
				if err := os.WriteFile(bin, []byte(c.script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ext, plugin := filepath.Join(dir, "pitwall.ts"), filepath.Join(dir, "pitwall.mjs")
			os.WriteFile(ext, PiExtension(bin), 0o600)
			os.WriteFile(plugin, OpenCodePlugin(bin), 0o600)
			args := []string{"--experimental-strip-types", "--no-warnings", harness, ext, plugin}
			if c.deleted {
				args = append(args, bin)
			}
			cmd := exec.Command(node, args...)
			cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "PITWALL_GUARD=1")
			cmd.WaitDelay = 15 * time.Second
			b, err := cmd.Output()
			if err != nil {
				t.Fatalf("node: %v\n%s", err, b)
			}
			reason := fmt.Sprintf("pitwall guard could not check this command (%s); denied because the tab is guarded", c.why)
			got := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(got) != 2 || !strings.HasPrefix(got[0], `{"block":true,"reason":"pitwall guard could not check this command (`) || !strings.Contains(got[0], c.why) || !strings.HasPrefix(got[1], "pitwall guard could not check") || !strings.Contains(got[1], c.why) {
				t.Fatalf("got %q, want both denied like %q", got, reason)
			}
		})
	}
}
