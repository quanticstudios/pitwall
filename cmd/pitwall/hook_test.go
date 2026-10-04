package main

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeDaemon accepts hook connections and answers a Reply event with
// reply, or with nothing when reply is nil.
func fakeDaemon(t *testing.T, reply []byte) (string, chan proto.AgentEvent) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan proto.AgentEvent, 4)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				c := proto.NewConn(nc)
				defer c.Close()
				for {
					m, err := c.Recv()
					if err != nil {
						return
					}
					if ev, ok := m.(proto.AgentEvent); ok {
						got <- ev
						if ev.Reply && reply != nil {
							c.Send(proto.HookReply{Output: reply})
						}
					}
				}
			}()
		}
	}()
	return sock, got
}

func TestSendHook(t *testing.T) {
	const allow = `{"hookSpecificOutput":{"decision":{"behavior":"allow"},"hookEventName":"PermissionRequest"}}`
	sock, got := fakeDaemon(t, []byte(allow))
	req := []byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test"}}`)
	if out := sendHook(sock, "p1", "claude", req, time.Second); string(out) != allow {
		t.Errorf("permission request printed %q", out)
	}
	if ev := <-got; !ev.Reply || ev.Pane != "p1" {
		t.Errorf("event %+v", ev)
	}
	start := time.Now()
	if out := sendHook(sock, "p1", "claude", []byte(`{"hook_event_name":"PreToolUse"}`), time.Second); out != nil {
		t.Errorf("PreToolUse printed %q", out)
	}
	if ev := <-got; ev.Reply {
		t.Error("PreToolUse asked for a reply")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("a hook that needs no reply waited")
	}

	// A daemon that never answers costs at most the wait, and decides nothing.
	quiet, _ := fakeDaemon(t, nil)
	start = time.Now()
	if out := sendHook(quiet, "p1", "claude", req, 100*time.Millisecond); out != nil {
		t.Errorf("silent daemon: printed %q", out)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("waited %v", d)
	}
	// No daemon at all.
	if out := sendHook(filepath.Join(t.TempDir(), "none.sock"), "p1", "claude", req, time.Second); out != nil {
		t.Errorf("no daemon: printed %q", out)
	}
}
