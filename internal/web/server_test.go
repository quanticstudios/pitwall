package web

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const goodToken = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func newServer(t *testing.T, token func() (string, error)) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{Token: token, Version: "test"}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); hs.Close() })
	return s, hs
}

func fixed(tok string) func() (string, error) { return func() (string, error) { return tok, nil } }

// client is a WebSocket client for tests: it sets any Host and Origin.
type client struct {
	nc net.Conn
	br *bufio.Reader
}

// dial sends the upgrade with host and origin ("" for none) and returns
// the response's status, and the client when it is 101.
func dial(t *testing.T, hs *httptest.Server, host, origin string) (*client, int) {
	t.Helper()
	addr := strings.TrimPrefix(hs.URL, "http://")
	if host == "" {
		host = addr
	}
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /ws HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n"
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	if _, err := io.WriteString(nc, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp.StatusCode
	}
	// RFC 6455's own example key and accept.
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("Sec-WebSocket-Accept %q", got)
	}
	return &client{nc, br}, resp.StatusCode
}

func (c *client) frame(t *testing.T, op byte, fin bool, p []byte) {
	t.Helper()
	b := []byte{op}
	if fin {
		b[0] |= 0x80
	}
	switch {
	case len(p) < 126:
		b = append(b, 0x80|byte(len(p)))
	case len(p) <= 0xffff:
		b = binary.BigEndian.AppendUint16(append(b, 0x80|126), uint16(len(p)))
	default:
		b = binary.BigEndian.AppendUint64(append(b, 0x80|127), uint64(len(p)))
	}
	mask := []byte{1, 2, 3, 4}
	b = append(b, mask...)
	for i, x := range p {
		b = append(b, x^mask[i%4])
	}
	if _, err := c.nc.Write(b); err != nil {
		t.Fatal(err)
	}
}

func (c *client) send(t *testing.T, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	c.frame(t, opText, true, data)
}

