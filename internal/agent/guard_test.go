package agent

import (
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
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" || exec.Command(node, "--experimental-strip-types", "-e", "").Run() != nil {
		t.Skip("needs node with TypeScript support and a shell script as the hook binary")
	}
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
