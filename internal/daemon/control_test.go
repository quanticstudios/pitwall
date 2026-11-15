//go:build linux

package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/web"
)

// controlCommands are the control commands the socket serves.
var controlCommands = []string{"listTabs", "subscribeTabs", "unsubscribeTabs", "readPane", "scrollback", "waitPane"}

// controlDaemon serves session work with tabs build (#1, pane p1) and test
// (#2, pane p2), each running a command, and a worker on box that build
// started.
func controlDaemon(t *testing.T, edit func(*Options)) (*Daemon, *fakes, string) {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	tab := func(id, name, pane string) model.Workspace {
		return model.Workspace{ID: id, SessionID: "s", Name: name, NameSet: true, Tabs: []model.Tab{{ID: "t" + id, Layout: &layout.Node{Pane: pane}}}, ActiveTab: "t" + id}
	}
	dir := t.TempDir()
	f.saved = model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work", Order: []string{"w1", "w2"}, Workers: []model.RemoteWorker{{ID: "r9", Host: "box", Parent: "w1"}}}},
		Workspaces: []model.Workspace{tab("w1", "build", "p1"), tab("w2", "test", "p2")},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "w1", Cwd: dir, Cmd: []string{"make"}},
			{ID: "p2", WorkspaceID: "w2", Cwd: dir, Cmd: []string{"make", "test"}},
		},
	}
	d, sock, stop := runDaemon(t, f, edit)
	t.Cleanup(stop)
	for start := time.Now(); fakeNamed(f, "p2") == nil; time.Sleep(5 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("the saved panes did not start")
		}
	}
	return d, f, sock
}

// fakeNamed is the started fake of pane id, nil before it starts.
func fakeNamed(f *fakes, id string) *fakePane {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.panes {
		if p.cfg.ID == id {
			return p
		}
	}
	return nil
}

// watching waits until n control commands watch d.
func watching(t *testing.T, d *Daemon, n int) {
	t.Helper()
	for start := time.Now(); ; time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		got := len(d.ctlWatch)
		d.mu.Unlock()
		if got == n {
			return
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("%d control watches, want %d", got, n)
		}
	}
}

// exit ends pane id's process with code, as the program does on its own.
func exit(f *fakes, id string, code int) {
	p := fakeNamed(f, id)
	p.code = code
	p.Close()
}

// TestControlOnDaemon: listTabs, readPane and waitPane on a daemon,
// whose pane exits reach the waits with the programs' own codes.
func TestControlOnDaemon(t *testing.T) {
	d, f, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	caps := c.hello(t)["capabilities"].([]any)
	for _, typ := range controlCommands {
		if !slices.Contains(caps, any(typ)) {
			t.Fatalf("capabilities %v lack %s", caps, typ)
		}
	}
	c.line(t, `{"type":"listTabs","id":"l"}`)
	r := c.next(t)
	var got []string
	for _, row := range r["data"].(map[string]any)["tabs"].([]any) {
		m := row.(map[string]any)
		got = append(got, fmt.Sprint(m["addr"], " ", m["title"], " ", m["stale"], " ", m["host"]))
	}
	if want := "#1 build false |#2 test false |@r9  true box"; strings.Join(got, "|") != want {
		t.Fatalf("listTabs %q, want %q (%v)", strings.Join(got, "|"), want, r)
	}
	p1 := fakeNamed(f, "p1")
	p1.mu.Lock()
	p1.screen = []string{"make: done"}
	p1.mu.Unlock()
	c.line(t, `{"type":"readPane","id":"r","pane":"#1","screen":true}`)
	if r := c.next(t); r["data"].(map[string]any)["text"] != "make: done" {
		t.Fatalf("readPane %v", r)
	}

	c.line(t, `{"type":"waitPane","id":"0","pane":"#1","until":"exit"}`)
	c.line(t, `{"type":"waitPane","id":"3","pane":"@p2","until":"exit"}`)
	watching(t, d, 2)
	exit(f, "p1", 0)
	exit(f, "p2", 3)
	for range 2 {
		r := c.next(t)
		data, _ := r["data"].(map[string]any)
		e, _ := data["exit"].(map[string]any)
		if data["outcome"] != "exited" || e == nil || fmt.Sprint(e["code"]) != r["id"] || e["signal"] != false {
			t.Fatalf("waitPane %v", r)
		}
	}
	watching(t, d, 0)
}

