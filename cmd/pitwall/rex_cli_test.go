package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRexCLIArgParsingAndHelp(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		opt, err := parseEventsArgs([]string{"--json", "--session", "work", "--pane", "#1.2"})
		if err != nil || !opt.asJSON || opt.session != "work" || opt.pane != "#1.2" {
			t.Fatalf("parseEventsArgs = %+v, %v", opt, err)
		}
		opt, err = parseEventsArgs([]string{"-s", "main"})
		if err != nil || opt.session != "main" {
			t.Fatalf("parseEventsArgs -s = %+v, %v", opt, err)
		}
		opt, err = parseEventsArgs([]string{"--help"})
		if err != nil || !opt.help {
			t.Fatalf("parseEventsArgs --help = %+v, %v", opt, err)
		}
		if _, err := parseEventsArgs([]string{"extra"}); err == nil {
			t.Fatal("expected error for positional arg")
		}
		var out, errOut bytes.Buffer
		if code := eventsCommand([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "usage: pitwall events") {
			t.Fatalf("events --help: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("send-key", func(t *testing.T) {
		opt, err := parseSendKeyArgs([]string{"-s", "work", "#1.2", "up", "enter", "ctrl+c"})
		if err != nil || opt.session != "work" || opt.pane != "#1.2" || len(opt.keys) != 3 {
			t.Fatalf("parseSendKeyArgs = %+v, %v", opt, err)
		}
		opt, err = parseSendKeyArgs([]string{"#1", "--", "-", "enter"})
		if err != nil || opt.pane != "#1" || strings.Join(opt.keys, ",") != "-,enter" {
			t.Fatalf("parseSendKeyArgs -- = %+v, %v", opt, err)
		}
		if _, err := parseSendKeyArgs([]string{"#1"}); err == nil {
			t.Fatal("expected error with no keys")
		}
		if _, err := parseSendKeyArgs([]string{"#1", "not-a-key"}); err == nil {
			t.Fatal("expected error for unknown key")
		}
		var out, errOut bytes.Buffer
		if code := sendKeyCommand([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "usage: pitwall send-key") {
			t.Fatalf("send-key --help: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("api", func(t *testing.T) {
		opt, err := parseAPIArgs(nil)
		if err != nil || !opt.stdin {
			t.Fatalf("parseAPIArgs nil = %+v, %v", opt, err)
		}
		opt, err = parseAPIArgs([]string{"-"})
		if err != nil || !opt.stdin {
			t.Fatalf("parseAPIArgs - = %+v, %v", opt, err)
		}
		opt, err = parseAPIArgs([]string{`{"type":"listTabs"}`})
		if err != nil || opt.stdin || len(opt.args) != 1 {
			t.Fatalf("parseAPIArgs arg = %+v, %v", opt, err)
		}
		var out, errOut bytes.Buffer
		if code := apiCommand([]string{"--help"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "usage: pitwall api") {
			t.Fatalf("api --help: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("new and split flags", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := newCommand([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "--wait") {
			t.Fatalf("new --help: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
		out.Reset()
		errOut.Reset()
		if code := newCommand([]string{"--timeout", "1s"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--timeout requires --wait") {
			t.Fatalf("new --timeout without --wait: code=%d err=%q", code, errOut.String())
		}
		out.Reset()
		errOut.Reset()
		if code := splitCommand([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "--wait") {
			t.Fatalf("split --help: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
		out.Reset()
		errOut.Reset()
		if code := splitCommand([]string{"--timeout", "1s"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--timeout requires --wait") {
			t.Fatalf("split --timeout without --wait: code=%d err=%q", code, errOut.String())
		}
	})

	t.Run("read and scrollback flags", func(t *testing.T) {
		var out bytes.Buffer
		if err := readCommand([]string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--format") {
			t.Fatalf("read --help: out=%q err=%v", out.String(), err)
		}
		for _, args := range [][]string{
			{"#1", "--screen", "--format", "bogus"},
			{"#1", "--screen", "--scrollback"},
			{"#1", "--lines", "10"},
			{"#1", "--format", "ansi"},
			{"#1", "--scrollback", "--lines", "-1"},
		} {
			if err := readCommand(args, &out); err == nil {
				t.Errorf("read %v: expected error", args)
			}
		}
		out.Reset()
		if err := scrollbackCommand([]string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--format") {
			t.Fatalf("scrollback --help: out=%q err=%v", out.String(), err)
		}
		if err := scrollbackCommand([]string{"#1", "--format", "bogus"}, &out); err == nil {
			t.Error("scrollback --format bogus: expected error")
		}
	})
}

func TestPrepareAPILine(t *testing.T) {
	var seq int64
	// Injects id when omitted.
	out, id, typ, hasID := prepareAPILine([]byte(`{"type":"listTabs"}`), &seq)
	if !hasID || id != "api-1" || typ != "listTabs" {
		t.Fatalf("got out=%s id=%q typ=%q hasID=%v", out, id, typ, hasID)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil || m["id"] != "api-1" {
		t.Fatalf("marshaled = %s, err=%v", out, err)
	}
	// Preserves caller id verbatim.
	raw := []byte(`{"type":"listTabs","id":"custom"}`)
	out, id, typ, hasID = prepareAPILine(raw, &seq)
	if !hasID || id != "custom" || typ != "listTabs" || string(out) != string(raw) {
		t.Fatalf("preserve id: out=%s id=%q typ=%q hasID=%v", out, id, typ, hasID)
	}
	// Leaves non-JSON/untyped lines without id.
	rawBad := []byte(`{"noType":true}`)
	out, id, typ, hasID = prepareAPILine(rawBad, &seq)
	if hasID || id != "" || typ != "" || string(out) != string(rawBad) {
		t.Fatalf("untyped: out=%s id=%q typ=%q hasID=%v", out, id, typ, hasID)
	}
}

func TestFormatEventLine(t *testing.T) {
	cases := []struct {
		frame map[string]any
		want  []string
	}{
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "pane.state", "pane": "p1", "agent": "claude", "state": "pending-approval", "detail": "Run\ngo test"},
			[]string{"pane.state", "@p1", "claude", "pending-approval: Run go test"},
		},
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "tab.opened", "tab": "w1", "title": "api"},
			[]string{"tab.opened", "@w1", "api"},
		},
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "pr.state", "tab": "w1", "prNumber": float64(7), "prState": "OPEN", "prReason": "ready"},
			[]string{"pr.state", "@w1", "#7", "OPEN", "ready"},
		},
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "queued.sent", "pane": "p1", "from": "p2", "queued": float64(2)},
			[]string{"queued.sent", "@p1", "from @p2", "(remaining: 2)"},
		},
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "turn.end", "pane": "p1", "agent": "codex", "state": "done"},
			[]string{"turn.end", "@p1", "codex", "done"},
		},
		{
			map[string]any{"at": "2026-11-22T12:00:00Z", "event": "pane.exited", "pane": "p1", "exitCode": float64(9)},
			[]string{"pane.exited", "@p1", "exit 9"},
		},
	}
	for _, tc := range cases {
		line := formatEventLine(tc.frame)
		if strings.Contains(line, "\n") {
			t.Errorf("line has newline: %q", line)
		}
		for _, w := range tc.want {
			if !strings.Contains(line, w) {
				t.Errorf("formatEventLine(%v) = %q, missing %q", tc.frame, line, w)
			}
		}
	}
}
