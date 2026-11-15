package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// The owner walk names a closed tab by its record's id and by its tab's
// and pane's ids, so a Reparent or a read of a finished worker reaches it.
func TestEachIDClosed(t *testing.T) {
	var got []string
	eachID(model.State{Closed: []model.ClosedTab{{ID: "c", Tab: model.Workspace{ID: "w"}, Pane: model.Pane{ID: "p"}}}}, func(id string) { got = append(got, id) })
	if !slices.Equal(got, []string{"c", "w", "p"}) {
		t.Fatalf("ids %v", got)
	}
}

// remoteState is this machine's session s: the agent's tab lw (pane lp,
// #1) and a tab lx (#2), with a worker rw recorded on box, whose part of
// s is "part". boxState has that part with the worker (box:2 after a
// shell, box:1) and another session's tab ow.
func remoteState() (here, box model.State) {
	here = model.State{
		Daemon:   "here",
		Sessions: []model.Session{{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "part"}}, Workers: []model.RemoteWorker{{ID: "rw", Host: "box", Parent: "lw", Since: time.Now()}}}},
		Workspaces: []model.Workspace{
			{ID: "lw", SessionID: "s", Label: "agent", Tabs: []model.Tab{{ID: "lt", Layout: &layout.Node{Pane: "lp"}}}},
			{ID: "lx", SessionID: "s", Label: "other"},
		},
		Panes: []model.Pane{{ID: "lp", WorkspaceID: "lw"}},
	}
	box = model.State{
		Daemon:   "box",
		Sessions: []model.Session{{ID: "part", Name: "main", Origin: model.Origin{Daemon: "here", Session: "s"}}, {ID: "own", Name: "mine"}},
		Workspaces: []model.Workspace{
			{ID: "sh", SessionID: "part", Label: "~"},
			{ID: "rw", SessionID: "part", Label: "worker", Parent: "lw", RemoteParent: true, Tabs: []model.Tab{{ID: "rt", Layout: &layout.Node{Pane: "rp"}}}},
			{ID: "ow", SessionID: "own", Label: "someone's"},
		},
		Panes:      []model.Pane{{ID: "rp", WorkspaceID: "rw", Provider: model.ProviderClaude}},
		Activities: []model.Activity{{PaneID: "rp", WorkspaceID: "rw", Provider: model.ProviderClaude, State: model.StateCompleted}},
	}
	return here, box
}

// withHost is r with box's state as current, as host would read it.
func withHost(r *remotes, name string, hs model.State) *hostPart {
	h := &hostPart{name: name, state: hs, level: proto.Level}
	if p := partOn(hs, &r.session, name, originOf(r.state.Daemon, r.session.ID, proto.Level), false); p != nil {
		h.part = p.ID
	}
	h.view = sessionPart(hs, h.part, r.session, name)
	r.hosts[name] = h
	return h
}

// box:N and an @id this machine lacks find a tab in box's part of the
// session; #N never leaves this machine, and another session's tab there
// is never reached.
func TestRemotesResolve(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	here, box := remoteState()
	r := newRemotes(nil, here, here.Sessions[0])
	withHost(r, "box", box)
	for _, c := range []struct {
		addr, host, tab string
		pane            bool
	}{
		{"box:2", "box", "rw", false},
		{"box:2.1", "box", "rw", true},
		{"@rw", "box", "rw", false},
		{"@rp", "box", "rw", true},
		{"#2", "", "lx", false},
		{"2", "", "lx", false},
	} {
		h, w, err := r.tab(c.addr)
		if c.pane {
			h, w, _, err = r.pane(c.addr)
		}
		host := ""
		if h != nil {
			host = h.name
		}
		if err != nil || host != c.host || w.ID != c.tab {
			t.Errorf("%s: host %q tab %q, %v; want %q %q", c.addr, host, w.ID, err, c.host, c.tab)
		}
	}
	for _, addr := range []string{"@ow", "box:@ow", "box:3", "#3"} {
		if h, w, err := r.tab(addr); err == nil {
			t.Errorf("%s reached %+v on %v", addr, w, h)
		}
	}
	// A host out of reach says so.
	r.hosts["box"] = &hostPart{name: "box", err: errors.New("ssh: connect to host box: Connection refused")}
	if _, _, err := r.tab("box:2"); err == nil || !strings.Contains(err.Error(), "box unreachable") {
		t.Fatalf("an unreachable host: %v", err)
	}
}

