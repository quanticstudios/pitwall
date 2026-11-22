//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// startRealDaemon starts a real daemon on a short temp unix socket and returns
// its socket path and a cancel function to shut it down early when needed.
func startRealDaemon(t *testing.T) (sock string, stop func()) {
	t.Helper()
	home := t.TempDir()
	setHome(t, home)
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(k, t.TempDir())
	}
	sock = sockPath(t, "rex.sock")
	t.Setenv("PITWALL_SOCKET", sock)
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_ATTACH", "")

	d, err := daemon.New()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(stop)
	return sock, stop
}

func TestCLINewAndSplitWait(t *testing.T) {
	startRealDaemon(t)
	dir := t.TempDir()

	// 1. new --wait exit 0: prints pane address first, exits 0.
	code, out, stderr := cliOutput("new", "--wait", dir, "--", "/bin/sh", "-c", "exit 0")
	if code != 0 || strings.TrimSpace(out) != "#1.1" {
		t.Fatalf("new --wait exit 0: code=%d out=%q err=%q", code, out, stderr)
	}

	// 2. new --wait exit 7: prints pane address, exits 7.
	code, out, stderr = cliOutput("new", "--wait", dir, "--", "/bin/sh", "-c", "exit 7")
	if code != 7 || strings.TrimSpace(out) != "#2.1" {
		t.Fatalf("new --wait exit 7: code=%d out=%q err=%q", code, out, stderr)
	}

	// 3. new --wait --json exit 5: one JSON object with exit_code 5.
	code, out, stderr = cliOutput("new", "--wait", "--json", dir, "--", "/bin/sh", "-c", "exit 5")
	if code != 5 {
		t.Fatalf("new --wait --json exit 5: code=%d out=%q err=%q", code, out, stderr)
	}
	var res runResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("unmarshal new --wait --json %q: %v", out, err)
	}
	if res.Tab != "#3" || res.Pane != "#3.1" || res.TabID == "" || res.PaneID == "" || res.Outcome != "exited" || res.ExitCode == nil || *res.ExitCode != 5 {
		t.Fatalf("new --wait --json result = %+v", res)
	}

	// 4. split --wait exit 9 on tab #1.
	code, out, stderr = cliOutput("split", "--wait", "#1", "--", "/bin/sh", "-c", "exit 9")
	if code != 9 || strings.TrimSpace(out) != "#1.2" {
		t.Fatalf("split --wait exit 9: code=%d out=%q err=%q", code, out, stderr)
	}

	// 5. split --wait --json exit 0 on tab #1.
	code, out, stderr = cliOutput("split", "--wait", "--json", "#1", "--", "/bin/sh", "-c", "exit 0")
	if code != 0 {
		t.Fatalf("split --wait --json: code=%d out=%q err=%q", code, out, stderr)
	}
	res = runResult{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("unmarshal split --wait --json %q: %v", out, err)
	}
	if res.Tab != "#1" || res.Pane != "#1.2" || res.Outcome != "exited" || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("split --wait --json result = %+v", res)
	}

	// 6. new --wait --timeout exits 124.
	code, _, stderr = cliOutput("new", "--wait", "--timeout", "150ms", dir, "--", "/bin/sh", "-c", "sleep 5")
	if code != 124 {
		t.Fatalf("new --wait --timeout: code=%d err=%q, want 124", code, stderr)
	}
}

