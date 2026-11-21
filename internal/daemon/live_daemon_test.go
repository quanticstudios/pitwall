package daemon

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// waitLong is waitFor with a timeout real agents need.
func (c *testClient) waitLong(what string, d time.Duration, ok func(any) bool) any {
	c.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case m, open := <-c.in:
			if !open {
				c.t.Fatalf("connection closed waiting for %s", what)
			}
			if ok(m) {
				return m
			}
		case <-timeout:
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestLiveDaemon drives a running daemon, at PITWALL_LIVE_SOCKET, whose
// config turns [chat] suggest_next on: a real chat per agent in
// PITWALL_LIVE_AGENTS (claude,codex,antigravity) runs one turn, then
// ChatSuggest asks for its next prompt; a pane running
// PITWALL_LIVE_SERVER (a command that listens) shows in State.Servers,
// and ServerStop stops it.
func TestLiveDaemon(t *testing.T) {
	sock := os.Getenv("PITWALL_LIVE_SOCKET")
	if sock == "" {
		t.Skip("PITWALL_LIVE_SOCKET is not set")
	}
	dir := t.TempDir()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: dir})
	for _, p := range strings.Split(os.Getenv("PITWALL_LIVE_AGENTS"), ",") {
		if p == "" {
			continue
		}
		t.Run(p, func(t *testing.T) {
			gui.t = t
			gui.send(proto.ChatNew{Provider: model.Provider(p), Dir: dir, Prompt: "Reply with exactly the word ready. Do nothing else.", Effort: map[string]string{"claude": "low"}[p]})
			r := gui.waitLong("ChatNew's reply", time.Minute, func(m any) bool { _, ok := m.(proto.ChatResult); return ok }).(proto.ChatResult)
			if r.Code != "" {
				t.Fatalf("new %s chat: %+v", p, r)
			}
			t.Logf("%s chat %s turn %s", p, r.Chat, r.Turn)
			gui.waitLong("the turn to end", 3*time.Minute, func(m any) bool {
				s, ok := m.(proto.StateMsg)
				if !ok {
					return false
				}
				i := slices.IndexFunc(s.State.Chats, func(c model.Chat) bool { return c.ID == r.Chat })
				return i >= 0 && s.State.Chats[i].Turns >= 1 && s.State.Chats[i].State == session.StateReady
			})
			start := time.Now()
			gui.send(proto.ChatSuggest{Chat: r.Chat, Turn: r.Turn})
			s := gui.waitLong("ChatSuggest's reply", 2*time.Minute, func(m any) bool { _, ok := m.(proto.ChatSuggestResult); return ok }).(proto.ChatSuggestResult)
			t.Logf("%s suggests %q (code %q, err %q) in %s", p, s.Text, s.Code, s.Err, time.Since(start).Round(100*time.Millisecond))
			if s.Err != "" || s.Chat != r.Chat {
				t.Fatalf("suggest: %+v", s)
			}
			// Asked again for that turn: the same answer, no second run.
			start = time.Now()
			gui.send(proto.ChatSuggest{Chat: r.Chat, Turn: r.Turn})
			again := gui.waitLong("ChatSuggest's second reply", time.Minute, func(m any) bool { _, ok := m.(proto.ChatSuggestResult); return ok }).(proto.ChatSuggestResult)
			if again.Text != s.Text || time.Since(start) > 2*time.Second {
				t.Fatalf("second ask: %+v after %s", again, time.Since(start))
			}
			gui.send(proto.ChatArchive{Chat: r.Chat})
			gui.waitLong("ChatArchive's reply", time.Minute, func(m any) bool { a, ok := m.(proto.ChatResult); return ok && a.Chat == r.Chat })
		})
	}
	cmd := strings.Fields(os.Getenv("PITWALL_LIVE_SERVER"))
	t.Run("servers", func(t *testing.T) {
		if len(cmd) == 0 {
			t.Skip("PITWALL_LIVE_SERVER is not set")
		}
		gui.t = t
		gui.send(proto.NewSession{Name: "server-" + time.Now().Format("150405.000"), Cwd: dir, Cmd: cmd, Loose: true})
		var srv model.Server
		var ws string
		gui.waitLong("the server in State.Servers", time.Minute, func(m any) bool {
			s, ok := m.(proto.StateMsg)
			if !ok {
				return false
			}
			for id, ss := range s.State.Servers {
				for _, sv := range ss {
					if sv.Name != "" && len(sv.Ports) > 0 {
						srv, ws = sv, id
						return true
					}
				}
			}
			return false
		})
		t.Logf("tab %s: %+v", ws, srv)
		gui.send(proto.ServerStop{Workspace: ws, PID: srv.PID, Port: srv.Ports[0]})
		// The state without it can come before the reply: wait for both.
		var r *proto.ServerStopResult
		gone := false
		gui.waitLong("ServerStop's reply and the server leaving State.Servers", 30*time.Second, func(m any) bool {
			switch m := m.(type) {
			case proto.ServerStopResult:
				r = &m
			case proto.StateMsg:
				gone = !slices.ContainsFunc(m.State.Servers[ws], func(sv model.Server) bool { return sv.PID == srv.PID })
			}
			return r != nil && gone
		})
		t.Logf("stop: %+v", *r)
		if r.Err != "" || r.Running {
			t.Fatalf("stop: %+v", *r)
		}
	})
	stubborn := strings.Fields(os.Getenv("PITWALL_LIVE_STUBBORN"))
	t.Run("stubborn", func(t *testing.T) {
		if len(stubborn) == 0 {
			t.Skip("PITWALL_LIVE_STUBBORN is not set")
		}
		gui.t = t
		gui.send(proto.NewSession{Name: "stubborn-" + time.Now().Format("150405.000"), Cwd: dir, Cmd: stubborn, Loose: true})
		var srv model.Server
		var ws string
		gui.waitLong("the server in State.Servers", time.Minute, func(m any) bool {
			s, ok := m.(proto.StateMsg)
			if !ok {
				return false
			}
			for id, ss := range s.State.Servers {
				for _, sv := range ss {
					if sv.Name == "python3" && len(sv.Ports) > 0 {
						srv, ws = sv, id
						return true
					}
				}
			}
			return false
		})
		gui.send(proto.ServerStop{Workspace: ws, PID: srv.PID, Port: srv.Ports[0]})
		r := gui.waitLong("ServerStop's reply", 30*time.Second, func(m any) bool { _, ok := m.(proto.ServerStopResult); return ok }).(proto.ServerStopResult)
		t.Logf("SIGTERM: %+v", r)
		if !r.Running || r.Err != "" {
			t.Fatalf("a process ignoring SIGTERM: %+v", r)
		}
		gui.send(proto.ServerStop{Workspace: ws, PID: srv.PID, Port: srv.Ports[0], Force: true})
		r = gui.waitLong("the forced reply", 30*time.Second, func(m any) bool { _, ok := m.(proto.ServerStopResult); return ok }).(proto.ServerStopResult)
		t.Logf("SIGKILL: %+v", r)
		if r.Running || r.Err != "" {
			t.Fatalf("force: %+v", r)
		}
	})
}
