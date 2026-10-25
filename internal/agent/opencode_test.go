package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestGeminiHooks(t *testing.T) {
	var events map[string][]struct {
		Hooks []struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(GeminiHooks("/usr/bin/pitwall"), &events); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"SessionStart", "BeforeAgent", "BeforeTool", "AfterTool", "Notification", "AfterAgent", "SessionEnd"} {
		g := events[e]
		if len(g) != 1 || len(g[0].Hooks) != 1 || !strings.HasSuffix(g[0].Hooks[0].Command, " hook gemini") || g[0].Hooks[0].Timeout != 5000 {
			t.Errorf("%s: %+v", e, g)
		}
	}
	if got := Prompt(model.ProviderGemini, fixture(t, "gemini_before_agent")); got != "Fix the flaky cache test" {
		t.Errorf("Prompt: %q", got)
	}
	if got := LastMessage(fixture(t, "gemini_after_agent")); !strings.HasPrefix(got, "The test raced") {
		t.Errorf("LastMessage: %q", got)
	}
	if started, _ := NewProcess(fixture(t, "gemini_session_start")); !started {
		t.Error("NewProcess: Gemini's startup SessionStart is no new process")
	}
}

func TestOpenCodePlugin(t *testing.T) {
	bin := `/opt/my "odd" bin/pitwall`
	src := OpenCodePlugin(bin)
	if q, _ := json.Marshal(bin); !strings.Contains(string(src), "const bin = "+string(q)+";\n") {
		t.Fatal("bin not substituted as a quoted literal")
	}
	if !IsOpenCodePlugin(src) || IsOpenCodePlugin(append(src, '\n')) || IsOpenCodePlugin(PiExtension(bin)) || IsPiExtension(src) {
		t.Fatal("IsOpenCodePlugin misjudges a file")
	}
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs node and a shell script as the hook binary")
	}

	// A fake `pitwall` appends each payload it gets as a line; the harness
	// drives the plugin through two runs, an abort and an error.
	dir := t.TempDir()
	out := filepath.Join(dir, "out.jsonl")
	fake := filepath.Join(dir, "pitwall")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1 $2\" = \"hook opencode\" ] || exit 1\n{ cat; echo; } >> \"$OUT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "pitwall.mjs")
	if err := os.WriteFile(plugin, OpenCodePlugin(fake), 0o600); err != nil {
		t.Fatal(err)
	}
	const want = 15
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(`import fs from "node:fs";
const plugin = (await import(process.argv[2])).default;
if (plugin.id !== "pitwall" || typeof plugin.setup !== "function" || plugin.setup({ session: {} }) !== undefined) throw new Error("not an OpenCode 2 definition that idles in the service");
const h = await plugin.server({});
const ev = (type, properties) => h.event({ event: { type, properties } });
await h["chat.message"]({ sessionID: "s1" }, { parts: [{ type: "text", text: "fix it" }, { type: "text", text: "ctx", synthetic: true }] });
await h["tool.execute.before"]({ tool: "bash", sessionID: "s1", callID: "c1" }, { args: { command: "echo secret-arg" } });
await ev("session.created", { sessionID: "s2", info: { id: "s2", parentID: "s1" } });
await h["tool.execute.before"]({ tool: "read", sessionID: "s2", callID: "c2" }, { args: {} });
await ev("session.idle", { sessionID: "s2" });
const tool = (sessionID, callID, tool, state) => ev("message.part.updated", { sessionID, part: { type: "tool", sessionID, messageID: "m0", callID, tool, state } });
await tool("s1", "c1", "bash", { status: "running", input: { command: "echo secret-arg" } });
await tool("s1", "c1", "bash", { status: "completed", input: { command: "echo secret-arg" }, output: "ok", metadata: { exit: 0 } });
await tool("s1", "c4", "bash", { status: "completed", input: { command: "tsc" }, output: "error TS2304", metadata: { exit: 2 }, time: { start: 100, end: 900 } });
await tool("s1", "c4", "bash", { status: "completed", input: { command: "tsc" }, output: "error TS2304", metadata: { exit: 2 }, time: { start: 100, end: 900 } });
await tool("s1", "c5", "read", { status: "error", input: { filePath: "/r/x" }, error: "File not found", time: { start: 1, end: 2 } });
await tool("s2", "c6", "read", { status: "error", input: { filePath: "/r/y" }, error: "child" });
await ev("permission.asked", { id: "p1", sessionID: "s1", permission: "bash", patterns: [] });
await ev("permission.replied", { sessionID: "s1", requestID: "p1", reply: "once" });
await h["tool.execute.before"]({ tool: "plan_exit", sessionID: "s1", callID: "c3" }, { args: {} });
await ev("question.asked", { id: "q1", sessionID: "s1", questions: [{ header: "Build Agent", question: "Plan at p.md is complete." }], tool: { messageID: "m0", callID: "c3" } });
await ev("question.replied", { sessionID: "s1", requestID: "q1" });
await ev("message.updated", { sessionID: "s1", info: { id: "u1", role: "user" } });
await ev("message.part.updated", { sessionID: "s1", part: { type: "text", sessionID: "s1", messageID: "u1", text: "fix it" } });
await ev("message.updated", { sessionID: "s1", info: { id: "m1", role: "assistant" } });
await ev("message.part.updated", { sessionID: "s1", part: { type: "text", sessionID: "s1", messageID: "m1", text: "All done." } });
await ev("session.status", { sessionID: "s1", status: { type: "idle" } });
await ev("session.idle", { sessionID: "s1" });
await h["chat.message"]({ sessionID: "s1" }, { parts: [] });
await ev("session.error", { sessionID: "s1", error: { name: "MessageAbortedError", data: { message: "aborted" } } });
await ev("session.status", { sessionID: "s1", status: { type: "idle" } });
await h["chat.message"]({ sessionID: "s1" }, { parts: [] });
await ev("session.error", { sessionID: "s1", error: { name: "APIError", data: { message: "rate limited", isRetryable: false } } });
await ev("session.idle", { sessionID: "s1" });
const end = Date.now() + 10000;
while (Date.now() < end) {
	const n = fs.existsSync(process.env.OUT) ? fs.readFileSync(process.env.OUT, "utf8").split("\n").filter(Boolean).length : 0;
	if (n >= Number(process.argv[3])) break;
	await new Promise((r) => setTimeout(r, 20));
}
await new Promise((r) => setTimeout(r, 200)); // nothing more may follow
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harness, plugin, "15")
	cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "OUT="+out)
	cmd.WaitDelay = 15 * time.Second
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-arg") || strings.Contains(string(b), "ctx") || strings.Contains(string(b), "child") {
		t.Errorf("sent tool arguments or a synthetic part: %s", b)
	}
	var got []string
	var stop, failure map[string]any
	var tools []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		ev := m["hook_event_name"].(string)
		switch ev {
		case "UserPromptSubmit":
			ev += ":" + m["prompt"].(string)
		case "PreToolUse":
			ev += ":" + m["tool_name"].(string)
		case "Notification":
			ev += ":" + m["notification_type"].(string)
		case "Stop":
			stop = m
		case "StopFailure":
			failure = m
		case "PostToolUseFailure":
			f, ok := ToolFailure(model.ProviderOpenCode, []byte(line))
			if !ok {
				t.Errorf("ToolFailure(%s) not ok", line)
			}
			tools = append(tools, fmt.Sprintf("%+v", f))
		}
		got = append(got, ev)
	}
	wantEvents := []string{"SessionStart", "UserPromptSubmit:fix it", "PreToolUse:bash", "PostToolUseFailure", "PostToolUseFailure", "Notification:permission_prompt", "PostToolUse",
		"PreToolUse:plan_exit", "PreToolUse:ExitPlanMode", "PostToolUse", "Stop",
		"UserPromptSubmit:", "Interrupt", "UserPromptSubmit:", "StopFailure"}
	if len(wantEvents) != want || strings.Join(got, " ") != strings.Join(wantEvents, " ") {
		t.Fatalf("events\n got %v\nwant %v", got, wantEvents)
	}
	if w := []string{
		fmt.Sprintf("%+v", Failure{Tool: "bash", Command: "tsc", Output: "error TS2304", Exit: 2, Ms: 800, Shell: true}),
		fmt.Sprintf("%+v", Failure{Tool: "read", Command: "/r/x", Output: "File not found", Ms: 1}),
	}; strings.Join(tools, "\n") != strings.Join(w, "\n") {
		t.Errorf("failures\n got %v\nwant %v", tools, w)
	}
	if stop["last_assistant_message"] != "All done." || stop["session_id"] != "s1" || failure["error"] != "rate limited" {
		t.Errorf("Stop %v, StopFailure %v", stop, failure)
	}
	// Derive reads them as Claude's hooks.
	if next, ok := Derive(nil, model.ProviderOpenCode, []byte(strings.Split(string(b), "\n")[10]), time.Now()); !ok || next.State != model.StateCompleted || next.Detail != "All done." {
		t.Errorf("Derive(Stop) = %+v, %v", next, ok)
	}
}

// TestOpenCode2Plugin drives the plugin's OpenCode 2 TUI half through a fake
// TUI context: only sessions the TUI shows report, subagents never.
func TestOpenCode2Plugin(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("needs node and a shell script as the hook binary")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.jsonl")
	fake := filepath.Join(dir, "pitwall")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1 $2\" = \"hook opencode\" ] || exit 1\n{ cat; echo; } >> \"$OUT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "tui.mjs")
	if err := os.WriteFile(plugin, OpenCodePlugin(fake), 0o600); err != nil {
		t.Fatal(err)
	}
	const want = 16
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(`import fs from "node:fs";
const plugin = (await import(process.argv[2])).default;
let listener;
let route = { type: "home" };
const tabs = [{ sessionID: "t1" }];
const dispose = plugin.setup({
	ui: { router: { current: () => route }, tabs: { list: () => tabs } },
	data: { listen: (fn) => { listener = fn; return () => { listener = undefined; }; } },
});
if (typeof dispose !== "function") throw new Error("setup returned no cleanup");
let at = 1000;
const ev = (type, data) => listener({ details: { type, data, created: (at += 100) } });
ev("session.inbox.enqueued", { sessionID: "other", inboxID: "i0", item: { type: "user", payload: { text: "not mine" } } });
route = { type: "session", sessionID: "s1" };
ev("session.inbox.enqueued", { sessionID: "s1", inboxID: "i1", item: { type: "user", payload: { text: "fix it" }, delivery: "steer" } });
ev("session.execution.started", { sessionID: "s1" });
ev("session.created", { sessionID: "c1", parentID: "s1" });
route = { type: "session", sessionID: "c1" };
ev("session.tool.input.started", { sessionID: "c1", assistantMessageID: "m0", id: "x", name: "read" });
ev("session.tool.called", { sessionID: "c1", assistantMessageID: "m0", id: "x", input: {} });
ev("session.tool.failed", { sessionID: "c1", assistantMessageID: "m0", id: "x", error: { type: "e", message: "child" } });
route = { type: "home" };
ev("session.tool.input.started", { sessionID: "s1", assistantMessageID: "m1", id: "t1", name: "shell" });
ev("session.tool.called", { sessionID: "s1", assistantMessageID: "m1", id: "t1", input: { command: "tsc" } });
ev("session.tool.success", { sessionID: "s1", assistantMessageID: "m1", id: "t1", content: [{ type: "text", text: "error TS2304" }], metadata: { exit: 2 } });
ev("session.tool.input.started", { sessionID: "s1", assistantMessageID: "m1", id: "t2", name: "shell" });
ev("session.tool.called", { sessionID: "s1", assistantMessageID: "m1", id: "t2", input: { command: "echo secret-arg" } });
ev("session.tool.success", { sessionID: "s1", assistantMessageID: "m1", id: "t2", content: [{ type: "text", text: "ok" }], metadata: { exit: 0 } });
ev("session.tool.input.started", { sessionID: "s1", assistantMessageID: "m1", id: "t3", name: "read" });
ev("session.tool.called", { sessionID: "s1", assistantMessageID: "m1", id: "t3", input: { path: "/r/x" } });
ev("session.tool.failed", { sessionID: "s1", assistantMessageID: "m1", id: "t3", error: { type: "e", message: "File not found" } });
ev("permission.asked", { id: "p1", sessionID: "s1", action: "shell", resources: [] });
ev("permission.replied", { sessionID: "s1", requestID: "p1", reply: "once" });
ev("form.created", { form: { id: "f1", sessionID: "s1", title: "Questions", fields: [{ key: "q0", title: "Database", description: "Which database?" }] } });
ev("form.replied", { id: "f1", sessionID: "s1", answer: {} });
ev("session.text.ended", { sessionID: "s1", assistantMessageID: "m2", ordinal: 0, text: "All done." });
ev("session.execution.succeeded", { sessionID: "s1" });
ev("session.inbox.enqueued", { sessionID: "t1", inboxID: "i2", item: { type: "user", payload: { text: "" } } });
ev("session.execution.interrupted", { sessionID: "t1", reason: "user" });
ev("session.execution.started", { sessionID: "t1" });
ev("session.execution.failed", { sessionID: "t1", error: { type: "api", message: "rate limited" } });
const end = Date.now() + 10000;
while (Date.now() < end) {
	const n = fs.existsSync(process.env.OUT) ? fs.readFileSync(process.env.OUT, "utf8").split("\n").filter(Boolean).length : 0;
	if (n >= Number(process.argv[3])) break;
	await new Promise((r) => setTimeout(r, 20));
}
await new Promise((r) => setTimeout(r, 200)); // nothing more may follow
dispose();
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harness, plugin, fmt.Sprint(want))
	cmd.Env = append(os.Environ(), "PITWALL_PANE=p1", "OUT="+out)
	cmd.WaitDelay = 15 * time.Second
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-arg") || strings.Contains(string(b), "not mine") || strings.Contains(string(b), "child") {
		t.Errorf("sent another session's events or a passing command: %s", b)
	}
	var got, tools []string
	var stop, failure, form map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		ev := m["hook_event_name"].(string)
		switch ev {
		case "UserPromptSubmit":
			ev += ":" + m["prompt"].(string)
		case "PreToolUse":
			ev += ":" + m["tool_name"].(string)
		case "Notification":
			ev += ":" + m["notification_type"].(string)
			if m["notification_type"] == "elicitation_dialog" {
				form = m
			}
		case "Stop":
			stop = m
		case "StopFailure":
			failure = m
		case "PostToolUseFailure":
			f, ok := ToolFailure(model.ProviderOpenCode, []byte(line))
			if !ok {
				t.Errorf("ToolFailure(%s) not ok", line)
			}
			tools = append(tools, fmt.Sprintf("%+v", f))
		}
		got = append(got, ev)
	}
	wantEvents := []string{"SessionStart", "UserPromptSubmit:fix it", "PreToolUse:shell", "PostToolUseFailure", "PreToolUse:shell", "PreToolUse:read", "PostToolUseFailure",
		"Notification:permission_prompt", "PostToolUse", "Notification:elicitation_dialog", "PostToolUse", "Stop",
		"UserPromptSubmit:", "Interrupt", "PostToolUse", "StopFailure"}
	if len(wantEvents) != want || strings.Join(got, " ") != strings.Join(wantEvents, " ") {
		t.Fatalf("events\n got %v\nwant %v", got, wantEvents)
	}
	if w := []string{
		fmt.Sprintf("%+v", Failure{Tool: "shell", Command: "tsc", Output: "error TS2304", Exit: 2, Ms: 100, Shell: true}),
		fmt.Sprintf("%+v", Failure{Tool: "read", Command: "/r/x", Output: "File not found", Ms: 100}),
	}; strings.Join(tools, "\n") != strings.Join(w, "\n") {
		t.Errorf("failures\n got %v\nwant %v", tools, w)
	}
	if stop["last_assistant_message"] != "All done." || stop["session_id"] != "s1" || failure["error"] != "rate limited" || failure["session_id"] != "t1" || form["message"] != "Which database?" {
		t.Errorf("Stop %v, StopFailure %v, form %v", stop, failure, form)
	}
}
