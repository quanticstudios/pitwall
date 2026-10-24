package agent

import (
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
		if err := os.WriteFile(harness, []byte(`import fs from "node:fs";
if (process.env.TITLE) process.title = process.env.TITLE;
const ext = (await import(process.argv[2])).default;
const on = {};
ext({ on: (name, fn) => { on[name] = fn; } });
const ctx = { sessionManager: { getSessionFile: () => "/s/s1.jsonl", getSessionId: () => "s1" } };
const lines = () => fs.existsSync(process.env.OUT) ? fs.readFileSync(process.env.OUT, "utf8").split("\n").filter(Boolean).length : 0;
const wait = async (n) => { const end = Date.now() + 10000; while (lines() < n && Date.now() < end) await new Promise((r) => setTimeout(r, 20)); };
const reply = (stopReason, text) => ({ role: "assistant", stopReason, content: [{ type: "text", text }] });
await on.session_start({}, ctx);
await on.before_agent_start({ prompt: "fix it" }, ctx);
await on.agent_start({}, ctx);
await on.tool_call({ toolName: "bash", input: { command: "echo secret-arg" } }, ctx);
await on.agent_end({ messages: [reply("stop", "Checking the tests.")], willContinue: true }, ctx);
await on.agent_end({ messages: [reply("stop", "All done.")] }, ctx);
`+tt.end+`
await wait(5);
await on.session_shutdown({}, ctx);
await wait(6);
await new Promise((r) => setTimeout(r, 200)); // nothing more may follow
`), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext)
		cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "OUT="+out, "TITLE="+tt.title)
		cmd.WaitDelay = 15 * time.Second
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: node: %v\n%s", tt.name, err, b)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "secret-arg") {
			t.Errorf("%s: sent tool arguments: %s", tt.name, b)
		}
		var got []string
		var settled []byte
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			ev := PiEvent([]byte(line))
			if ev == "agent_settled" {
				settled = []byte(line)
			}
			got = append(got, ev)
		}
		want := "session_start before_agent_start agent_start tool_call agent_settled session_shutdown"
		if strings.Join(got, " ") != want {
			t.Fatalf("%s: events\n got %v\nwant %s", tt.name, got, want)
		}
		p := model.Provider(tt.agent)
		if next, ok := Derive(&model.Activity{Provider: p, SessionID: "s1", State: model.StateWorking}, p, settled, time.Now()); !ok || next.State != model.StateCompleted || next.Detail != "All done." || next.Provider != p {
			t.Errorf("%s: Derive(agent_settled) = %+v, %v", tt.name, next, ok)
		}
	}
}