func TestCLIReadAndScrollbackFormats(t *testing.T) {
	startRealDaemon(t)
	vt.SetPalette(vt.Palette{
		Fg:   0xe6e6e6,
		Bg:   0x121314,
		ANSI: [16]uint32{1: 0xff3344},
	})
	dir := t.TempDir()

	// Run a command that prints colored lines and exits (pane stays open with output).
	code, out, stderr := cliOutput("new", "--wait", dir, "--", "/bin/sh", "-c", `printf '\033[1;31mREDLINE\033[0m\nPLAIN\n'`)
	if code != 0 {
		t.Fatalf("new --wait: code=%d out=%q err=%q", code, out, stderr)
	}
	pane := strings.TrimSpace(out)

	// Text format.
	code, textOut, stderr := cliOutput("read", pane, "--screen", "--format", "text")
	if code != 0 || !strings.Contains(textOut, "REDLINE") || strings.Contains(textOut, "\x1b[") {
		t.Fatalf("read text: code=%d out=%q err=%q", code, textOut, stderr)
	}

	// ANSI format.
	code, ansiOut, stderr := cliOutput("read", pane, "--screen", "--format", "ansi")
	if code != 0 || !strings.Contains(ansiOut, "REDLINE") || !strings.Contains(ansiOut, "\x1b[") {
		t.Fatalf("read ansi: code=%d out=%q err=%q", code, ansiOut, stderr)
	}

	// HTML format with active palette colors.
	code, htmlOut, stderr := cliOutput("read", pane, "--screen", "--format", "html")
	if code != 0 || !strings.Contains(htmlOut, "<pre ") || !strings.Contains(htmlOut, "#121314") || !strings.Contains(htmlOut, "#ff3344") || !strings.Contains(htmlOut, "REDLINE") {
		t.Fatalf("read html: code=%d out=%q err=%q", code, htmlOut, stderr)
	}

	// read --scrollback --lines --format html.
	code, sbHTML, stderr := cliOutput("read", pane, "--scrollback", "--lines", "10", "--format", "html")
	if code != 0 || !strings.Contains(sbHTML, "<pre ") || !strings.Contains(sbHTML, "REDLINE") {
		t.Fatalf("read --scrollback html: code=%d out=%q err=%q", code, sbHTML, stderr)
	}

	// scrollback --format ansi.
	code, sbANSI, stderr := cliOutput("scrollback", pane, "--format", "ansi")
	if code != 0 || !strings.Contains(sbANSI, "\x1b[") || !strings.Contains(sbANSI, "REDLINE") {
		t.Fatalf("scrollback ansi: code=%d out=%q err=%q", code, sbANSI, stderr)
	}
}

