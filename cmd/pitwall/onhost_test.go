package main

import (
	"encoding/json"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestCutHost(t *testing.T) {
	for _, c := range []struct {
		args, want []string
		host       string
		bad        bool
	}{
		{args: []string{"ls", "--json"}, want: []string{"ls", "--json"}},
		{args: []string{"ls", "--host", "box", "--json"}, want: []string{"ls", "--json"}, host: "box"},
		{args: []string{"read", "-host=box", "box:3"}, want: []string{"read", "box:3"}, host: "box"},
		{args: []string{"send", "box:3", "--", "--host", "x"}, want: []string{"send", "box:3", "--", "--host", "x"}},
		{args: []string{"ls", "--hostname"}, want: []string{"ls", "--hostname"}},
		{args: []string{"ls", "--host"}, bad: true},
		{args: []string{"ls", "--host", "--json"}, bad: true},
		{args: []string{"ls", "--host", "--", "box"}, bad: true},
		{args: []string{"ls", "--host=-oProxyCommand=bad"}, bad: true},
		{args: []string{"ls", "-s", "--host=box", "--json"}, want: []string{"ls", "-s", "--host=box", "--json"}},
		{args: []string{"ls", "--host", "box", "-s", "--host"}, want: []string{"ls", "-s", "--host"}, host: "box"},
		{args: []string{"wait", "1", "--until", "--host=box"}, want: []string{"wait", "1", "--until", "--host=box"}},
		{args: []string{"prompt", "1", "--timeout", "--host=box"}, want: []string{"prompt", "1", "--timeout", "--host=box"}},
		{args: []string{"send", "-s", "--", "--host", "box"}, want: []string{"send", "-s", "--", "--host", "box"}},
		{args: []string{"ls", "--host", "local"}, bad: true},
		{args: []string{"ls", "--host", "a", "--host", "b"}, bad: true},
	} {
		got, host, err := cutHost(c.args)
		if c.bad != (err != nil) || !c.bad && (!slices.Equal(got, c.want) || host != c.host) {
			t.Errorf("cutHost(%q) = %q, %q, %v", c.args, got, host, err)
		}
	}
	for args, want := range map[string]bool{"ls": true, "session ls": true, "session kill": false, "new": false, "kill": true, "wait": true} {
		if hostCommand(strings.Fields(args)) != want {
			t.Errorf("hostCommand(%q) = %v", args, !want)
		}
	}
}

// Every address stays on the selected daemon, even when this pane's id
// also exists there. A second connection for prompt must stay there too.
func TestHostFlagAddresses(t *testing.T) {
	for _, command := range []string{"read", "send", "prompt", "wait", "kill"} {
		for _, addr := range []string{"1", "#1", "build", "w", "@w", "@p", "1.2", "#1.2", "box:1", "#box:1", "box:1.2", "#box:1.2", "box:@w", "#box:@w", "box:@p", "#box:@p", ""} {
			if command == "kill" && strings.Contains(addr, ".2") {
				continue // kill takes tab addresses, not pane ordinals
			}
			t.Run(command+"/"+addr, func(t *testing.T) {
				st := driveState(model.ProviderClaude, model.StateCompleted, false, 0).State
				st.Panes = append(st.Panes, model.Pane{ID: "p2", WorkspaceID: "w", Provider: model.ProviderClaude})
				st.Workspaces[0].Tabs[0].Layout = &layout.Node{Children: []*layout.Node{{Pane: "p"}, {Pane: "p2"}}}
				requests, dials, drops := fakeHostFlag(t, st, proto.Level, "")
				t.Setenv("PITWALL_PANE", "p")
				args := []string{command, "--host", "box", addr}
				switch command {
				case "send", "prompt":
					args = append(args, "--", "hello --host box")
				case "wait":
					args = append(args, "--until", "idle", "--timeout", "1s")
				}
				code, _, stderr := cliOutput(args...)
				if addr == "" {
					if code == 0 || len(requests) != 0 || dials.Load() != drops.Load() {
						t.Fatalf("empty address used caller fallback: %d %s", code, stderr)
					}
					return
				}
				if code != 0 {
					t.Fatalf("exit %d: %s", code, stderr)
				}
				pane := "p"
				if strings.HasSuffix(addr, ".2") {
					pane = "p2"
				}
				var want any
				switch command {
				case "read":
					want = proto.Read{Pane: pane}
				case "send":
					want = proto.Send{Pane: pane, Text: "hello --host box"}
				case "prompt":
					want = proto.Prompt{Pane: pane, Text: "hello --host box"}
				case "kill":
					want = proto.KillSession{WorkspaceID: "w"}
				}
				if want != nil {
					select {
					case got := <-requests:
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("request %#v, want %#v", got, want)
						}
					default:
						t.Fatal("no request")
					}
				}
				if len(requests) != 0 || dials.Load() != drops.Load() || onHost != "" {
					t.Fatalf("requests=%d dials=%d drops=%d host=%q", len(requests), dials.Load(), drops.Load(), onHost)
				}
			})
		}
	}
}