// ls --children --json gives box's worker its host, its address there and
// n 0, and a worker on a host out of reach a stale row.
func TestRemotesChildrenJSON(t *testing.T) {
	here, box := remoteState()
	t.Setenv("PITWALL_PANE", "lp")
	r := newRemotes(nil, here, here.Sessions[0])
	withHost(r, "box", box)
	var buf strings.Builder
	if err := listChildren(&buf, r, true, false); err != nil {
		t.Fatal(err)
	}
	var rows []tabJSON
	if err := json.Unmarshal([]byte(buf.String()), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "rw" || rows[0].Host != "box" || rows[0].Addr != "box:2" || rows[0].N != 0 || rows[0].Stale || rows[0].State != "done" || rows[0].Parent != "lw" {
		t.Fatalf("rows %+v", rows)
	}
	// A local tab keeps its number and gets #N.
	if local := tabsJSON(here, "s", nil); local[1].N != 2 || local[1].Addr != "#2" || local[1].Host != "" {
		t.Fatalf("local rows %+v", local)
	}
	r.hosts["box"] = &hostPart{name: "box", err: errors.New("down")}
	buf.Reset()
	if err := listChildren(&buf, r, true, false); err != nil {
		t.Fatal(err)
	}
	rows = nil
	json.Unmarshal([]byte(buf.String()), &rows)
	if len(rows) != 1 || !rows[0].Stale || rows[0].State != "unknown" || rows[0].Addr != "@rw" || rows[0].N != 0 {
		t.Fatalf("stale rows %+v", rows)
	}
}

// feed is what a fake daemon's watch connection sends; closing it hangs up.
type feed chan model.State

// serveFeed is a fake daemon on nc for a client of kind: it sends f's
// states until f closes.
func serveFeed(nc net.Conn, kind string, f feed) {
	defer nc.Close()
	c := proto.NewConn(nc)
	if got, err := c.Recv(); err != nil || got != (proto.Hello{Version: proto.Version, Level: proto.Level, Kind: kind}) {
		return
	}
	go func() {
		for {
			if _, err := c.Recv(); err != nil {
				return
			}
		}
	}()
	for st := range f {
		if c.Send(proto.StateMsg{State: st, Level: proto.Level}) != nil {
			return
		}
	}
}

// waitFeeds makes this machine's daemon a watch fed by local, and box's
// watch connections the feeds sent on box, one per dial; a dial with none
// waiting fails as ssh would. Backoff and the unreachable limit shrink.
func waitFeeds(t *testing.T, unreachable time.Duration) (local feed, box chan feed) {
	t.Helper()
	path := sockPath(t, "watch.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	local, box = make(feed, 8), make(chan feed, 8)
	go func() {
		nc, err := ln.Accept()
		if err == nil {
			serveFeed(nc, "watch", local)
		}
	}()
	prev, prevMin, prevMax, prevAfter, prevTick := dialHost, hostRetryMin, hostRetryMax, unreachableAfter, waitTick
	t.Cleanup(func() {
		dialHost, hostRetryMin, hostRetryMax, unreachableAfter, waitTick = prev, prevMin, prevMax, prevAfter, prevTick
	})
	hostRetryMin, hostRetryMax, unreachableAfter, waitTick = 5*time.Millisecond, 20*time.Millisecond, unreachable, 5*time.Millisecond
	dialHost = func(name, kind string) (*cliConn, func(), error) {
		var f feed
		select {
		case f = <-box:
		default:
			return nil, nil, errors.New("ssh: connect to host box: Connection refused")
		}
		a, b := net.Pipe()
		go serveFeed(b, kind, f)
		c, err := hello(a, kind)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { c.Close() }, nil
	}
	return local, box
}

type waitResult struct {
	code        int
	out, stderr string
}

// runWait runs pitwall wait with args in the background.
func runWait(args ...string) <-chan waitResult {
	done := make(chan waitResult, 1)
	go func() {
		code, out, stderr := cliOutput(args...)
		done <- waitResult{code, out, stderr}
	}()
	return done
}

func waitDone(t *testing.T, done <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("wait never returned")
	}
	return waitResult{}
}

func notDone(t *testing.T, done <-chan waitResult, why string) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatalf("%s: wait returned %+v", why, r)
	case <-time.After(300 * time.Millisecond):
	}
}