func TestCLISendKey(t *testing.T) {
	startRealDaemon(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "got.txt")

	// Start a pane that reads one line and writes it to marker.
	code, tab, stderr := cliOutput("new", dir, "--", "/bin/sh", "-c", `IFS= read -r line; printf '%s' "$line" > "$1"`, "sh", marker)
	if code != 0 {
		t.Fatalf("new: code=%d err=%q", code, stderr)
	}
	tab = strings.TrimSpace(tab)
	// Give the shell a moment to reach read.
	time.Sleep(100 * time.Millisecond)

	code, _, stderr = cliOutput("send-key", tab, "y", "e", "s", "enter")
	if code != 0 {
		t.Fatalf("send-key: code=%d err=%q", code, stderr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(marker)
		if err == nil && string(b) == "yes" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker never got 'yes' (got %q, err=%v)", string(b), err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Guarded tab refuses send-key: install hooks in temp HOME pointing at a wrapper around the test binary and start a fake claude with --guard.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakePitwall := filepath.Join(dir, "pitwall")
	if err := os.WriteFile(fakePitwall, []byte("#!/bin/sh\nexec env PITWALL_TEST_MAIN=1 \""+exe+"\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(os.Getenv("HOME"), ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"hooks":`+string(agent.ClaudeHooks(fakePitwall))+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(dir, "claude")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, gtab, stderr := cliOutput("new", "--guard", dir, "--", fakeBin)
	if code != 0 {
		t.Fatalf("new --guard: code=%d err=%q", code, stderr)
	}
	gtab = strings.TrimSpace(gtab)
	code, _, stderr = cliOutput("send-key", gtab, "enter")
	if code == 0 || !strings.Contains(strings.ToLower(stderr), "guard") {
		t.Fatalf("guarded send-key: code=%d err=%q, want refusal mentioning guard", code, stderr)
	}
}

func TestCLIEvents(t *testing.T) {
	_, stop := startRealDaemon(t)
	dir := t.TempDir()

	// Create an initial tab so a session exists.
	code, _, stderr := cliOutput("new", dir, "--", "/bin/sh", "-c", "sleep 30")
	if code != 0 {
		t.Fatalf("new initial: code=%d err=%q", code, stderr)
	}

	// 1. Stream JSON events and cancel via Ctrl+C (ctx) -> exit 0.
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	var errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runEvents(ctx, []string{"--json"}, pw, &errOut)
		pw.Close()
	}()

	// Wait briefly for subscription to attach, then open a tab that exits with code 3.
	time.Sleep(150 * time.Millisecond)
	code, _, stderr = cliOutput("new", "--wait", dir, "--", "/bin/sh", "-c", "exit 3")
	if code != 3 {
		t.Fatalf("new --wait exit 3: code=%d err=%q", code, stderr)
	}

	seenOpened, seenExited := false, false
	sc := bufio.NewScanner(pr)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for sc.Scan() {
			var ev map[string]any
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				continue
			}
			switch ev["event"] {
			case "tab.opened":
				seenOpened = true
			case "pane.exited":
				if ev["exitCode"] == float64(3) {
					seenExited = true
				}
			}
			if seenOpened && seenExited {
				cancel()
				return
			}
		}
	}()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("timed out waiting for events (opened=%v exited=%v err=%q)", seenOpened, seenExited, errOut.String())
	}
	go io.Copy(io.Discard, pr) // why: events may be mid-write when the reader stops; a full pipe would hide the cancel
	if exitCode := <-done; exitCode != 0 {
		t.Fatalf("events Ctrl+C exit = %d, want 0 (err=%q)", exitCode, errOut.String())
	}

	// 2. When daemon stops, events exits non-zero.
	var out2, errOut2 bytes.Buffer
	done2 := make(chan int, 1)
	go func() {
		done2 <- runEvents(context.Background(), nil, &out2, &errOut2)
	}()
	time.Sleep(150 * time.Millisecond)
	stop()
	select {
	case exitCode := <-done2:
		if exitCode == 0 {
			t.Fatalf("events after daemon stop exited 0, want non-zero (err=%q)", errOut2.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events did not exit after daemon stopped")
	}
}

func TestCLIAPI(t *testing.T) {
	startRealDaemon(t)
	dir := t.TempDir()
	code, _, stderr := cliOutput("new", "-n", "api-tab", dir, "--", "/bin/sh", "-c", "sleep 30")
	if code != 0 {
		t.Fatalf("new: code=%d err=%q", code, stderr)
	}

	// 1. Positional arg without id: auto-handshake + auto-id.
	var out, errOut bytes.Buffer
	code = runAPI(context.Background(), []string{`{"type":"listTabs"}`}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("api arg: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &res); err != nil {
		t.Fatalf("api arg output invalid JSON %q: %v", out.String(), err)
	}
	if res["type"] != "result" || res["ok"] != true || !strings.Contains(out.String(), "api-tab") {
		t.Fatalf("api arg output = %q", out.String())
	}

	// 2. Multiple lines on stdin (explicit id + auto id).
	out.Reset()
	errOut.Reset()
	stdin := strings.NewReader(`{"type":"listTabs","id":"q1"}` + "\n" + `{"type":"queueList"}` + "\n")
	code = runAPI(context.Background(), nil, stdin, &out, &errOut)
	if code != 0 {
		t.Fatalf("api stdin: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"id":"q1"`) || !strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("api stdin lines = %q", lines)
	}

	// 3. Explicit hello prints welcome too.
	out.Reset()
	errOut.Reset()
	code = runAPI(context.Background(), []string{`{"type":"hello","protocol":1}`, `{"type":"listTabs","id":"h1"}`}, nil, &out, &errOut)
	if code != 0 {
		t.Fatalf("api explicit hello: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"welcome"`) || !strings.Contains(lines[1], `"id":"h1"`) {
		t.Fatalf("api explicit hello lines = %q", lines)
	}
}

func TestSessionStartHookContext(t *testing.T) {
	sock, _ := startRealDaemon(t)
	dir := t.TempDir()
	code, jsonOut, stderr := cliOutput("new", "--json", "-n", "worker-alpha", dir, "--", "/bin/sh", "-c", "sleep 30")
	if code != 0 {
		t.Fatalf("new --json: code=%d err=%q", code, stderr)
	}
	var res runResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &res); err != nil {
		t.Fatalf("unmarshal new --json: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// Claude Code SessionStart emits hookSpecificOutput.additionalContext with pane & tab.
	for _, provider := range []string{"claude", "codex"} {
		cmd := exec.Command(exe, "hook", provider)
		cmd.Env = append(os.Environ(),
			"PITWALL_TEST_MAIN=1",
			"PITWALL_SOCKET="+sock,
			"PITWALL_PANE="+res.PaneID,
		)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s1","transcript_path":"/tmp/t.jsonl"}`)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("hook %s SessionStart failed: %v", provider, err)
		}
		var parsed struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
			t.Fatalf("hook %s output invalid JSON %q: %v", provider, string(out), err)
		}
		ctxText := parsed.HookSpecificOutput.AdditionalContext
		if parsed.HookSpecificOutput.HookEventName != "SessionStart" ||
			!strings.Contains(ctxText, "@"+res.PaneID) ||
			!strings.Contains(ctxText, "#1") ||
			!strings.Contains(ctxText, "worker-alpha") ||
			!strings.Contains(ctxText, "pitwall") {
			t.Fatalf("hook %s additionalContext = %q", provider, ctxText)
		}
	}

	// Non-SessionStart hook emits nothing on stdout.
	cmd := exec.Command(exe, "hook", "claude")
	cmd.Env = append(os.Environ(),
		"PITWALL_TEST_MAIN=1",
		"PITWALL_SOCKET="+sock,
		"PITWALL_PANE="+res.PaneID,
	)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"done"}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook claude Stop failed: %v", err)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("Stop hook emitted unexpected stdout: %q", string(out))
	}
}