func TestHostFlagSessions(t *testing.T) {
	for _, name := range []string{"", "other", "--host", "--host=box"} {
		t.Run(name, func(t *testing.T) {
			st := hostState("s", "w", "p")
			st.Sessions[0].UsedAt = time.Unix(2, 0)
			other := hostState("s2", "w2", "p2")
			other.Sessions[0].Name = "other"
			if name != "" {
				other.Sessions[0].Name = name
			}
			other.Sessions[0].UsedAt = time.Unix(1, 0)
			st.Sessions = append(st.Sessions, other.Sessions...)
			st.Workspaces = append(st.Workspaces, other.Workspaces...)
			st.Panes = append(st.Panes, other.Panes...)
			fakeHostFlag(t, st, proto.Level, "")
			t.Setenv("PITWALL_PANE", "p2") // this local id belongs to the older session there
			args := []string{"ls", "--host", "box", "--json"}
			want := "w"
			if name != "" {
				args = append(args, "-s", name)
				want = "w2"
			}
			code, out, stderr := cliOutput(args...)
			var rows []tabJSON
			if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].ID != want {
				t.Fatalf("exit %d: %q %s; want tab %s", code, out, stderr, want)
			}
		})
	}
}

func TestHostFlagRetainedReply(t *testing.T) {
	for _, addr := range []string{"@p", "box:@p", "#box:@p"} {
		t.Run(addr, func(t *testing.T) {
			st := hostState("s", "w", "p")
			st.Closed = []model.ClosedTab{{Tab: st.Workspaces[0], Pane: st.Panes[0]}}
			st.Workspaces, st.Panes = nil, nil
			requests, _, _ := fakeHostFlag(t, st, proto.Level, "")
			if code, out, stderr := cliOutput("read", "--host", "box", addr); code != 0 || out != "reply\n" {
				t.Fatalf("exit %d: %q %s", code, out, stderr)
			}
			if got := <-requests; got != (proto.Read{Pane: "p"}) {
				t.Fatalf("request %#v", got)
			}
		})
	}
}

func TestHostFlagPrefixedTitle(t *testing.T) {
	st := driveState(model.ProviderClaude, model.StateCompleted, false, 0).State
	st.Workspaces[0].Name = "box:1"
	requests, _, _ := fakeHostFlag(t, st, proto.Level, "")
	if code, _, stderr := cliOutput("send", "--host", "box", "box:box:1", "--", "hello"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := <-requests; got != (proto.Send{Pane: "p", Text: "hello"}) {
		t.Fatalf("request %#v", got)
	}
}

func TestHostFlagFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		level   int
		refusal string
	}{
		{name: "old level", level: 0},
		{name: "daemon refusal", level: proto.Level, refusal: "host refused this client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, dials, drops := fakeHostFlag(t, hostState("s", "w", "p"), tc.level, tc.refusal)
			if code, _, stderr := cliOutput("send", "--host", "box", "1", "--", "hello"); code == 0 || stderr == "" {
				t.Fatalf("exit %d: %q", code, stderr)
			}
			if len(requests) != 0 || dials.Load() != drops.Load() || onHost != "" {
				t.Fatalf("requests=%d dials=%d drops=%d host=%q", len(requests), dials.Load(), drops.Load(), onHost)
			}
		})
	}
}

