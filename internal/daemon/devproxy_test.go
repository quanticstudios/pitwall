package daemon

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func devTab() model.Workspace {
	return model.Workspace{ID: "w", Branch: "fix/login", WorktreeRoot: "/srv/web-app", Ports: model.PortBlock{First: 3010, Last: 3019}}
}

func TestDevRouting(t *testing.T) {
	d := &Daemon{st: model.State{Workspaces: []model.Workspace{devTab()}, Listening: map[string][]int{"w": {3012, 3015}}}}
	h := d.devHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.Context().Value(devPortKey{})) }))
	for _, tc := range []struct {
		host   string
		status int
		body   string
	}{
		{"fix-login.web-app.localhost:7778", 200, "3012"},
		{"FIX-LOGIN.WEB-APP.LOCALHOST:7778", 200, "3012"},
		{"0.fix-login.web-app.localhost", 200, "3010"},
		{"9.fix-login.web-app.localhost", 200, "3019"},
		{"10.fix-login.web-app.localhost", 404, "fix-login.web-app.localhost"},
		{"01.fix-login.web-app.localhost", 404, "Known names"},
		{"missing.repo.localhost", 404, "fix-login.web-app.localhost"},
		{"unknown.localhost", 404, "fix-login.web-app.localhost"},
		{"example.com", 403, ".localhost Host"},
		{"127.0.0.1:7778", 403, ".localhost Host"},
		{"localhost:7778", 403, ".localhost Host"},
		{"fix-login.web-app.localhost.example.com", 403, ".localhost Host"},
		{"fix-login..localhost", 403, ".localhost Host"},
		{"fix-login.web-app.localhost:bad:port", 403, ".localhost Host"},
	} {
		r := httptest.NewRequest("GET", "http://proxy/", nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s: %d %q", tc.host, w.Code, w.Body.String())
		}
	}
	check := func(want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://fix-login.web-app.localhost/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("got %d, want %d", w.Code, want)
		}
	}
	d.st.Listening = nil
	check(503)
	d.st.Workspaces = append(d.st.Workspaces, devTab())
	check(409)
	d.st.Workspaces = d.st.Workspaces[:1]
	d.st.Listening = map[string][]int{"w": {3012}}
	d.o.DevProxy.Port = 3012
	check(502)
}

func TestDevProxyWebSocket(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "fix-login.web-app.localhost" || r.URL.Path != "/hmr" || r.URL.RawQuery != "v=1" || r.Header.Get("Upgrade") != "websocket" {
			t.Error("upgrade path or headers lost")
			http.Error(w, "bad upgrade", 400)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n")
		rw.Flush()
		frame := make([]byte, 8)
		if _, err := io.ReadFull(rw, frame); err != nil {
			t.Error(err)
			return
		}
		if !bytes.Equal(frame, []byte{0x81, 0x82, 1, 2, 3, 4, 'o' ^ 1, 'k' ^ 2}) {
			t.Errorf("frame %x", frame)
			return
		}
		rw.Write([]byte{0x81, 2, 'o', 'k'})
		rw.Flush()
		io.Copy(io.Discard, rw)
	}))
	defer backend.Close()
	_, bp, _ := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	port, _ := strconv.Atoi(bp)
	ln, proxyPort := listen(t)
	ln.Close()
	tab := devTab()
	tab.Ports = model.PortBlock{First: port, Last: port}
	d := &Daemon{o: Options{DevProxy: config.DevProxySettings{Enabled: true, Port: proxyPort}}, st: model.State{Workspaces: []model.Workspace{tab}, Listening: map[string][]int{"w": {port}}}}
	stop := d.serveDevProxy()
	if stop == nil {
		t.Fatal("proxy did not bind")
	}
	defer stop()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "GET /hmr?v=1 HTTP/1.1\r\nHost: fix-login.web-app.localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	r := bufio.NewReader(c)
	res, err := http.ReadResponse(r, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 101 || res.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("upgrade: %v", res)
	}
	c.Write([]byte{0x81, 0x82, 1, 2, 3, 4, 'o' ^ 1, 'k' ^ 2})
	frame := make([]byte, 4)
	if _, err := io.ReadFull(r, frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, []byte{0x81, 2, 'o', 'k'}) {
		t.Fatalf("reply %x", frame)
	}
	stop()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("upgrade survived proxy shutdown")
	}
}

func TestDevProxyIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable")
	}
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", r.Host, r.URL.RequestURI())
	}))
	backend.Listener.Close()
	backend.Listener = ln
	backend.Start()
	defer backend.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	reserved, proxyPort := listen(t)
	reserved.Close()
	tab := devTab()
	tab.Ports = model.PortBlock{First: port, Last: port}
	d := &Daemon{o: Options{DevProxy: config.DevProxySettings{Enabled: true, Port: proxyPort}}, st: model.State{Workspaces: []model.Workspace{tab}, Listening: map[string][]int{"w": {port}}}}
	stop := d.serveDevProxy()
	if stop == nil {
		t.Fatal("proxy did not bind")
	}
	defer stop()
	r, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/path?q=1", proxyPort), nil)
	r.Host = tab.DevHost()
	client := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 || string(body) != tab.DevHost()+" /path?q=1" {
		t.Fatalf("HTTP %d: %s %v", res.StatusCode, body, err)
	}
}

func TestDevProxyBindFailure(t *testing.T) {
	_, port := listen(t)
	d := &Daemon{o: Options{DevProxy: config.DevProxySettings{Enabled: true, Port: port}}}
	if stop := d.serveDevProxy(); stop != nil {
		stop()
		t.Fatal("bound an occupied port")
	}
	d.o.DevProxy.Enabled = false
	if stop := d.serveDevProxy(); stop != nil {
		stop()
		t.Fatal("disabled proxy started")
	}
}

func TestWorktreeURLEnvironment(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.DevProxy = config.DevProxySettings{Enabled: true, Port: 7778}
	d := newDaemon(t, o)
	tab := devTab()
	d.st.Workspaces = []model.Workspace{tab}
	if err := d.handle(t.Context(), proto.OpenPane{WorkspaceID: tab.ID}); err != nil {
		t.Fatal(err)
	}
	want := "PITWALL_URL=http://fix-login.web-app.localhost:7778"
	if !slices.Contains(f.pane(0).cfg.Env, want) {
		t.Fatalf("missing %s", want)
	}
	d.o.DevProxy.Enabled = false
	if slices.Contains(d.worktreeEnv(tab), want) {
		t.Fatal("disabled proxy supplies PITWALL_URL")
	}
}
