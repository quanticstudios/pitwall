package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// silentDaemon accepts hook connections, records each event and never
// answers.
func silentDaemon(t *testing.T) (string, chan proto.AgentEvent) {
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
					}
				}
			}()
		}
	}()
	return sock, got
}

// TestHookReportsAndExits: a permission request's hook forwards the event
// and exits at once with nothing on stdout, so it never delays or decides
// the agent's prompt, even when the daemon never answers.
func TestHookReportsAndExits(t *testing.T) {
	sock, got := silentDaemon(t)
	t.Setenv("PITWALL_PANE", "p1")
	t.Setenv("PITWALL_SOCKET", sock)
	req := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test"}}`
	in := filepath.Join(t.TempDir(), "in.json")
	os.WriteFile(in, []byte(req), 0o600)
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdin, w
	start := time.Now()
	runHook([]string{"claude"})
	took := time.Since(start)
	os.Stdin, os.Stdout = oldIn, oldOut
	w.Close()
	out, _ := io.ReadAll(r)
	if len(out) != 0 {
		t.Errorf("the hook printed %q", out)
	}
	if took > 300*time.Millisecond {
		t.Errorf("the hook took %v", took)
	}
	select {
	case ev := <-got:
		if ev.Pane != "p1" || string(ev.Payload) != req {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the daemon got no event")
	}
	// No daemon at all costs nothing either.
	start = time.Now()
	sendHook(filepath.Join(t.TempDir(), "none.sock"), "p1", "claude", []byte(req))
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("no daemon: took %v", d)
	}
}
