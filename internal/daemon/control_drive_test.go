//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// call sends req on c and returns its result, skipping pushed frames.
func (c *jsonConn) call(t *testing.T, req string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(req), &m); err != nil {
		t.Fatal(err)
	}
	c.line(t, req)
	for {
		r := c.next(t)
		if r == nil {
			t.Fatalf("%s: the connection closed", req)
		}
		if r["type"] == "result" && r["id"] == m["id"] {
			return r
		}
	}
}

// has reports whether d has tab id, and with test that test holds for it.
func has(d *Daemon, id string, test func(model.Workspace) bool) bool {
	st := d.state()
	w := st.Workspace(id)
	return w != nil && (test == nil || test(*w))
}

func dataOf(r map[string]any) map[string]any { d, _ := r["data"].(map[string]any); return d }

// tabsWith counts d's tabs with id.
func tabsWith(d *Daemon, id string) int {
	n := 0
	for _, w := range d.state().Workspaces {
		if w.ID == id {
			n++
		}
	}
	return n
}

// TestControlStartIDs: one startId opens one tab whatever the retry
// leaves out, from any connection, at once or after the daemon restarts;
// a retry of a closed tab says so; a local child is never a remote one's.
func TestControlStartIDs(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	dir := t.TempDir()
	tabs := len(d.state().Workspaces)
	req := fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"cmd":["sleep","9"],"parentPane":"p1","startId":"00000000000000A1"}`, dir)
	a := dialJSON(t, sock)
	a.hello(t)
	r := a.call(t, req)
	if dataOf(r)["tab"] != "00000000000000a1" || dataOf(r)["pane"] == nil {
		t.Fatalf("newTab %v", r)
	}
	a.nc.Close() // the client lost the connection, and retries
	b := dialJSON(t, sock)
	b.hello(t)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			c := dialJSON(t, sock)
			c.hello(t)
			if r := c.call(t, req); r["ok"] != true {
				t.Errorf("a concurrent retry: %v", r)
			}
		})
	}
	wg.Wait()
	if r := b.call(t, req); dataOf(r)["tab"] != "00000000000000a1" || dataOf(r)["closed"] != nil {
		t.Fatalf("a retry %v", r)
	}
	st := d.state()
	if len(st.Workspaces) != tabs+1 || tabsWith(d, "00000000000000a1") != 1 {
		t.Fatalf("%d tabs after the retries, want %d", len(st.Workspaces), tabs+1)
	}
	if w := st.Workspace("00000000000000a1"); w.Parent != "w1" || w.RemoteParent || w.SessionID != "s" {
		t.Fatalf("the child %+v", w)
	}
	if r := b.call(t, `{"type":"killTab","id":"k","tab":"@00000000000000a1"}`); r["ok"] != true {
		t.Fatal(r)
	}
	if r := b.call(t, req); dataOf(r)["closed"] != true || tabsWith(d, "00000000000000a1") != 0 {
		t.Fatalf("a retry of a closed tab: %v", r)
	}
	other := fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"startId":"00000000000000a1"}`, dir)
	if r := b.call(t, other); r["ok"] != false {
		t.Fatalf("the id with another parent: %v", r)
	}
}