func (c *client) recv(t *testing.T) (byte, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[1]&0x80 != 0 {
		t.Fatal("server frame is masked")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(c.br, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(c.br, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(c.br, p); err != nil {
		t.Fatal(err)
	}
	return h[0] & 0x0f, p
}

func (c *client) json(t *testing.T) map[string]any {
	t.Helper()
	op, p := c.recv(t)
	if op != opText {
		t.Fatalf("op %#x %q, want a text frame", op, p)
	}
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// closed reads a close frame and returns its code and reason.
func (c *client) closed(t *testing.T) (int, string) {
	t.Helper()
	op, p := c.recv(t)
	if op != opClose || len(p) < 2 {
		t.Fatalf("op %#x %q, want a close", op, p)
	}
	return int(binary.BigEndian.Uint16(p)), string(p[2:])
}

func hi(token string, protocol int) map[string]any {
	return map[string]any{"type": "hello", "protocol": protocol, "token": token, "client": map[string]string{"name": "aide", "version": "0.42.0"}}
}

func TestHostAndOrigin(t *testing.T) {
	_, hs := newServer(t, fixed(goodToken))
	port := hs.URL[strings.LastIndex(hs.URL, ":")+1:]
	for _, tc := range []struct {
		host, origin string
		want         int
	}{
		{"", "", 101},
		{"localhost:" + port, "", 101},
		{"", "http://127.0.0.1:" + port, 101},
		{"", "http://localhost:" + port, 101},
		{"evil.example:" + port, "", 403}, // DNS rebinding: evil.example now resolves to 127.0.0.1
		{"127.0.0.1:1", "", 403},
		{"127.0.0.1", "", 403},
		{"", "http://evil.example", 403},
		{"", "https://127.0.0.1:" + port, 403},
		{"", "http://127.0.0.1:1", 403},
		{"", "null", 403},
	} {
		if _, got := dial(t, hs, tc.host, tc.origin); got != tc.want {
			t.Errorf("Host %q Origin %q: %d, want %d", tc.host, tc.origin, got, tc.want)
		}
	}
	// No CORS: a refused answer allows no other origin either.
	req, _ := http.NewRequest("GET", hs.URL+"/ws", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin: %d %v", resp.StatusCode, resp.Header)
	}
	// A plain request from this host is not an upgrade.
	resp, err = http.Get(hs.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("plain GET: %d, want 400", resp.StatusCode)
	}
}

func TestHello(t *testing.T) {
	_, hs := newServer(t, fixed(goodToken))
	for _, tc := range []struct {
		name   string
		hello  any
		code   int
		reason string
	}{
		{"no token", hi("", 1), 4001, "bad token"},
		{"wrong token", hi(strings.Repeat("0", 64), 1), 4001, "bad token"},
		{"short token", hi(goodToken[:63], 1), 4001, "bad token"},
		{"not hello", map[string]any{"type": "listSessions", "id": "1", "token": goodToken}, 4000, "expected hello"},
		{"not JSON", "hello", 4000, "expected hello"},
		{"wrong token, newer protocol", hi("x", 2), 4001, "bad token"},
		{"newer protocol", hi(goodToken, 2), 4002, "client sent 2"},
	} {
		c, _ := dial(t, hs, "", "")
		if s, ok := tc.hello.(string); ok {
			c.frame(t, opText, true, []byte(s))
		} else {
			c.send(t, tc.hello)
		}
		if code, reason := c.closed(t); code != tc.code || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: closed %d %q, want %d %q", tc.name, code, reason, tc.code, tc.reason)
		}
	}

	c, _ := dial(t, hs, "", "")
	c.send(t, hi(goodToken, 1))
	w := c.json(t)
	if w["type"] != "welcome" || w["protocol"] != 1.0 || w["deltaVersion"] != 1.0 || w["server"].(map[string]any)["version"] != "test" {
		t.Fatalf("welcome %v", w)
	}
	// A ping gets its pong; commands are not served yet; frames the server
	// cannot tie to a command get error and keep the connection.
	c.frame(t, opPing, true, []byte("p"))
	if op, p := c.recv(t); op != opPong || string(p) != "p" {
		t.Fatalf("pong %#x %q", op, p)
	}
	c.send(t, map[string]any{"type": "listSessions", "id": "2"})
	if r := c.json(t); r["type"] != "result" || r["id"] != "2" || r["ok"] != false || r["code"] != "unsupported" {
		t.Fatalf("result %v", r)
	}
	c.frame(t, opText, true, []byte("[1]"))
	if r := c.json(t); r["type"] != "error" || r["code"] != "invalid" {
		t.Fatalf("not an object: %v", r)
	}
	c.send(t, map[string]any{"type": "listSessions"})
	if r := c.json(t); r["type"] != "error" {
		t.Fatalf("no id: %v", r)
	}
	// A message in fragments, with a ping between them.
	c.frame(t, opText, false, []byte(`{"type":"read",`))
	c.frame(t, opPing, true, nil)
	if op, _ := c.recv(t); op != opPong {
		t.Fatalf("op %#x, want pong", op)
	}
	c.frame(t, opContinue, true, []byte(`"id":"3"}`))
	if r := c.json(t); r["id"] != "3" {
		t.Fatalf("fragmented: %v", r)
	}
	// A frame over 16 MiB is refused from its header.
	c.nc.Write([]byte{0x81, 0x80 | 127, 0, 0, 0, 0, 1, 0, 0, 1})
	if code, _ := c.closed(t); code != 1009 {
		t.Fatalf("big frame: %d, want 1009", code)
	}
}

func TestHelloTimeout(t *testing.T) {
	defer func(d time.Duration) { helloTimeout = d }(helloTimeout)
	helloTimeout = 50 * time.Millisecond
	_, hs := newServer(t, fixed(goodToken))
	c, _ := dial(t, hs, "", "")
	if code, _ := c.closed(t); code != 4003 {
		t.Fatalf("closed %d, want 4003", code)
	}
}

func TestClose(t *testing.T) {
	s, hs := newServer(t, fixed(goodToken))
	c, _ := dial(t, hs, "", "")
	c.send(t, hi(goodToken, 1))
	c.json(t)
	s.Close()
	if code, reason := c.closed(t); code != 1001 || reason != "daemon exiting" {
		t.Fatalf("closed %d %q", code, reason)
	}
}

// The token file: 0600, the same token across restarts, a new one on
// rotate, which the server takes at the next hello.
func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "web.json")
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ip := ln.Addr().(*net.TCPAddr).IP; !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v, want 127.0.0.1 only", ip)
	}

	f, err := Publish(path, ln.Addr(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenRe.MatchString(f.Token) || f.PID != 42 || f.URL != "ws://"+ln.Addr().String()+"/ws" {
		t.Fatalf("published %+v", f)
	}
	if runtime.GOOS != "windows" { // mode bits mean nothing there; see the package doc
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v, want 0600", fi.Mode(), err)
		}
	}
	again, err := Publish(path, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, 43)
	if err != nil || again.Token != f.Token || again.PID != 43 || again.URL != "ws://127.0.0.1:9/ws" {
		t.Fatalf("restart: %+v %v, want token %s", again, err, f.Token)
	}

	_, hs := newServer(t, func() (string, error) { f, err := Read(path); return f.Token, err })
	rot, err := Rotate(path)
	if err != nil || rot.Token == f.Token || !tokenRe.MatchString(rot.Token) || rot.URL != again.URL {
		t.Fatalf("rotate: %+v %v", rot, err)
	}
	c, _ := dial(t, hs, "", "")
	c.send(t, hi(f.Token, 1))
	if code, _ := c.closed(t); code != 4001 {
		t.Fatalf("old token: %d, want 4001", code)
	}
	c, _ = dial(t, hs, "", "")
	c.send(t, hi(rot.Token, 1))
	if w := c.json(t); w["type"] != "welcome" {
		t.Fatalf("new token: %v", w)
	}

	// No file at all refuses every token, the empty one too.
	os.Remove(path)
	c, _ = dial(t, hs, "", "")
	c.send(t, hi("", 1))
	if code, _ := c.closed(t); code != 4001 {
		t.Fatalf("no file: %d, want 4001", code)
	}
	if f, err := Publish(path, ln.Addr(), 44); err != nil || f.Token == rot.Token {
		t.Fatalf("after delete: %+v %v, want a new token", f, err)
	}
}