// fakeHostFlag serves only in-memory connections. Any local socket dial
// fails, and the selected daemon never needs ssh or a running process.
func fakeHostFlag(t *testing.T, st model.State, level int, refusal string) (chan any, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	t.Setenv("PITWALL_SOCKET", t.TempDir()+"/none.sock")
	requests := make(chan any, 10)
	var dials, drops atomic.Int32
	var serving sync.WaitGroup
	prev := dialHost
	t.Cleanup(func() { dialHost = prev; serving.Wait() })
	dialHost = func(name, kind string) (*cliConn, func(), error) {
		if name != "box" {
			t.Errorf("unexpected host %q", name)
		}
		dials.Add(1)
		a, b := net.Pipe()
		serving.Add(1)
		go func() {
			defer serving.Done()
			defer b.Close()
			c := proto.NewConn(b)
			if _, err := c.Recv(); err != nil { // Hello
				return
			}
			if kind == "watch" {
				c.Send(proto.StateMsg{State: st, Level: level})
			}
			for {
				msg, err := c.Recv()
				if err != nil {
					return
				}
				switch msg.(type) {
				case proto.Sync:
					if refusal != "" {
						c.Send(proto.Error{Message: refusal})
					}
					c.Send(proto.StateMsg{State: st, Level: level})
				case proto.Read:
					requests <- msg
					c.Send(proto.ReadResult{Text: "reply"})
				default:
					requests <- msg
				}
			}
		}()
		conn, err := hello(a, kind)
		return conn, func() { drops.Add(1); a.Close() }, err
	}
	return requests, &dials, &drops
}

// ls, read and session ls with --host talk to the host's daemon, never
// this machine's, and ls's rows give addresses read takes back.
func TestHostFlag(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_SOCKET", t.TempDir()+"/none.sock") // any local dial fails
	box := hostState("bs", "bt", "bp")
	box.Workspaces[0].Label = "evals"
	box.Workspaces[0].Tabs[0].Layout = &layout.Node{Pane: "bp"}
	var dials, drops atomic.Int32
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(name, kind string) (*cliConn, func(), error) {
		if name != "box" {
			t.Errorf("dial %s", name)
		}
		dials.Add(1)
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			c := proto.NewConn(b)
			for {
				msg, err := c.Recv()
				if err != nil {
					return
				}
				switch m := msg.(type) {
				case proto.Sync:
					c.Send(proto.StateMsg{State: box, Level: proto.Level})
				case proto.Read:
					c.Send(proto.ReadResult{Text: "reply from " + m.Pane})
				}
			}
		}()
		conn, err := helloCLI(a)
		return conn, func() { drops.Add(1); a.Close() }, err
	}
	run := func(args ...string) string {
		t.Helper()
		var out, errOut strings.Builder
		if code := runCLI(args, nil, &out, &errOut); code != 0 {
			t.Fatalf("%q: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}

	var rows []tabJSON
	if err := json.Unmarshal([]byte(run("ls", "--host", "box", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Host != "box" || rows[0].Addr != "box:1" || rows[0].N != 0 || rows[0].Title != "evals" {
		t.Fatalf("rows %+v", rows)
	}
	if got := run("read", rows[0].Addr, "--host", "box", "-s", "main"); got != "reply from bp\n" {
		t.Fatalf("read %q", got)
	}
	if got := run("read", "--host=box", "1"); got != "reply from bp\n" {
		t.Fatalf("read 1: %q", got)
	}
	if got := run("session", "ls", "--host", "box"); !strings.Contains(got, "main") {
		t.Fatalf("session ls %q", got)
	}
	if got := run("ls", "--host", "box"); !strings.Contains(got, "box:1") {
		t.Fatalf("ls %q", got)
	}
	if dials.Load() != 5 || drops.Load() != 5 {
		t.Fatalf("%d dials, %d forwards dropped", dials.Load(), drops.Load())
	}
	if onHost != "" {
		t.Fatalf("onHost %q after the call", onHost)
	}

	var out, errOut strings.Builder
	if code := runCLI([]string{"ls", "--host", "box", "--children"}, nil, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--children") {
		t.Fatalf("ls --children --host: %d %q", code, errOut.String())
	}
}
