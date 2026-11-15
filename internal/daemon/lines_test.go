//go:build linux

package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// jsonConn is a JSON lines client of the daemon's socket.
type jsonConn struct {
	nc net.Conn
	br *bufio.Reader
}

func dialJSON(t *testing.T, sock string) *jsonConn {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	nc.SetDeadline(time.Now().Add(20 * time.Second))
	return &jsonConn{nc, bufio.NewReader(nc)}
}

func (c *jsonConn) line(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(c.nc, s+"\n"); err != nil {
		t.Fatal(err)
	}
}

// next is the next frame, nil at the end of the connection.
func (c *jsonConn) next(t *testing.T) map[string]any {
	t.Helper()
	l, err := c.br.ReadBytes('\n')
	if err == io.EOF && len(l) == 0 {
		return nil
	}
	if err != nil {
		t.Fatalf("read: %v (%q)", err, l)
	}
	var m map[string]any
	if err := json.Unmarshal(l, &m); err != nil {
		t.Fatalf("%v: %q", err, l)
	}
	return m
}

const jsonHello = `{"type":"hello","protocol":1,"client":{"name":"script"}}`

func (c *jsonConn) hello(t *testing.T) map[string]any {
	t.Helper()
	c.line(t, jsonHello)
	w := c.next(t)
	if w["type"] != "welcome" || w["protocol"] != 1.0 {
		t.Fatalf("welcome %v", w)
	}
	return w
}

// answers reports whether a gob cli client gets its state back within a
// second.
func answers(t *testing.T, sock string) {
	t.Helper()
	cli := dial(t, sock, "cli")
	start := time.Now()
	cli.send(proto.Sync{})
	cli.waitState("the state", func(model.State) bool { return true })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a gob client waited %s", d)
	}
}

// TestJSONSocket: the socket answers a JSON hello with welcome and the
// control commands alone, refuses a bad protocol with a close line, serves
// no chat command, and gob clients on it work as before.
func TestJSONSocket(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	for _, h := range []string{`{"type":"hello","protocol":2,"client":{"name":"x"}}`, `{"type":"hello","client":{"name":"x"}}`} {
		c := dialJSON(t, sock)
		c.line(t, h)
		if m := c.next(t); m["type"] != "close" || m["code"] != 4002.0 {
			t.Fatalf("%s: got %v, want close 4002", h, m)
		}
		if m := c.next(t); m != nil {
			t.Fatalf("after close: %v", m)
		}
	}
	c := dialJSON(t, sock)
	if caps := fmt.Sprint(c.hello(t)["capabilities"]); caps != fmt.Sprint(controlCommands) {
		t.Fatalf("capabilities %v, want %v", caps, controlCommands)
	}
	for i, typ := range []string{"subscribeSessions", "listSessions", "start", "mergePR", "readUsage"} {
		c.line(t, fmt.Sprintf(`{"type":%q,"id":"%d"}`, typ, i))
		if r := c.next(t); r["type"] != "result" || r["code"] != "unsupported" {
			t.Fatalf("%s: %v", typ, r)
		}
	}
	answers(t, sock)
	c.line(t, `{"type":"listTabs","id":"x"}`) // the JSON client still works
	if r := c.next(t); r["id"] != "x" || r["ok"] != true {
		t.Fatalf("after gob: %v", r)
	}
}

// TestSilentJSONClient: clients that send nothing, or half a line, do not
// delay a gob client, and one silent past helloWait is dropped.
func TestSilentJSONClient(t *testing.T) {
	defer func(d time.Duration) { helloWait = d }(helloWait)
	helloWait = 300 * time.Millisecond
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	silent := dialJSON(t, sock)
	half := dialJSON(t, sock)
	io.WriteString(half.nc, `{"type":"hel`)
	answers(t, sock)
	if m := silent.next(t); m != nil {
		t.Fatalf("silent client got %v, want the connection closed", m)
	}
}

// TestJSONFlood: a JSON client that sends 10,000 listTabs and reads
// nothing is no longer read from, and gob clients are answered meanwhile.
func TestJSONFlood(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	c := dialJSON(t, sock)
	c.hello(t)
	frame := []byte(`{"type":"listTabs","id":"f"` + strings.Repeat(" ", 1000) + "}\n") // 10 MB in all
	wrote := make(chan error, 1)
	go func() {
		for range 10_000 {
			if _, err := c.nc.Write(frame); err != nil {
				wrote <- err
				return
			}
		}
		wrote <- nil
	}()
	time.Sleep(500 * time.Millisecond)
	answers(t, sock)
	select {
	case err := <-wrote:
		t.Fatalf("the daemon read every frame of a client that reads nothing (%v)", err)
	default:
	}
}

// TestPeekKeepsPeer: a gob client's connection after the peek is the
// same *net.UnixConn, with its peer credentials and every byte.
func TestPeekKeepsPeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "pwp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := proto.Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hello := proto.Hello{Version: proto.Version, Kind: "hook"}
	c.Send(hello)
	nc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if isJSON, err := jsonClient(nc); isJSON || err != nil {
		t.Fatalf("jsonClient %v, %v", isJSON, err)
	}
	if pid := peerPID(nc); pid != os.Getpid() {
		t.Fatalf("peerPID %d, want %d", pid, os.Getpid())
	}
	if m, err := proto.NewConn(nc).Recv(); err != nil || m != hello {
		t.Fatalf("Recv %v, %v", m, err)
	}
}

// TestJSONHandoff: a handoff closes an open JSON connection, as it does
// gob ones; the panes keep their I/O; a new daemon answers a fresh hello
// and a wait redialled on it sees a pane exit.
func TestJSONHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("starts daemon processes")
	}
	dir, err := os.MkdirTemp("", "pwj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, f := range []string{"burst", "input", "tui"} {
		if err := syscall.Mkfifo(filepath.Join(dir, f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(dir, "d.sock")
	old := startDaemonProc(t, dir, "old", "")
	if err := old.serving(t); err != nil {
		t.Fatal(err)
	}
	c := dial(t, sock, "cli")
	awaitContains(t, c, "echo0", "ready")
	j := dialJSON(t, sock)
	j.hello(t)
	watch := dial(t, sock, "watch")
	watch.waitState("the old state", func(model.State) bool { return true })

	next := startDaemonProc(t, dir, "new", "")
	if err := next.serving(t); err != nil {
		t.Fatal(err)
	}
	if err := old.waitExit(t); err != nil {
		t.Fatalf("old daemon after handoff: %v: %s", err, old.err.String())
	}
	for m := j.next(t); m != nil; m = j.next(t) {
		if m["type"] != "close" {
			t.Fatalf("the JSON client got %v after the handoff", m)
		}
	}
	for range watch.in { // the old watch ends with its daemon
	}

	dialJSON(t, sock).hello(t)
	c = dial(t, sock, "cli")
	typeIn(c, "echo0", "a2\n")
	awaitContains(t, c, "echo0", "out:a2")
	watch = dial(t, sock, "watch")
	watch.waitState("the new state", func(model.State) bool { return true })
	typeIn(c, "sh0", "exit 7\n")
	watch.waitFor("sh0's exit", func(m any) bool { e, ok := m.(proto.PaneExited); return ok && e.Pane == "sh0" && e.ExitCode == 7 })
}
