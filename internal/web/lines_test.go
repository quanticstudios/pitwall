package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// lineClient is a JSON lines client of a Server's ServeLines.
type lineClient struct {
	nc net.Conn
	br *bufio.Reader
}

// dialLines serves one connection of s as JSON lines, over loopback TCP so
// the test runs on every platform, and returns its client end.
func dialLines(t *testing.T, s *Server) *lineClient {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		if nc, err := ln.Accept(); err == nil {
			nc.(*net.TCPConn).SetWriteBuffer(16 << 10) // small, so a client that does not read blocks the writer soon
			s.ServeLines(nc)
		}
	}()
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	nc.(*net.TCPConn).SetReadBuffer(16 << 10)
	nc.SetDeadline(time.Now().Add(20 * time.Second))
	return &lineClient{nc, bufio.NewReaderSize(nc, 1<<20)}
}

func (c *lineClient) line(t *testing.T, s string) {
	t.Helper()
	if _, err := io.WriteString(c.nc, s+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (c *lineClient) send(t *testing.T, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	c.line(t, string(data))
}

func (c *lineClient) json(t *testing.T) map[string]any {
	t.Helper()
	l, err := c.br.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v (%q)", err, l)
	}
	var m map[string]any
	if err := json.Unmarshal(l, &m); err != nil {
		t.Fatalf("%v: %q", err, l)
	}
	return m
}

// closed reads the close line and then the end of the connection.
func (c *lineClient) closed(t *testing.T) (int, string) {
	t.Helper()
	m := c.json(t)
	if m["type"] != "close" {
		t.Fatalf("got %v, want a close line", m)
	}
	if l, err := c.br.ReadBytes('\n'); err != io.EOF {
		t.Fatalf("after close: %q %v, want EOF", l, err)
	}
	code, _ := m["code"].(float64)
	reason, _ := m["reason"].(string)
	return int(code), reason
}

func linesHello(protocol any) map[string]any {
	m := map[string]any{"type": "hello", "client": map[string]string{"name": "script"}}
	if protocol != nil {
		m["protocol"] = protocol
	}
	return m
}

// TestLinesHello: hello needs no token, a wrong or missing protocol is
// refused with a close line, and a server without Chats offers nothing and
// serves no chat command.
func TestLinesHello(t *testing.T) {
	s := &Server{Version: "test", Token: fixed(goodToken)}
	t.Cleanup(s.Close)
	for _, tc := range []struct {
		name   string
		hello  string
		code   int
		reason string
	}{
		{"newer protocol", mustString(linesHello(2)), 4002, "client sent 2"},
		{"no protocol", mustString(linesHello(nil)), 4002, "client sent 0"},
		{"not hello", `{"type":"listTabs","id":"1"}`, 4000, "expected hello"},
		{"not JSON", `hello`, 4000, "expected hello"},
	} {
		c := dialLines(t, s)
		c.line(t, tc.hello)
		if code, reason := c.closed(t); code != tc.code || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: closed %d %q, want %d %q", tc.name, code, reason, tc.code, tc.reason)
		}
	}

	for _, h := range []map[string]any{linesHello(1), hi("ignored", 1)} {
		c := dialLines(t, s)
		c.line(t, "")
		c.send(t, h)
		w := c.json(t)
		if w["type"] != "welcome" || w["protocol"] != 1.0 || w["server"].(map[string]any)["version"] != "test" {
			t.Fatalf("welcome %v", w)
		}
		if caps, ok := w["capabilities"].([]any); !ok || len(caps) != 0 {
			t.Fatalf("capabilities %v, want none", w["capabilities"])
		}
		c.send(t, map[string]any{"type": "subscribeSessions", "id": "1"})
		if r := c.json(t); r["type"] != "result" || r["id"] != "1" || r["code"] != "unsupported" {
			t.Fatalf("chat command: %v", r)
		}
		c.line(t, "  ")
		c.line(t, "[1]")
		if r := c.json(t); r["type"] != "error" || r["code"] != "invalid" {
			t.Fatalf("not an object: %v", r)
		}
		c.line(t, `{"type":"mergePR"}`+"\r")
		if r := c.json(t); r["type"] != "error" || r["message"] != "frame has no id" {
			t.Fatalf("no id: %v", r)
		}
	}
}

func mustString(v any) string { return string(mustJSON(v)) }

func TestLinesHelloTimeout(t *testing.T) {
	defer func(d time.Duration) { helloTimeout = d }(helloTimeout)
	helloTimeout = 50 * time.Millisecond
	s := &Server{Version: "test"}
	t.Cleanup(s.Close)
	if code, _ := dialLines(t, s).closed(t); code != 4003 {
		t.Fatalf("closed %d, want 4003", code)
	}
}