// TestControlStartIDRestart: a retry after the daemon stopped and a new
// one loaded its state opens no second tab.
func TestControlStartIDRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{Sessions: []model.Session{{ID: "s", Name: "work"}}}
	dir := t.TempDir()
	req := fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"cmd":["sleep","9"],"startId":"00000000000000b2"}`, dir)
	_, sock, stop := runDaemon(t, f, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	if r := c.call(t, req); r["ok"] != true {
		t.Fatal(r)
	}
	stop()
	d, sock, stop := runDaemon(t, f, func(*Options) {})
	defer stop()
	c = dialJSON(t, sock)
	c.hello(t)
	if r := c.call(t, req); dataOf(r)["tab"] != "00000000000000b2" || tabsWith(d, "00000000000000b2") != 1 || len(d.state().Workspaces) != 1 {
		t.Fatalf("a retry after a restart: %v, %d tabs", r, len(d.state().Workspaces))
	}
}

// A root's id cannot be claimed by a request from a local parent, even
// when both requests name the same session.
func TestControlStartIDDifferentLocalParent(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	dir := t.TempDir()
	for _, closed := range []bool{false, true} {
		if closed {
			if r := c.call(t, `{"type":"killTab","id":"k","tab":"@00000000000000c3"}`); r["ok"] != true {
				t.Fatal(r)
			}
		} else if r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"session":"s","startId":"00000000000000c3"}`, dir)); r["ok"] != true {
			t.Fatal(r)
		}
		before := len(d.state().Panes)
		r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"r","dir":%q,"session":"s","parentPane":"p1","startId":"00000000000000c3","detached":true}`, dir))
		if r["code"] != "conflict" || r["data"] != nil {
			t.Errorf("closed=%v: another parent claimed a root id: %v", closed, r)
		}
		if st := d.state(); len(st.Panes) != before || has(d, "00000000000000c3", func(w model.Workspace) bool { return w.Detached }) {
			t.Fatal("a refused retry changed the root tab or started a pane")
		}
	}
}

// Closing the last tab removes its session. The same request must still
// acknowledge the closed start rather than resolve a new session.
func TestControlStartIDClosedSession(t *testing.T) {
	for _, session := range []string{"", "s", "work"} {
		t.Run("session="+session, func(t *testing.T) {
			d, _, sock := controlDaemon(t, func(*Options) {})
			c := dialJSON(t, sock)
			c.hello(t)
			req := fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"session":%q,"startId":"00000000000000d4"}`, t.TempDir(), session)
			if r := c.call(t, req); r["ok"] != true {
				t.Fatal(r)
			}
			for _, tab := range []string{"w1", "w2"} {
				if r := c.call(t, fmt.Sprintf(`{"type":"killTab","id":"k","tab":"@%s"}`, tab)); r["ok"] != true {
					t.Fatal(r)
				}
			}
			if r := c.call(t, `{"type":"killTab","id":"k","tab":"@00000000000000d4"}`); r["ok"] != true {
				t.Fatal(r)
			}
			if st := d.state(); len(st.Sessions) != 0 {
				t.Fatalf("session survived its last tab: %v", st.Sessions)
			}
			if r := c.call(t, req); r["ok"] != true || dataOf(r)["closed"] != true {
				t.Fatalf("retry after closing the session: %v", r)
			}
			if st := d.state(); len(st.Sessions) != 0 || len(st.Panes) != 0 {
				t.Fatal("a closed retry started something")
			}
		})
	}
}

