//go:build linux

package daemon

import (
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
