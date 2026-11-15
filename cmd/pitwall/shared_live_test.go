//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// TestSharedSessionLive runs a shared session on two real daemons: this
// machine's, and box's, which the window reaches through pitwall's own
// ssh code and the fake ssh of the remote tests. It opens tabs on both
// hosts in one session, types into each, takes box down and checks the
// down state, closes a box tab meanwhile, then brings box back and checks
// that its tabs return, the closed one closes there, and typing works.
func TestSharedSessionLive(t *testing.T) {
	dir := fakeHost(t)
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	setHome(t, home)
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_ATTACH", "")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", filepath.Join(dir, "bin")+":/usr/bin:/bin") // no real agent
	sockR, sockL := sockPath(t, "box.sock"), sockPath(t, "here.sock")
	runR, runL := filepath.Dir(sockR), filepath.Dir(sockL)
	envOf := func(name, run string) {
		t.Setenv("XDG_STATE_HOME", filepath.Join(dir, name, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, name, "config"))
		t.Setenv("XDG_RUNTIME_DIR", run)
	}
	serve := func(sock string) (stop func()) {
		d, err := daemon.New()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- d.Serve(ctx, ln) }()
		return func() {
			cancel()
			if err := <-served; err != nil {
				t.Error(err)
			}
		}
	}
	envOf("box", runR)
	stopBox := serve(sockR)
	boxUp := true
	t.Cleanup(func() {
		if boxUp {
			stopBox()
		}
	})
	envOf("here", runL)
	t.Cleanup(serve(sockL))
	// remote-start, run on "box" through the fake ssh, finds box's daemon.
	t.Setenv("PITWALL_SOCKET", sockR)
	t.Cleanup(func() { killRelay(dir) })

	nc, err := net.Dial("unix", sockL)
	if err != nil {
		t.Fatal(err)
	}
	conn, initial, err := guiHandshake(nc, home, "")
	if err != nil {
		t.Fatal(err)
	}
	ls := initial.State.Sessions[0]
	local := newBackend(conn, ls.ID)
	local.state, local.link.Level = initial.State, initial.Level
	go local.recvLoop()
	go local.sendLoop()
	r, err := newSSHHost("box", []config.Host{{Name: "box", SSH: "me@box", Window: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.close)
	prev := pingEvery
	pingEvery = 50 * time.Millisecond
	t.Cleanup(func() { pingEvery = prev })
	// why: the window outlives the test. Its reconnects after box's daemon
	// stops would start a daemon there that outlives the test too, and
	// read globals other tests set.
	var over atomic.Bool
	t.Cleanup(func() { over.Store(true) })
	dial := func(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
		if over.Load() {
			return nil, proto.StateMsg{}, errors.New("the test is over")
		}
		return r.dial(session, restart)
	}
	m := newMulti(local, []hostDial{{name: "box", ssh: "me@box", dial: dial}})
	m.Send(proto.SessionShow{SessionID: ls.ID})
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })

	boxTabs := func() []model.Workspace {
		st := m.State()
		return slices.DeleteFunc(slices.Clone(st.Workspaces), func(w model.Workspace) bool {
			return w.SessionID != ls.ID || m.HostOf(w.ID) != "box"
		})
	}
	// The first box tab makes box's part of the session, named after it.
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: ls.ID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a box tab in the session", func() bool { return len(boxTabs()) == 1 })
	waitFor(t, "box's part recorded here", func() bool {
		st := local.State()
		s := st.Session(ls.ID)
		return s != nil && s.Remote("box") != nil
	})
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: ls.ID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two box tabs", func() bool { return len(boxTabs()) == 2 })
	rw1, rw2 := boxTabs()[0].ID, boxTabs()[1].ID
	paneOf := func(tab string) string {
		st := m.State()
		for _, p := range st.Panes {
			if p.WorkspaceID == tab {
				return p.ID
			}
		}
		return ""
	}
	st := m.State()
	lw := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.SessionID == ls.ID && m.HostOf(w.ID) == "" })
	if lw < 0 {
		t.Fatal("no local tab in the session")
	}
	lp, rp := paneOf(st.Workspaces[lw].ID), paneOf(rw1)
	if m.TabNumber(rw1) != "box:1" && m.TabNumber(rw1) != "box:2" {
		t.Fatalf("box tab number %q", m.TabNumber(rw1))
	}

	screen := func(sock, pane string) string {
		nc, err := net.Dial("unix", sock)
		if err != nil {
			return ""
		}
		c, err := helloCLI(nc)
		if err != nil {
			return ""
		}
		defer c.Close()
		text, _ := readPane(c, proto.Read{Pane: pane, Screen: true})
		return text
	}
	typed := func(sock, pane, cmd, want string) {
		t.Helper()
		if err := m.Send(proto.Input{Pane: pane, Data: []byte(cmd + "\r")}); err != nil {
			t.Fatalf("typing into %s: %v", pane, err)
		}
		waitFor(t, want+" on "+pane, func() bool { return strings.Contains(screen(sock, pane), want) })
	}
	typed(sockL, lp, "echo here-$((6*7))", "here-42")
	typed(sockR, rp, "echo box-$((6*7))", "box-42")
	waitFor(t, "box's latency", func() bool { return m.Hosts()[1].Latency > 0 })

	// box goes down: unreachable, and its daemon gone.
	if err := os.WriteFile(filepath.Join(dir, "down"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stopBox()
	boxUp = false
	waitFor(t, "box down", func() bool { h := m.Hosts()[1]; return !h.Up && strings.Contains(h.Note, "timed out") })
	if h := m.Hosts()[1]; h.Since.IsZero() {
		t.Fatalf("down host %+v has no time it went down", h)
	}
	if len(boxTabs()) != 2 {
		t.Fatalf("box's tabs left the session: %v", boxTabs())
	}
	if err := m.Send(proto.Input{Pane: rp, Data: []byte("x")}); err == nil {
		t.Fatal("typing into a down host's pane was accepted")
	}
	typed(sockL, lp, "echo still-$((1+1))", "still-2") // this machine's tabs are never affected
	// Closing a box tab while box is down forgets it here.
	if err := m.Send(proto.CloseTab{WorkspaceID: rw2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the closed tab forgotten", func() bool {
		st := local.State()
		r := st.Session(ls.ID).Remote("box")
		return r != nil && slices.Equal(r.Forget, []string{rw2}) && len(boxTabs()) == 1
	})

	// box comes back.
	envOf("box", runR)
	stopBox = serve(sockR)
	boxUp = true
	envOf("here", runL)
	os.Remove(filepath.Join(dir, "down"))
	m.Reconnect()
	waitFor(t, "box up again", func() bool { return m.Hosts()[1].Up })
	defer func() {
		if t.Failed() {
			st := local.State()
			bs := m.hosts[1].b.State()
			t.Logf("remote %+v; box tabs %v; box sessions %+v; box workspaces %d; rw1 %s rw2 %s", st.Session(ls.ID).Remotes, boxTabs(), bs.Sessions, len(bs.Workspaces), rw1, rw2)
		}
	}()
	waitFor(t, "the forgotten tab closed on box", func() bool {
		st := local.State()
		r := st.Session(ls.ID).Remote("box")
		return r != nil && len(r.Forget) == 0 && len(boxTabs()) == 1 && boxTabs()[0].ID == rw1
	})
	waitFor(t, "a pane in box's tab", func() bool { return paneOf(rw1) != "" })
	typed(sockR, paneOf(rw1), "echo back-$((2+3))", "back-5")

	// pitwall new --host box, run here, opens a tab in box's part.
	prevDial := dialHost
	t.Cleanup(func() { dialHost = prevDial })
	var boxDown atomic.Bool // ssh to box fails while it is set
	dialHost = func(_, kind string) (*cliConn, func(), error) {
		if boxDown.Load() {
			return nil, nil, errors.New("ssh: connect to host box port 22: Connection refused")
		}
		nc, err := net.Dial("unix", sockR)
		if err != nil {
			return nil, nil, err
		}
		c, err := hello(nc, kind)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { c.Close() }, nil
	}
	t.Setenv("PITWALL_SOCKET", sockL)
	code, out, errOut := cliOutput("new", "-s", ls.Name, "--host", "box", "--", "sh", "-c", "echo cli-$((3*3)); sleep 60")
	if code != 0 || !strings.HasPrefix(out, "#box:") {
		t.Fatalf("pitwall new --host box: %d %q %q", code, out, errOut)
	}
	waitFor(t, "the CLI's tab in the session", func() bool { return len(boxTabs()) == 2 })
	cli := slices.DeleteFunc(boxTabs(), func(w model.Workspace) bool { return w.ID == rw1 })[0]
	waitFor(t, "its command's output", func() bool { return strings.Contains(screen(sockR, paneOf(cli.ID)), "cli-9") })

	// With -d the tab opens detached there, as a local new -d does.
	if code, out, errOut := cliOutput("new", "-s", ls.Name, "-d", "--host", "box", "--", "sleep", "60"); code != 0 || !strings.HasPrefix(out, "#box:") {
		t.Fatalf("pitwall new -d --host box: %d %q %q", code, out, errOut)
	}
	waitFor(t, "the detached tab on box", func() bool {
		tabs := boxTabs()
		return len(tabs) == 3 && slices.ContainsFunc(tabs, func(w model.Workspace) bool { return w.Detached })
	})

	// pitwall session kill here cannot reach box, so it leaves a shared
	// session alone.
	if code, _, errOut := cliOutput("session", "kill", "-f", ls.Name); code == 0 || !strings.Contains(errOut, "has tabs on box") {
		t.Fatalf("session kill of a shared session: %d %q", code, errOut)
	}
	if len(boxTabs()) != 3 {
		t.Fatalf("box's tabs after a refused kill: %v", boxTabs())
	}

	// A guarded tab on box, whose home has no pitwall hooks, does not start.
	boxTabCount := func() int {
		c, done, err := dialHost("box", "cli")
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		m, err := syncMsg(c)
		if err != nil {
			t.Fatal(err)
		}
		return len(m.State.Workspaces)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n := boxTabCount()
	if code, _, errOut := cliOutput("new", "-s", ls.Name, "--host", "box", "--guard", "--", "claude"); code == 0 || !strings.Contains(errOut, "guarded tab not started") || !strings.Contains(errOut, "pitwall hooks install") {
		t.Fatalf("an unhooked guarded tab on box: %d %q", code, errOut)
	}
	if got := boxTabCount(); got != n {
		t.Fatalf("box has %d tabs after a refused guarded start, had %d", got, n)
	}
	// The same refusal on first contact must remove the initial shell
	// created for the new part, leaving box's existing tabs alone.
	if code, _, errOut := cliOutput("session", "new", "-d", "guard-refusal", home); code != 0 {
		t.Fatalf("local guard-refusal session: %d %q", code, errOut)
	}
	if code, _, errOut := cliOutput("new", "-s", "guard-refusal", "--host", "box", "--guard", "--", "claude"); code == 0 || !strings.Contains(errOut, "guarded tab not started") {
		t.Fatalf("guard refusal on a new part: %d %q", code, errOut)
	}
	if got := boxTabCount(); got != n {
		t.Fatalf("box kept an initial shell after refusal: %d tabs, had %d", got, n)
	}
	// A remote preset's effective extension directory gates guarded pi.
	// This is a fake agent; TestGuardPlugins exercises the actual extension.
	guardDir := filepath.Join(dir, "guard-agent")
	if err := os.WriteFile(filepath.Join(dir, "bin", "pi"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "here", "config", "pitwall", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("[[agents.preset]]\nname = \"guard-pi\"\nagent = \"pi\"\nguard = true\nenv = [%q]\n", "PI_CODING_AGENT_DIR="+guardDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	startPi := func(want bool) {
		t.Helper()
		code, _, errOut := cliOutput("new", "-s", ls.Name, "--host", "box", "--preset", "guard-pi")
		if want && code != 0 || !want && (code == 0 || !strings.Contains(errOut, "guarded tab not started")) {
			t.Fatalf("guarded pi, want %v: exit %d, %s", want, code, errOut)
		}
	}
	startPi(false)
	if got := boxTabCount(); got != n {
		t.Fatalf("unhooked pi left %d tabs, had %d", got, n)
	}
	ext := filepath.Join(guardDir, "extensions", "pitwall.ts")
	if err := os.MkdirAll(filepath.Dir(ext), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ext, agent.PiExtension(self), 0o600); err != nil {
		t.Fatal(err)
	}
	startPi(true)
	if got := boxTabCount(); got != n+1 {
		t.Fatalf("hooked pi left %d tabs, want %d", got, n+1)
	}
	if err := os.Remove(ext); err != nil {
		t.Fatal(err)
	}
	startPi(false)
	if got := boxTabCount(); got != n+1 {
		t.Fatalf("missing extension left %d tabs, want %d", got, n+1)
	}

	// A part belongs to the session that made it. The window's part of ls
	// names this machine's daemon and ls.
	boxState := func() model.State {
		t.Helper()
		c, done, err := dialHost("box", "cli")
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		m, err := syncMsg(c)
		if err != nil {
			t.Fatal(err)
		}
		return m.State
	}
	hereSt := local.State()
	here := hereSt.Daemon
	bs := boxState()
	if lsPart := bs.Session(hereSt.Session(ls.ID).Remote("box").Session); here == "" || lsPart == nil || lsPart.Origin != (model.Origin{Daemon: here, Session: ls.ID}) {
		t.Fatalf("daemon %q; box's part of %s: %+v", here, ls.ID, lsPart)
	}
	// box has a session named agents a person made there, and another
	// machine's part of its own agents session.
	other := model.Origin{Daemon: "other-daemon", Session: "other-session"}
	bc, bdone, err := dialHost("box", "cli")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []any{proto.SessionNew{Name: "agents", Cwd: home}, proto.SessionNew{Name: "agents", Cwd: home, Origin: other}} {
		if _, err := syncCLI(bc, msg); err != nil {
			t.Fatal(err)
		}
	}
	bdone()
	if code, _, errOut := cliOutput("session", "new", "-d", "agents", home); code != 0 {
		t.Fatalf("local agents session: %d %q", code, errOut)
	}
	// An agent here opens a tab on box in its agents session: it makes a
	// part of its own and joins neither of the two.
	t.Setenv("PITWALL_PANE", lp)
	code, out, errOut = cliOutput("new", "-s", "agents", "--host", "box", "--", "sleep", "60")
	t.Setenv("PITWALL_PANE", "")
	if code != 0 || !strings.HasPrefix(out, "#box:") {
		t.Fatalf("an agent's new --host box: %d %q %q", code, out, errOut)
	}
	var agents model.Session
	waitFor(t, "agents linked to its part", func() bool {
		st := local.State()
		if s := st.SessionNamed("agents"); s != nil && s.Remote("box") != nil {
			agents = *s
			return true
		}
		return false
	})
	bs = boxState()
	mine, plain, theirs := bs.PartOf(model.Origin{Daemon: here, Session: agents.ID}), bs.SessionNamed("agents"), bs.PartOf(other)
	if mine == nil || mine.Name != "agents-3" || agents.Remote("box").Session != mine.ID || len(bs.Numbered(mine.ID)) != 1 ||
		plain == nil || plain.Origin != (model.Origin{}) || len(bs.Numbered(plain.ID)) != 1 || theirs == nil || len(bs.Numbered(theirs.ID)) != 1 {
		t.Fatalf("box's sessions after an agent's start: %+v", bs.Sessions)
	}
	// Killing agents here ends its part on box and leaves the other two.
	plainID, theirsID, mineID := plain.ID, theirs.ID, mine.ID
	if err := m.Send(proto.SessionKill{SessionID: agents.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agents' part gone from box", func() bool {
		bs := boxState()
		return bs.Session(mineID) == nil
	})
	bs = boxState()
	if len(bs.Numbered(plainID)) != 1 || len(bs.Numbered(theirsID)) != 1 {
		t.Fatalf("a kill reached another session's tabs on box: %+v", bs.Sessions)
	}

	// An agent here starts a worker on box: it is recorded here first, runs
	// there under the agent's tab, and hangs under it in the window.
	orchestrator := st.Workspaces[lw].ID
	workers := func() []model.RemoteWorker {
		st := local.State()
		return st.Session(ls.ID).Workers
	}
	t.Setenv("PITWALL_PANE", lp)
	code, out, errOut = cliOutput("new", "-s", ls.Name, "--host", "box", "--", "sleep", "60")
	if code != 0 || !strings.HasPrefix(out, "#box:") || len(workers()) != 1 {
		t.Fatalf("an agent's worker on box: %d %q %q; records %+v", code, out, errOut, workers())
	}
	worker := workers()[0]
	bs = boxState()
	if w := bs.Workspace(worker.ID); worker.Host != "box" || worker.Parent != orchestrator || w == nil || w.Parent != orchestrator || !w.RemoteParent {
		t.Fatalf("record %+v; on box %+v", worker, w)
	}
	waitFor(t, "box's worker under the agent's tab", func() bool {
		st := m.State()
		w := st.Workspace(worker.ID)
		return w != nil && st.Root(*w) == orchestrator
	})
	// The same start again with its id finds it there and starts nothing.
	n = boxTabCount()
	code, out, errOut = cliOutput("new", "-s", ls.Name, "--host", "box", "--id", worker.ID, "--", "sleep", "60")
	if code != 0 || !strings.HasPrefix(out, "#box:") || boxTabCount() != n || len(workers()) != 1 {
		t.Fatalf("a repeated start: %d %q %q; box tabs %d, had %d", code, out, errOut, boxTabCount(), n)
	}
	// A guarded worker box cannot hold is refused and leaves no record.
	if code, _, errOut := cliOutput("new", "-s", ls.Name, "--host", "box", "--guard", "--", "claude"); code == 0 || !strings.Contains(errOut, "guarded tab not started") || boxTabCount() != n || len(workers()) != 1 {
		t.Fatalf("a refused guarded worker: %d %q; box tabs %d, records %+v", code, errOut, boxTabCount(), workers())
	}
	// ls --children reaches the worker on box: its host, its number
	// there, and n 0, so no "#$n" reaches a tab here.
	code, out, errOut = cliOutput("ls", "--children", "--json")
	var rows []tabJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 {
		t.Fatalf("ls --children --json: %d %q %q", code, out, errOut)
	}
	i := slices.IndexFunc(rows, func(r tabJSON) bool { return r.ID == worker.ID })
	if i < 0 || rows[i].Host != "box" || rows[i].N != 0 || !strings.HasPrefix(rows[i].Addr, "box:") || rows[i].Stale {
		t.Fatalf("box's worker in ls --children: %+v", rows)
	}
	addr := rows[i].Addr
	// read and send reach it by that address. The worker runs sleep, no
	// agent, so box's daemon, which alone knows its pane, refuses the send.
	if code, _, errOut := cliOutput("read", "--screen", addr); code != 0 {
		t.Fatalf("read %s: %d %q", addr, code, errOut)
	}
	if code, _, errOut := cliOutput("send", addr, "--", "hello from here"); code != 1 || !strings.Contains(errOut, "the pane runs no agent pitwall knows") {
		t.Fatalf("send %s: %d %q", addr, code, errOut)
	}
	// wait on a worker on box watches box's daemon.
	code, out, errOut = cliOutput("new", "-s", ls.Name, "--host", "box", "--", "sh", "-c", "echo two-$((1+1)); sleep 1")
	two := strings.TrimSpace(strings.TrimPrefix(out, "#"))
	if code != 0 || !strings.HasPrefix(two, "box:") {
		t.Fatalf("a second worker on box: %d %q %q", code, out, errOut)
	}
	if code, out, errOut := cliOutput("wait", two, "--until", "exit", "--timeout", "20s"); code != 0 || out != "exit 0\n" {
		t.Fatalf("wait %s: %d %q %q", two, code, out, errOut)
	}
	// Box out of reach: wait --children says so, never exits 0, and
	// gives up after unreachableAfter.
	prevAfter := unreachableAfter
	unreachableAfter = 2 * time.Second
	boxDown.Store(true)
	code, out, errOut = cliOutput("wait", "--children", "--until", "exit", "--timeout", "20s")
	boxDown.Store(false)
	unreachableAfter = prevAfter
	if code != waitError || !strings.Contains(errOut, "box unreachable: ssh: connect to host box") || !strings.Contains(errOut, "box unreachable for 2s") {
		t.Fatalf("wait --children with box down: %d %q %q", code, out, errOut)
	}
	// kill reaches it there.
	twoID := slices.DeleteFunc(slices.Clone(workers()), func(w model.RemoteWorker) bool { return w.ID == worker.ID })[0].ID
	if code, _, errOut := cliOutput("kill", "-f", two); code != 0 {
		t.Fatalf("kill %s: %d %q", two, code, errOut)
	}
	if bs := boxState(); bs.Workspace(twoID) != nil {
		t.Fatalf("%s still open on box after kill", two)
	}
	t.Setenv("PITWALL_PANE", "")

	// The agent's tab closes and reopens with a new id: the record follows
	// here, and the window moves box's worker under it.
	if err := m.Send(proto.KillSession{WorkspaceID: orchestrator}); err != nil {
		t.Fatal(err)
	}
	var reopened string
	waitFor(t, "the agent's tab closed", func() bool {
		st := local.State()
		i := slices.IndexFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == orchestrator })
		if i >= 0 {
			reopened = st.Closed[i].ID
		}
		return i >= 0
	})
	if err := m.Send(proto.ReopenTab{ID: reopened}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the records under the reopened tab", func() bool {
		return len(workers()) == 2 && !slices.ContainsFunc(workers(), func(w model.RemoteWorker) bool { return w.Parent != reopened })
	})
	waitFor(t, "box's worker under the reopened tab", func() bool {
		bs := boxState()
		w := bs.Workspace(worker.ID)
		return w != nil && w.Parent == reopened
	})
}
