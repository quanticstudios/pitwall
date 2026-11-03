package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestOmpDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if got := OmpDir("/h"); got != filepath.Join("/h", ".omp", "agent") {
		t.Errorf("OmpDir = %q", got)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "~/agents/pi")
	if got := OmpDir("/h"); got != filepath.Join("/h", "agents", "pi") || got != PiDir("/h") {
		t.Errorf("OmpDir with PI_CODING_AGENT_DIR = %q", got)
	}
}

func TestOmpExtension(t *testing.T) {
	bin := `/opt/my "odd" bin/pitwall`
	src, pi := OmpExtension(bin), PiExtension(bin)
	if !strings.Contains(string(src), ompAgent) || strings.Contains(string(src), piAgent) || !strings.Contains(string(pi), piAgent) {
		t.Fatal("omp extension does not name its agent")
	}
	if !IsOmpExtension(src) || IsPiExtension(src) || IsOmpExtension(pi) || IsOmpExtension(append(src, '\n')) {
		t.Fatal("IsOmpExtension misjudges a file")
	}
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" || exec.Command(node, "--experimental-strip-types", "-e", "").Run() != nil {
		t.Skip("needs node with TypeScript support and a shell script as the hook binary")
	}

	// A fake `pitwall` appends each payload it gets as a line. The harness
	// drives the extension through one run as omp and as pi fire it: omp ends
	// a run with an agent_end that will not continue, pi with agent_settled.
	// omp loads pi's copy under $PI_CODING_AGENT_DIR, and that copy knows omp
	// by its process title.
	for _, tt := range []struct {
		name, agent string
		ext         func(bin string) []byte
		title, end  string
	}{
		{"omp", "omp", OmpExtension, "", ""},
		{"pi's copy in omp", "omp", PiExtension, "omp", ""},
		{"pi", "pi", PiExtension, "", `await on.agent_settled({}, ctx);`},
	} {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.jsonl")
		fake := filepath.Join(dir, "pitwall")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1 $2\" = \"hook "+tt.agent+"\" ] || exit 1\n{ cat; echo; } >> \"$OUT\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		ext := filepath.Join(dir, "pitwall.ts")
		if err := os.WriteFile(ext, tt.ext(fake), 0o600); err != nil {
			t.Fatal(err)
		}
		harness := filepath.Join(dir, "harness.mjs")
		if err := os.WriteFile(harness, []byte(`import childProcess from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
// Observe the real hook children so inspection follows every close event.
const spawn = childProcess.spawn;
const children = new Set();
childProcess.spawn = (...args) => {
	const child = spawn(...args);
	children.add(new Promise((resolve, reject) => {
		child.once("error", reject);
		child.once("close", (code) => code === 0 ? resolve() : reject(new Error("hook exit " + code)));
	}));
	return child;
};
syncBuiltinESMExports();
const drain = async () => {
	do {
		const batch = [...children];
		children.clear();
		await Promise.all(batch);
		// Let close callbacks finish scheduling the next queued hook.
		await new Promise((resolve) => setImmediate(resolve));
	} while (children.size);
};
// The extension unrefs its children; the harness must stay until they close.
const keepAlive = setInterval(() => {}, 0x7fffffff);
if (process.env.TITLE) process.title = process.env.TITLE;
const ext = (await import(process.argv[2])).default;
const on = {};
ext({ on: (name, fn) => { on[name] = fn; } });
const ctx = { sessionManager: { getSessionFile: () => "/s/s1.jsonl", getSessionId: () => "s1" } };
const reply = (stopReason, text) => ({ role: "assistant", stopReason, content: [{ type: "text", text }] });
await on.session_start({}, ctx);
await on.before_agent_start({ prompt: "fix it" }, ctx);
await on.agent_start({}, ctx);
await on.tool_call({ toolName: "bash", input: { command: "echo secret-arg" } }, ctx);
await on.tool_result({ toolName: "bash", input: { command: "echo secret-arg" }, content: [{ type: "text", text: "ok" }], isError: false }, ctx);
await on.tool_result({ toolName: "bash", input: { command: "make" }, content: [{ type: "text", text: "no rule\n\nCommand exited with code 2" }], isError: true,
	structuredContent: { exit_code: 2 }, details: { exitCode: 2 } }, { ...ctx, cwd: "/r" });
await on.agent_end({ messages: [reply("stop", "Checking the tests.")], willContinue: true }, ctx);
await on.agent_end({ messages: [reply("stop", "All done.")] }, ctx);
`+tt.end+`
await drain();
await on.session_shutdown({}, ctx);
await drain();
clearInterval(keepAlive);
`), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", "--no-warnings", harness, ext)
		cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "OUT="+out, "TITLE="+tt.title)
		cmd.WaitDelay = 15 * time.Second
		b, err := cmd.CombinedOutput()
		timedOut := ctx.Err()
		cancel()
		if timedOut != nil {
			t.Fatalf("%s: node harness did not finish within 30s: %v\n%s", tt.name, timedOut, b)
		}
		if err != nil {
			t.Fatalf("%s: node: %v\n%s", tt.name, err, b)
		}
		b, err = os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "secret-arg") {
			t.Errorf("%s: sent tool arguments: %s", tt.name, b)
		}
		var got []string
		var settled, toolErr []byte
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			ev := PiEvent([]byte(line))
			switch ev {
			case "agent_settled":
				settled = []byte(line)
			case "tool_error":
				toolErr = []byte(line)
			}
			got = append(got, ev)
		}
		want := "session_start before_agent_start agent_start tool_call tool_error agent_settled session_shutdown"
		if strings.Join(got, " ") != want {
			t.Fatalf("%s: events\n got %v\nwant %s", tt.name, got, want)
		}
		p := model.Provider(tt.agent)
		if f, ok := ToolFailure(p, toolErr); !ok || f != (Failure{Tool: "bash", Command: "make", Output: "no rule\n\nCommand exited with code 2", Exit: 2, Cwd: "/r", Shell: true}) {
			t.Errorf("%s: ToolFailure(tool_error) = %+v, %v", tt.name, f, ok)
		}
		if _, ok := Derive(&model.Activity{Provider: p, SessionID: "s1", State: model.StateWorking}, p, toolErr, time.Now()); ok {
			t.Errorf("%s: tool_error changed the activity", tt.name)
		}
		if next, ok := Derive(&model.Activity{Provider: p, SessionID: "s1", State: model.StateWorking}, p, settled, time.Now()); !ok || next.State != model.StateCompleted || next.Detail != "All done." || next.Provider != p {
			t.Errorf("%s: Derive(agent_settled) = %+v, %v", tt.name, next, ok)
		}
	}
}