// childState adds to here a local child lc of the agent's tab lw, its
// claude in state.
func childState(here model.State, state model.AgentState) model.State {
	here.Workspaces = append(slices.Clone(here.Workspaces), model.Workspace{ID: "lc", SessionID: "s", Label: "local", Parent: "lw", Tabs: []model.Tab{{ID: "ct", Layout: &layout.Node{Pane: "cp"}}}})
	here.Panes = append(slices.Clone(here.Panes), model.Pane{ID: "cp", WorkspaceID: "lc", Provider: model.ProviderClaude})
	here.Activities = []model.Activity{{PaneID: "cp", WorkspaceID: "lc", Provider: model.ProviderClaude, State: state}}
	return here
}

// A worker that finished on box while box was up does not count once box
// drops: wait holds until box is back, even with the local child done.
func TestWaitChildrenHostDrops(t *testing.T) {
	here, boxState := remoteState()
	local, box := waitFeeds(t, time.Minute)
	t.Setenv("PITWALL_PANE", "lp")
	first := make(feed, 1)
	first <- boxState
	box <- first
	local <- childState(here, model.StateWorking)
	done := runWait("wait", "--children", "--until", "done")
	notDone(t, done, "local child still working")
	close(first) // box drops
	time.Sleep(50 * time.Millisecond)
	local <- childState(here, model.StateCompleted)
	notDone(t, done, "box down, its worker finished before")
	again := make(feed, 1)
	again <- boxState
	box <- again
	r := waitDone(t, done)
	if r.code != waitReached || !strings.Contains(r.out, "#2 local done") || !strings.Contains(r.out, "box:2 worker done") || !strings.Contains(r.stderr, "box unreachable") {
		t.Fatalf("after box came back: %+v", r)
	}
	close(again)
}

// A host out of reach from the first call fails the wait once
// unreachableAfter passes, naming the host, and never exits 0.
func TestWaitChildrenHostUnreachable(t *testing.T) {
	here, _ := remoteState()
	local, _ := waitFeeds(t, 200*time.Millisecond)
	t.Setenv("PITWALL_PANE", "lp")
	local <- here
	r := waitDone(t, runWait("wait", "--children", "--until", "done"))
	if r.code != waitError || !strings.Contains(r.stderr, "box unreachable for 200ms") {
		t.Fatalf("box never reachable: %+v", r)
	}
}