// The reopen archive is bounded. Forgetting a tab there must not make
// its successful start id reusable, including after a daemon restart.
func TestControlStartIDAfterArchivePruned(t *testing.T) {
	for _, prune := range []string{"age", "count"} {
		t.Run(prune, func(t *testing.T) {
			d, _, sock := controlDaemon(t, func(*Options) {})
			c := dialJSON(t, sock)
			c.hello(t)
			req := fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"session":"s","parentPane":"p1","startId":"00000000000000e5"}`, t.TempDir())
			if r := c.call(t, req); r["ok"] != true {
				t.Fatal(r)
			}
			if r := c.call(t, `{"type":"killTab","id":"k","tab":"@00000000000000e5"}`); r["ok"] != true {
				t.Fatal(r)
			}
			d.mu.Lock()
			if prune == "age" {
				for i := range d.st.Closed {
					d.st.Closed[i].ClosedAt = time.Now().AddDate(0, 0, -model.ClosedDays-1)
				}
			} else {
				for range model.ClosedKeep {
					d.st.Closed = append(d.st.Closed, model.ClosedTab{ClosedAt: time.Now()})
				}
			}
			d.st.Closed = model.PruneClosed(d.st.Closed, time.Now())
			d.mu.Unlock()
			st := d.state()
			if slices.ContainsFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == "00000000000000e5" }) {
				t.Fatal("the fixture did not prune the closed tab")
			}
			if r := c.call(t, req); r["ok"] != true || dataOf(r)["closed"] != true {
				t.Fatalf("retry after pruning the archive: %v", r)
			}
			if tabsWith(d, "00000000000000e5") != 0 {
				t.Fatal("the pruned id started a second tab")
			}
			// Load the saved snapshot through the ordinary restart path.
			f := &fakes{statsCalls: map[string]int{}, saved: d.state()}
			next, nextSock, stop := runDaemon(t, f, func(*Options) {})
			defer stop()
			retry := dialJSON(t, nextSock)
			retry.hello(t)
			if r := retry.call(t, req); r["ok"] != true || dataOf(r)["closed"] != true || tabsWith(next, "00000000000000e5") != 0 {
				t.Fatalf("retry after restarting without the archive: %v", r)
			}
		})
	}
}

func TestControlStartIDHistoryFull(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	d.mu.Lock()
	d.st.Starts = make(map[string]model.TabStart, maxStartIDs)
	for i := range maxStartIDs {
		d.st.Starts[fmt.Sprintf("%016x", i)] = model.TabStart{SessionID: "s", SessionName: "work"}
	}
	d.mu.Unlock()
	c := dialJSON(t, sock)
	c.hello(t)
	dir := t.TempDir()
	before := len(d.state().Panes)
	if r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"session":"s","startId":"fedcba9876543210"}`, dir)); r["code"] != "conflict" {
		t.Fatalf("a full history admitted a new id: %v", r)
	}
	if r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"r","dir":%q,"session":"s","startId":"0000000000000001"}`, dir)); r["ok"] != true || dataOf(r)["closed"] != true {
		t.Fatalf("a full history refused a known retry: %v", r)
	}
	if st := d.state(); len(st.Panes) != before || len(st.Starts) != maxStartIDs {
		t.Fatal("a full-history request started a process or changed history")
	}
}

func TestControlStartIDLegacyState(t *testing.T) {
	const id = "0123456789abcdef"
	f := &fakes{statsCalls: map[string]int{}}
	f.saved.Closed = []model.ClosedTab{{
		Tab:      model.Workspace{ID: id, SessionID: "s", Parent: "parent", ClientID: true},
		Session:  "work",
		ClosedAt: time.Now().AddDate(0, 0, -model.ClosedDays-1),
	}}
	d := newDaemon(t, f.options())
	st := d.state()
	if len(st.Closed) != 0 || st.Starts[id].SessionID != "s" || st.Starts[id].Parent != "parent" {
		t.Fatalf("legacy identity lost during archive pruning: %+v", st.Starts)
	}
	// A reader changing its snapshot cannot alter the retained identity.
	delete(st.Starts, id)
	d.mu.Lock()
	started, err := d.startedTab(id, "s", "parent")
	d.mu.Unlock()
	if !started || err != nil {
		t.Fatalf("legacy retry: %v, %v", started, err)
	}
}

func TestControlStartIDHandoffToOldDaemon(t *testing.T) {
	_, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	if r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"startId":"00000000000000f6"}`, t.TempDir())); r["ok"] != true {
		t.Fatal(r)
	}
	conn, err := proto.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Send(proto.Hello{Version: proto.Version, Level: proto.StartHistoryLevel - 1, Kind: "handoff"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(handoffRequest{Version: handoffVersion}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.Recv()
	if abort, ok := m.(handoffAbort); err != nil || !ok || !strings.Contains(abort.Reason, "retain start ids") {
		t.Fatalf("old daemon accepted start history: %v, %v", m, err)
	}
	conn.Close()
	answers(t, sock)
}

func TestControlSendQueueBound(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderClaude
	d.st.Activities = []model.Activity{{PaneID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking}}
	d.mu.Unlock()
	a, b := dialJSON(t, sock), dialJSON(t, sock)
	a.hello(t)
	b.hello(t)
	for i := range sendLimit + 1 {
		c := a
		if i%2 == 0 {
			c = b
		}
		r := c.call(t, `{"type":"sendPane","id":"s","pane":"@p1","text":"later"}`)
		if i < sendLimit && r["ok"] != true || i == sendLimit && r["code"] != "busy" {
			t.Fatalf("message %d: %v", i+1, r)
		}
	}
	d.mu.Lock()
	queued := len(d.mail.queue["p1"])
	d.mu.Unlock()
	if queued != sendLimit {
		t.Fatalf("%d messages retained, want %d", queued, sendLimit)
	}
}

func TestControlStartIDAfterParentCloses(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	req := fmt.Sprintf(`{"type":"newTab","id":"n","dir":%q,"parentPane":"p1","startId":"00000000000000f7"}`, t.TempDir())
	first := c.call(t, req)
	if first["ok"] != true {
		t.Fatal(first)
	}
	if r := c.call(t, `{"type":"killTab","id":"k","tab":"@w1"}`); r["ok"] != true {
		t.Fatal(r)
	}
	if r := c.call(t, req); r["ok"] != true || dataOf(r)["pane"] != dataOf(first)["pane"] || tabsWith(d, "00000000000000f7") != 1 {
		t.Fatalf("live retry after its parent closed: %v", r)
	}
}

// A lookup outside d.mu may finish after the ownership transfer or
// shutdown. It must never start a process on the old daemon afterwards.
func TestControlStartDuringShutdown(t *testing.T) {
	for _, transition := range []string{"handoff", "shutdown", "cancel"} {
		t.Run(transition, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			f := &fakes{statsCalls: map[string]int{}}
			o := f.options()
			o.Branch = func(context.Context, string) (string, bool) {
				close(entered)
				<-release
				return "", false
			}
			d := newDaemon(t, o)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			dir := t.TempDir()
			go func() {
				done <- (ctlHub{d}).Do(ctx, proto.NewSession{Cwd: dir, ID: "00000000000000f8", Cmd: []string{"sleep", "60"}})
			}()
			<-entered
			d.mu.Lock()
			d.closing = transition != "cancel"
			d.handedOff = transition == "handoff"
			d.mu.Unlock()
			if transition == "cancel" {
				cancel()
			}
			close(release)
			err := <-done
			// Restore ordinary test cleanup even on the broken code path.
			d.mu.Lock()
			d.handedOff = false
			d.mu.Unlock()
			f.mu.Lock()
			started := len(f.panes)
			f.mu.Unlock()
			if err == nil || started != 0 || len(d.state().Workspaces) != 0 {
				t.Fatalf("%s: err=%v, processes=%d", transition, err, started)
			}
		})
	}
}

func TestStartHistorySessionClosedDuringLookup(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	dir := t.TempDir()
	d, f, sock := controlDaemon(t, func(o *Options) {
		o.RepoRoot = func(_ context.Context, path string) (string, bool) {
			if path == dir {
				close(entered)
				<-release
			}
			return path, false
		}
	})
	d.mu.Lock()
	d.st.Projects = append(d.st.Projects, model.Project{ID: "project", SessionID: "s", Root: dir, Kind: model.ProjectFolder})
	d.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- (ctlHub{d}).Do(context.Background(), proto.NewWorkspace{ProjectID: "project", Name: "worker", ID: "00000000000000f9", Cmd: []string{"sleep", "60"}})
	}()
	<-entered
	c := dialJSON(t, sock)
	c.hello(t)
	for _, id := range []string{"w1", "w2"} {
		if r := c.call(t, fmt.Sprintf(`{"type":"killTab","id":"k","tab":"@%s"}`, id)); r["ok"] != true {
			t.Fatal(r)
		}
	}
	close(release)
	err := <-done
	f.mu.Lock()
	started := len(f.panes)
	f.mu.Unlock()
	if err == nil || started != 2 || len(d.state().Workspaces) != 0 {
		t.Fatalf("start after losing its session: err=%v processes=%d", err, started)
	}
}

