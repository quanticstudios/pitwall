package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestStatuslinePassthrough: the wrapped command gets Claude Code's JSON
// and its output, escapes, invalid UTF-8 and missing final newline
// included, and exit code come through unchanged, while the limits land
// in the state directory.
func TestStatuslinePassthrough(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("PITWALL_PANE", "") // never report to a live daemon
	in := `{"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":91,"resets_at":1800003600}}}`
	var out, errOut bytes.Buffer
	code := runStatusline([]string{`cat; printf '\033[1m it'"'"'s\377 \n\n  end'; exit 3`}, strings.NewReader(in), &out, &errOut)
	want := in + "\x1b[1m it's\xff \n\n  end"
	if code != 3 || out.String() != want || errOut.Len() != 0 {
		t.Fatalf("code %d, out %q, want %q, stderr %q", code, out.String(), want, errOut.String())
	}
	data, err := os.ReadFile(filepath.Join(state, "pitwall", flow.ClaudeLimitsFile))
	if err != nil {
		t.Fatal(err)
	}
	var l flow.Limit
	if json.Unmarshal(data, &l) != nil || len(l.Windows) != 1 || l.Windows[0].Used != 91 {
		t.Fatalf("saved %s", data)
	}

	out.Reset()
	if code := runStatusline(nil, strings.NewReader(in), &out, &errOut); code != 0 || out.Len() != 0 {
		t.Fatalf("no command: code %d, out %q", code, out.String())
	}
}

// TestHooksStatusline: install --statusline wraps the user's statusLine
// command and keeps its other fields, a second install changes nothing,
// a plain install leaves it alone, and uninstall puts the command back;
// without one, uninstall drops the statusLine it added.
func TestHooksStatusline(t *testing.T) {
	home := hooksHome(t)
	bin := testBin
	path := filepath.Join(home, ".claude", "settings.json")
	mine := `~/.claude/statusline.sh --say 'it'"'"'s'`
	writeHooksTestFile(t, path, `{"statusLine":{"type":"command","command":`+quoteJSON(mine)+`,"padding":2}}`)
	statusLine := func() map[string]any {
		t.Helper()
		var v struct{ StatusLine map[string]any }
		if err := json.Unmarshal(readHooksTestFile(t, path), &v); err != nil {
			t.Fatal(err)
		}
		return v.StatusLine
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := runHooks(args, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	run("install")
	if got := statusLine()["command"]; got != mine {
		t.Fatalf("a plain install changed the status line: %v", got)
	}
	if out := run("install", "--statusline", "--dry-run"); !strings.Contains(out, "wrapped statusLine: "+mine) {
		t.Fatalf("dry run: %s", out)
	}
	run("install", "--statusline")
	sl := statusLine()
	if sl["command"] != agent.ClaudeStatusline(bin, mine) || sl["padding"] != 2.0 || sl["type"] != "command" {
		t.Fatalf("installed %v", sl)
	}
	if out := run("install", "--statusline"); strings.Contains(out, "statusLine") {
		t.Fatalf("second install: %s", out)
	}
	run("uninstall")
	if sl := statusLine(); sl["command"] != mine || sl["padding"] != 2.0 {
		t.Fatalf("uninstalled %v", sl)
	}

	writeHooksTestFile(t, path, `{"model":"keep"}`)
	run("install", "--statusline")
	if sl := statusLine(); sl["command"] != agent.ClaudeStatusline(bin, "") {
		t.Fatalf("installed %v", sl)
	}
	run("uninstall")
	if sl := statusLine(); sl != nil {
		t.Fatalf("uninstall kept %v", sl)
	}
}

func quoteJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

const statuslineJSON = `{"session_id":"s1","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},"effort":{"level":"high"},` +
	`"context_window":{"context_window_size":1000000,"current_usage":{"input_tokens":1,"cache_read_input_tokens":99}},"cost":{"total_cost_usd":1.5}}`

// statuslineDaemon is a socket for pitwall statusline: each connection's
// events go to the channel, and with hang set it never answers or closes.
func statuslineDaemon(t *testing.T, hang bool) <-chan any {
	t.Helper()
	setHome(t, t.TempDir())
	path := sockPath(t, "sl.sock")
	t.Setenv("PITWALL_SOCKET", path)
	t.Setenv("PITWALL_PANE", "p1")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan any, 8)
	var conns []net.Conn
	var mu sync.Mutex
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			if hang {
				continue
			}
			go func() {
				defer nc.Close()
				nc.SetDeadline(time.Now().Add(5 * time.Second))
				c := proto.NewConn(nc)
				for {
					m, err := c.Recv()
					if err != nil {
						return
					}
					if ev, ok := m.(proto.AgentEvent); ok {
						got <- ev
					}
				}
			}()
		}
	}()
	return got
}

func TestStatuslineReportsToDaemon(t *testing.T) {
	got := statuslineDaemon(t, false)
	var out bytes.Buffer
	if code := runStatusline([]string{"cat"}, strings.NewReader(statuslineJSON), &out, io.Discard); code != 0 || out.String() != statuslineJSON {
		t.Fatalf("code %d, out %q", code, out.String())
	}
	select {
	case m := <-got:
		ev := m.(proto.AgentEvent)
		st, ok := agent.DecodeStatusline(ev.Payload)
		want := agent.StatuslineState{SessionID: "s1", Model: "claude-opus-5-5", ModelName: "Opus 5.5", Effort: "high", ContextUsed: 100, ContextSize: 1_000_000, CostUSD: 1.5}
		if ev.Pane != "p1" || ev.Provider != "report" || !ok || st != want {
			t.Fatalf("event %+v, state %+v %v", ev, st, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon heard nothing")
	}
}

// A daemon that accepts and never answers, or no daemon at all, must not
// hold the status line, and the user's command output stays as it is.
func TestStatuslineNeverBlocks(t *testing.T) {
	for _, name := range []string{"hung", "gone"} {
		t.Run(name, func(t *testing.T) {
			statuslineDaemon(t, name == "hung")
			if name == "gone" {
				t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
			}
			var out bytes.Buffer
			start := time.Now()
			code := runStatusline([]string{"printf 'my line'; exit 4"}, strings.NewReader(statuslineJSON), &out, io.Discard)
			if code != 4 || out.String() != "my line" {
				t.Fatalf("code %d, out %q", code, out.String())
			}
			if d := time.Since(start); d > statuslineWait+time.Second {
				t.Fatalf("took %v", d)
			}
		})
	}
}

func TestStatuslineOutsidePaneSendsNothing(t *testing.T) {
	got := statuslineDaemon(t, false)
	t.Setenv("PITWALL_PANE", "")
	runStatusline(nil, strings.NewReader(statuslineJSON), io.Discard, io.Discard)
	select {
	case m := <-got:
		t.Fatalf("sent %+v", m)
	case <-time.After(300 * time.Millisecond):
	}
}