func TestLinesClose(t *testing.T) {
	s := &Server{Version: "test"}
	c := dialLines(t, s)
	c.send(t, linesHello(1))
	c.json(t)
	s.Close()
	if code, reason := c.closed(t); code != 1001 || reason != "daemon exiting" {
		t.Fatalf("closed %d %q", code, reason)
	}
}

// TestLinesTooBig: a line over 16 MiB is refused before it ends.
func TestLinesTooBig(t *testing.T) {
	s := &Server{Version: "test"}
	t.Cleanup(s.Close)
	c := dialLines(t, s)
	c.send(t, linesHello(1))
	c.json(t)
	go c.nc.Write([]byte(`{"type":"x","id":"1","pad":"` + strings.Repeat("x", maxMessage) + "\"}\n"))
	if code, _ := c.closed(t); code != 1009 {
		t.Fatalf("closed %d, want 1009", code)
	}
}

// TestLinesFlood: a client that sends 10,000 bad frames and reads nothing
// holds at most maxFrames replies; the server stops reading it, and once
// it reads, every reply arrives.
func TestLinesFlood(t *testing.T) {
	s := &Server{Version: "test"}
	t.Cleanup(s.Close)
	c := dialLines(t, s)
	c.send(t, linesHello(1))
	c.json(t)
	const n = 10_000
	frame := []byte(`{"type":"listTabs"` + strings.Repeat(" ", 1000) + "}\n") // 10 MB in all, past what loopback buffers
	wrote := make(chan error, 1)
	go func() {
		for range n {
			if _, err := c.nc.Write(frame); err != nil {
				wrote <- err
				return
			}
		}
		wrote <- nil
	}()
	conn := onlyConn(s)
	full := false
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		conn.mu.Lock()
		frames, size := conn.frames, conn.fbytes
		conn.mu.Unlock()
		if frames > maxFrames || size > maxFrameBytes {
			t.Fatalf("%d replies of %d bytes queued", frames, size)
		}
		full = full || frames == maxFrames
	}
	if !full {
		t.Fatal("the queue never filled")
	}
	select {
	case err := <-wrote:
		t.Fatalf("every frame was read from a client that reads nothing (%v)", err)
	default:
	}
	for i := range n {
		if r := c.json(t); r["type"] != "error" || r["message"] != "frame has no id" {
			t.Fatalf("reply %d: %v", i, r)
		}
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
}

// TestTooLarge: one result over maxResultBytes is tooLarge on the lines
// transport, and sent whole on WebSocket, where chat snapshots can be
// that big.
func TestTooLarge(t *testing.T) {
	big := ok("7", "", map[string]any{"text": strings.Repeat("x", maxResultBytes)})
	for _, lines := range []bool{true, false} {
		c := newConn(nil, nil, lines)
		c.reply(big)
		var r map[string]any
		if err := json.Unmarshal(c.q[0].frame, &r); err != nil {
			t.Fatal(err)
		}
		if tooLarge := r["code"] == "tooLarge" && r["id"] == "7" && r["ok"] == false; tooLarge != lines {
			t.Fatalf("lines %v: %.100s", lines, c.q[0].frame)
		}
	}
}

// TestLinesNotOnWebPort: the [web] TCP listener speaks HTTP only, so a
// JSON hello there gets 400 and no welcome.
func TestLinesNotOnWebPort(t *testing.T) {
	_, hs := newServer(t, fixed(goodToken))
	nc, err := net.Dial("tcp", strings.TrimPrefix(hs.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(nc, mustString(hi(goodToken, 1))+"\n")
	got, _ := io.ReadAll(nc)
	if !strings.HasPrefix(string(got), "HTTP/1.1 400") || strings.Contains(string(got), "welcome") {
		t.Fatalf("got %q, want HTTP 400", got)
	}
}

// TestLinesHalfClose: a script that sends its commands and closes its end,
// as socat does at the end of its input, still gets every result.
func TestLinesHalfClose(t *testing.T) {
	s := &Server{Version: "test"}
	t.Cleanup(s.Close)
	for range 20 {
		c := dialLines(t, s)
		c.line(t, mustString(linesHello(1))+"\n"+`{"type":"listTabs","id":"1"}`+"\n"+`{"type":"listTabs","id":"2"}`)
		c.nc.(*net.TCPConn).CloseWrite()
		c.json(t)
		for _, id := range []string{"1", "2"} {
			if r := c.json(t); r["id"] != id {
				t.Fatalf("got %v, want the result of %s", r, id)
			}
		}
		if _, err := c.br.ReadBytes('\n'); err != io.EOF {
			t.Fatalf("after the results: %v, want EOF", err)
		}
	}
}