// dialWebToken is dialWeb with the token hello carries; it returns the
// welcome too.
func dialWebToken(t *testing.T, addr, token string) (*wsClient, map[string]any) {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	io.WriteString(nc, "GET /ws HTTP/1.1\r\nHost: "+addr+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	c := &wsClient{t: t, nc: nc, results: map[string]map[string]any{}, changed: make(chan struct{}, 1)}
	c.send(map[string]any{"type": "hello", "protocol": web.Protocol, "token": token, "client": map[string]string{"name": "script", "version": "1"}})
	go c.read(br)
	return c, c.wait(map[string]any{"type": "welcome"})
}

// noControl checks that a connection that said a valid hello was offered
// no control command and gets unsupported for each.
func noControl(t *testing.T, listener string, c *wsClient, welcome map[string]any) {
	t.Helper()
	for _, typ := range controlCommands {
		if slices.Contains(welcome["capabilities"].([]any), any(typ)) {
			t.Errorf("%s: welcome lists %s", listener, typ)
		}
		if r := c.call(map[string]any{"type": typ, "pane": "@p1", "until": "exit"}); r["code"] != "unsupported" {
			t.Errorf("%s: %s answered %v", listener, typ, r)
		}
	}
}

// TestNoControlOnListeners: the [web] listener and the phone's /ws take
// a valid hello and serve none of the control commands.
func TestNoControlOnListeners(t *testing.T) {
	webPath := filepath.Join(t.TempDir(), "web.json")
	remoteDir := t.TempDir()
	d, _, _ := controlDaemon(t, func(o *Options) { o.WebPath, o.RemoteDir = webPath, remoteDir })

	stop := d.serveWeb(config.WebSettings{Enabled: true})
	if stop == nil {
		t.Fatal("serveWeb failed")
	}
	defer stop()
	wf, err := web.Read(webPath)
	if err != nil {
		t.Fatal(err)
	}
	c, welcome := dialWebToken(t, strings.TrimSuffix(strings.TrimPrefix(wf.URL, "ws://"), "/ws"), wf.Token)
	noControl(t, "[web]", c, welcome)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	bundle := t.TempDir()
	os.WriteFile(filepath.Join(bundle, "index.html"), []byte("fixture"), 0o644)
	stopRemote := d.serveRemote(phone{RemoteSettings: config.RemoteSettings{Listen: addr}, Bundle: bundle})
	if stopRemote == nil {
		t.Fatal("serveRemote failed")
	}
	defer stopRemote()
	do := func(method, path, auth, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if strings.HasPrefix(auth, "Bearer ") {
			req.Header.Set("Authorization", auth)
		} else if auth != "" {
			req.Header.Set("Cookie", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	code, err := remote.Pair(remoteDir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var paired struct{ Token string }
	json.NewDecoder(do("POST", "/api/pair", "", `{"code":"`+code+`"}`).Body).Decode(&paired)
	cks := do("POST", "/api/web", "Bearer "+paired.Token, "").Cookies()
	if paired.Token == "" || len(cks) != 1 {
		t.Fatalf("pair %q, web cookies %v", paired.Token, cks)
	}
	var boot struct{ Token string }
	json.NewDecoder(do("GET", "/_pitwall/bootstrap", cks[0].Name+"="+cks[0].Value, "").Body).Decode(&boot)
	if boot.Token == "" {
		t.Fatal("no bootstrap token")
	}
	c, welcome = dialWebToken(t, addr, boot.Token)
	if welcome == nil {
		t.Fatal("the phone's /ws refused a valid hello")
	}
	noControl(t, "[remote]", c, welcome)
}

// TestControlExamples runs docs/examples/control.py and control.sh
// against a daemon: list, read, and a wait that ends with the program's
// exit code. Without python3, socat or jq the client is skipped, except
// in CI, which installs them.
func TestControlExamples(t *testing.T) {
	for _, ex := range []struct {
		name string
		argv []string
		need []string
	}{
		{"control.py", []string{"python3", "../../docs/examples/control.py"}, []string{"python3"}},
		{"control.sh", []string{"bash", "../../docs/examples/control.sh"}, []string{"bash", "socat", "jq"}},
	} {
		t.Run(ex.name, func(t *testing.T) {
			d, f, sock := controlDaemon(t, func(*Options) {})
			p1 := fakeNamed(f, "p1")
			p1.mu.Lock()
			p1.screen = []string{"make: done"}
			p1.mu.Unlock()
			for _, tool := range ex.need {
				if _, err := exec.LookPath(tool); err != nil {
					if os.Getenv("CI") != "" {
						t.Fatalf("%s needs %s, which CI installs", ex.name, tool)
					}
					t.Skipf("no %s", tool)
				}
			}
			run := func(args ...string) *exec.Cmd {
				cmd := exec.Command(ex.argv[0], append(ex.argv[1:], args...)...)
				cmd.Env = append(os.Environ(), "PITWALL_SOCKET="+sock)
				return cmd
			}
			out, err := run().CombinedOutput()
			if err != nil || string(out) != "#1\trunning\tbuild\n#2\trunning\ttest\n@r9\tunknown\t\n" {
				t.Fatalf("list: %v\n%s", err, out)
			}
			out, err = run("read", "#1").CombinedOutput()
			if err != nil || string(out) != "make: done\n" {
				t.Fatalf("read: %v\n%q", err, out)
			}
			out, err = run("read", "#9").CombinedOutput()
			if err == nil || !strings.Contains(string(out), "not-found") {
				t.Fatalf("read #9: %v\n%s", err, out)
			}
			wait := run("wait", "@p2", "exit", "20000")
			var buf strings.Builder
			wait.Stdout, wait.Stderr = &buf, &buf
			if err := wait.Start(); err != nil {
				t.Fatal(err)
			}
			watching(t, d, 1)
			exit(f, "p2", 3)
			err = wait.Wait()
			var got struct {
				Outcome string
				Exit    struct {
					Code            int
					Unknown, Signal bool
				}
			}
			if json.Unmarshal([]byte(buf.String()), &got); err != nil || got.Outcome != "exited" || got.Exit.Code != 3 || got.Exit.Unknown || got.Exit.Signal {
				t.Fatalf("wait: %v\n%s", err, buf.String())
			}
		})
	}
}