// TestControlGuard: guarded starts over JSON pass the daemon's gate or
// start nothing; a guarded tab's splits carry the guard, and its shells
// and new tabs are refused.
func TestControlGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	settings := config.Settings{Presets: []config.AgentPreset{{Name: "safe", Agent: "claude", Guard: true}}}
	d, _, sock := controlDaemon(t, func(o *Options) { o.Fleet = func() config.Settings { return settings } })
	dir := t.TempDir()
	c := dialJSON(t, sock)
	c.hello(t)
	guarded := func(pane string) bool {
		st := d.state()
		i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == pane })
		return i >= 0 && slices.ContainsFunc(st.Panes[i].Env, guard.EnvOn)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no hooks
	tabs := len(d.state().Workspaces)
	for _, req := range []string{
		`{"type":"newTab","id":"1","dir":%q,"cmd":["claude"],"guard":true}`,
		`{"type":"newTab","id":"1","dir":%q,"preset":"safe"}`,
	} {
		if r := c.call(t, fmt.Sprintf(req, dir)); r["code"] != "refused" {
			t.Errorf("%s without hooks: %v", req, r)
		}
	}
	if n := len(d.state().Workspaces); n != tabs {
		t.Fatalf("refused starts left %d tabs, want %d", n, tabs)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", hookedDir(t))
	var g map[string]any
	for _, req := range []string{
		`{"type":"newTab","id":"1","dir":%q,"cmd":["claude"],"guard":true}`,
		`{"type":"newTab","id":"1","dir":%q,"preset":"safe"}`,
	} {
		r := c.call(t, fmt.Sprintf(req, dir))
		if p, _ := dataOf(r)["pane"].(string); !guarded(p) {
			t.Errorf("%s with hooks: %v, guarded %v", req, r, guarded(p))
		}
		g = dataOf(r)
	}
	settings.GuardWorkers = true
	r := c.call(t, fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"cmd":["claude"],"parentPane":"p1"}`, dir))
	if p, _ := dataOf(r)["pane"].(string); !guarded(p) {
		t.Errorf("guard_workers: %v", r)
	}

	r = c.call(t, fmt.Sprintf(`{"type":"splitPane","id":"1","tab":"@%s","cmd":["claude"]}`, g["tab"]))
	if p, _ := dataOf(r)["pane"].(string); !guarded(p) {
		t.Errorf("an agent split into a guarded tab: %v", r)
	}
	tabs, panes := len(d.state().Workspaces), len(d.state().Panes)
	for _, req := range []string{
		`{"type":"splitPane","id":"1","tab":"@%s","cmd":["sh"]}`,
		`{"type":"splitPane","id":"1","tab":"@%s"}`,
		`{"type":"tabNew","id":"1","pane":"@%s"}`,
	} {
		addr := g["tab"]
		if strings.Contains(req, "tabNew") {
			addr = g["pane"]
		}
		if r := c.call(t, fmt.Sprintf(req, addr)); r["code"] != "refused" {
			t.Errorf("%s in a guarded tab: %v", req, r)
		}
	}
	if st := d.state(); len(st.Workspaces) != tabs || len(st.Panes) != panes {
		t.Fatal("a refused split or tab started something")
	}
	r = c.call(t, `{"type":"splitPane","id":"1","tab":"@w1","cmd":["sh"]}`)
	if p, _ := dataOf(r)["pane"].(string); p == "" || guarded(p) {
		t.Errorf("a split of an unguarded tab: %v", r)
	}
}

// TestControlDriveOnDaemon: the tab commands change the daemon as the
// CLI's do.
func TestControlDriveOnDaemon(t *testing.T) {
	d, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	for _, req := range []string{
		`{"type":"renameTab","id":"1","tab":"#1","title":"ship"}`,
		`{"type":"detachTab","id":"1","tab":"ship"}`,
		`{"type":"setStatus","id":"1","pane":"@p2","text":"deploying"}`,
		`{"type":"setStatus","id":"1","pane":"@p2","state":"working","progress":40}`,
	} {
		if r := c.call(t, req); r["ok"] != true {
			t.Fatalf("%s: %v", req, r)
		}
	}
	if r := c.call(t, `{"type":"sendPane","id":"1","pane":"@p2","text":"hello","fromPane":"p1"}`); r["code"] != "conflict" {
		t.Fatalf("a message for a pane without an agent: %v", r)
	}
	st := d.state()
	w1, w2 := st.Workspace("w1"), st.Workspace("w2")
	if w1.Name != "ship" || !w1.Detached || w2.Status != "deploying" {
		t.Fatalf("w1 %q detached %v, w2 status %q", w1.Name, w1.Detached, w2.Status)
	}
	if r := c.call(t, `{"type":"focusTab","id":"1","tab":"@w1"}`); r["ok"] != true || has(d, "w1", func(w model.Workspace) bool { return w.Detached }) {
		t.Fatalf("focusTab %v", r)
	}
	r := c.call(t, `{"type":"tabNew","id":"1","pane":"@p2"}`)
	tab, _ := dataOf(r)["tab"].(string)
	if !has(d, tab, nil) {
		t.Fatalf("tabNew %v", r)
	}
	r = c.call(t, `{"type":"splitPane","id":"1","tab":"@`+tab+`","down":true}`)
	pane, _ := dataOf(r)["pane"].(string)
	if r := c.call(t, `{"type":"tabClose","id":"1","pane":"@`+pane+`"}`); r["ok"] != true || has(d, tab, nil) {
		t.Fatalf("tabClose %v", r)
	}
	if r := c.call(t, `{"type":"killTab","id":"1","tab":"@w2"}`); r["ok"] != true || has(d, "w2", nil) {
		t.Fatalf("killTab %v", r)
	}
	if r := c.call(t, `{"type":"killTab","id":"1","tab":"@r9"}`); r["code"] != "remote" {
		t.Fatalf("killTab of a worker on box: %v", r)
	}
	for start := time.Now(); ; time.Sleep(5 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("no tab is left")
		}
		if len(d.state().Workspaces) == 1 {
			break
		}
	}
}