// A record box has neither open nor closed lostAfter after its start is
// lost; a closed worker box does not keep as finished, as after its daemon
// restarted, lost its result. Both exit 1.
func TestWaitChildrenLost(t *testing.T) {
	for _, c := range []struct {
		name string
		box  func(model.State) model.State
		want string
	}{
		{"never started", func(b model.State) model.State {
			b.Workspaces, b.Panes, b.Activities = b.Workspaces[:1], nil, nil
			return b
		}, "box:@rw lost: box has no tab with this id"},
		{"result lost", func(b model.State) model.State {
			b.Closed = []model.ClosedTab{{ID: "c", Tab: model.Workspace{ID: "rw", SessionID: "part", Branch: "feat"}, Pane: model.Pane{ID: "rp"}}}
			b.Workspaces, b.Panes, b.Activities = b.Workspaces[:1], nil, nil
			return b
		}, "box:@rw result lost: box restarted or the tab closed unfinished; its branch is feat"},
	} {
		t.Run(c.name, func(t *testing.T) {
			here, boxState := remoteState()
			here.Sessions[0].Workers[0].Since = time.Now().Add(-lostAfter - time.Minute)
			local, box := waitFeeds(t, time.Minute)
			t.Setenv("PITWALL_PANE", "lp")
			f := make(feed, 1)
			f <- c.box(boxState)
			box <- f
			local <- here
			r := waitDone(t, runWait("wait", "--children", "--until", "done"))
			close(f)
			if r.code != waitError || !strings.Contains(r.out, c.want) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// ls --orphans lists a worker whose parent is gone and the tabs of a part
// this daemon made for a session it no longer has; adopt takes them, and
// never a part another daemon made.
func TestOrphansAdopt(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "pitwall"), 0o700)
	os.WriteFile(filepath.Join(cfg, "pitwall", "config.toml"), []byte("[[hosts]]\nname = \"box\"\n"), 0o600)
	t.Setenv("PITWALL_PANE", "lp")
	here, boxState := remoteState()
	here.Sessions[0].Remotes, here.Sessions[0].Workers = nil, nil
	boxState.Sessions = append(boxState.Sessions,
		model.Session{ID: "dead", Name: "old", Origin: model.Origin{Daemon: "here", Session: "gone"}},
		model.Session{ID: "foreign", Name: "theirs", Origin: model.Origin{Daemon: "there", Session: "gone"}})
	boxState.Workspaces = append(boxState.Workspaces,
		model.Workspace{ID: "dw", SessionID: "dead", Label: "orphan", Parent: "oldtab", RemoteParent: true},
		model.Workspace{ID: "fw", SessionID: "foreign", Label: "foreign", Parent: "x", RemoteParent: true})
	hc := fakeLocal(t,
		cliExchange{request: proto.SetOrigin{SessionID: "dead", Origin: model.Origin{Daemon: "here", Session: "s"}}, state: boxState, level: proto.Level},
		cliExchange{request: proto.Reparent{WorkspaceID: "dw", ParentTab: "lw", Origin: model.Origin{Daemon: "here", Session: "s"}}, state: boxState, level: proto.Level})
	isRecord := func(got any) bool {
		m, ok := got.(proto.RemoteWorkers)
		return ok && m.SessionID == "s" && len(m.Add) == 1 && m.Add[0].ID == "dw" && m.Add[0].Host == "box" && m.Add[0].Parent == "lw"
	}
	local := fakeLocal(t,
		cliExchange{request: proto.SessionLink{SessionID: "s", Host: "box", Remote: "dead"}, level: proto.Level},
		cliExchange{request: isRecord, level: proto.Level})
	r := newRemotes(local, here, here.Sessions[0])
	withHost(r, "box", boxState).conn = hc
	var out, errOut strings.Builder
	if err := listOrphans(&out, &errOut, r, true); err != nil {
		t.Fatal(err)
	}
	var rows []tabJSON
	json.Unmarshal([]byte(out.String()), &rows)
	if len(rows) != 1 || rows[0].ID != "dw" || rows[0].Host != "box" || rows[0].Addr != "@dw" {
		t.Fatalf("orphans %+v", rows)
	}
	if err := adopt(&out, r, []string{"@fw"}, false); err == nil || !strings.Contains(err.Error(), "not an orphan") {
		t.Fatalf("adopted another daemon's worker: %v", err)
	}
	out.Reset()
	if err := adopt(&out, r, []string{"@dw"}, false); err != nil || !strings.Contains(out.String(), "box:@dw adopted") {
		t.Fatalf("adopt: %v %q", err, out.String())
	}
}

// A title with a colon, whose start names no host of the session, is a
// title here, and nothing is dialled for it.
func TestRemotesTitleWithColon(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	here, _ := remoteState()
	here.Workspaces[1].Label = "fix: login"
	r := newRemotes(nil, here, here.Sessions[0])
	h, w, err := r.tab("fix: login")
	if err != nil || h != nil || w.ID != "lx" {
		t.Fatalf("%v %+v %v", h, w, err)
	}
	if len(r.hosts) != 0 {
		t.Fatal(fmt.Sprint("dialled ", r.sorted()))
	}
}
