package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// changingControl changes the live state immediately after copying the
// request's resolution snapshot, before the wait goroutine can start.
type changingControl struct {
	*fakeControl
	after func()
}

func (f *changingControl) State() model.State {
	st := f.fakeControl.State()
	if f.after != nil {
		after := f.after
		f.after = nil
		after()
	}
	return st
}

func TestControlWaitDuringResolution(t *testing.T) {
	for _, tc := range []struct {
		name, until, outcome string
		code                 int
	}{
		{"exit zero", "exit", "exited", 0},
		{"exit three", "exit", "exited", 3},
		{"interrupted turn", "done", "reached", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeControl{st: twoSessions()}
			changing := &changingControl{fakeControl: f}
			changing.after = func() {
				if tc.until == "exit" {
					f.set(func(st *model.State) { st.Panes = st.Panes[1:] }, "p1", tc.code)
				} else {
					f.set(func(st *model.State) { st.Activities = nil })
				}
			}
			c := dialLines(t, &Server{Control: changing})
			c.send(t, linesHello(1))
			c.json(t)
			r := ask(t, c, fmt.Sprintf(`{"type":"waitPane","id":"w","pane":"@p1","until":%q,"timeoutMs":1000}`, tc.until))
			if data(r)["outcome"] != tc.outcome {
				t.Fatalf("state changed during resolution: %v", r)
			}
			if tc.until == "exit" && data(r)["exit"].(map[string]any)["code"] != float64(tc.code) {
				t.Fatalf("lost program exit code: %v", r)
			}
		})
	}
}

func TestControlWaitTimeoutBounds(t *testing.T) {
	f, c := controlServer(t)
	f.set(func(st *model.State) { st.Panes[1].Exited = true })
	for _, tc := range []struct {
		ms   int64
		code string
	}{
		{0, ""},
		{int64((1<<63 - 1) / time.Millisecond), ""},
		{int64((1<<63-1)/time.Millisecond) + 1, "invalid"},
		{1<<63 - 1, "invalid"},
	} {
		root := loadSchema(t)
		req := map[string]any{"type": "waitPane", "id": "w", "pane": "@p2", "until": "exit", "timeoutMs": float64(tc.ms)}
		if valid := len(validate(root, root, req, "request")) == 0; valid != (tc.code == "") {
			t.Errorf("timeoutMs %d: schema validity %v, want %v", tc.ms, valid, tc.code == "")
		}
		r := ask(t, c, fmt.Sprintf(`{"type":"waitPane","id":"w","pane":"@p2","until":"exit","timeoutMs":%d}`, tc.ms))
		if code, _ := r["code"].(string); code != tc.code {
			t.Errorf("timeoutMs %d: %v, want code %q", tc.ms, r, tc.code)
		}
	}
}

// Each advertised control command must have exactly one client schema.
func TestControlCommandSchema(t *testing.T) {
	root := loadSchema(t)
	defs := root["$defs"].(schema)
	for _, command := range controlCommands {
		matches := 0
		for _, alternative := range defs["client"].(schema)["oneOf"].([]any) {
			ref := alternative.(schema)["$ref"].(string)
			def := defs[strings.TrimPrefix(ref, defsRefStart)].(schema)
			if def["properties"].(schema)["type"].(schema)["const"] == command {
				matches++
			}
		}
		if matches != 1 {
			t.Errorf("advertised command %s has %d schema variants, want 1", command, matches)
		}
	}
}

// A wait that has reached its outcome may still be blocked behind other
// results. Closing the connection must release its watch without a drain.
func TestControlWaitCancelAdmission(t *testing.T) {
	f := &fakeControl{st: twoSessions()}
	w := watchPane(f)
	w.selectPane("p1")
	c := newConn(nil, nil, true)
	c.frames = maxFrames
	s := &Server{Control: f}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.waitPane(ctx, c, "w", "p1", "exit", 0, model.State{}, w)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a full result queue did not block admission")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(c.dead)
		t.Fatal("cancellation left a wait blocked on its result queue")
	}
	if f.watching() != 0 || len(c.q) != 0 {
		t.Fatalf("after cancellation: %d watches and %d queued results", f.watching(), len(c.q))
	}
}

// Draining the queue must wake all producers along with the reader.
// Otherwise the reader can consume the only notice and leave completed
// waits asleep indefinitely, even though their results now fit.
func TestControlAdmissionWakesAll(t *testing.T) {
	c := newConn(nil, nil, true)
	c.frames = maxFrames
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 3)
	go func() { done <- c.wait() }()
	for range 2 {
		go func() { done <- c.queueControl(ctx, []byte(`{"type":"result"}`), false) }()
	}
	select {
	case <-done:
		t.Fatal("admission did not block on the full queue")
	case <-time.After(20 * time.Millisecond):
	}
	c.mu.Lock()
	c.frames = 0
	c.signalDrained()
	c.mu.Unlock()
	for range 3 {
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("admission was canceled instead of resumed")
			}
		case <-time.After(time.Second):
			close(c.dead)
			t.Fatal("a producer or reader slept after the queue drained")
		}
	}
}

func TestControlTabsCoalesceAndCancel(t *testing.T) {
	f := &fakeControl{st: twoSessions()}
	s := &Server{Control: f}
	c := newConn(nil, nil, true) // no writer: queued lists cannot drain
	t.Cleanup(func() { s.endControl(c); close(c.dead) })
	queuedTabs := func() []byte {
		c.mu.Lock()
		defer c.mu.Unlock()
		var frame []byte
		for _, o := range c.q {
			if o.tabs {
				if frame != nil {
					t.Fatal("more than one tabs list queued")
				}
				frame = slices.Clone(o.frame)
			}
		}
		if c.frames > maxFrames || c.fbytes > maxFrameBytes {
			t.Fatalf("queue holds %d frames of %d bytes", c.frames, c.fbytes)
		}
		return frame
	}
	await := func(want string) {
		t.Helper()
		for end := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
			if strings.Contains(string(queuedTabs()), want) {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("queued tabs did not reach %q", want)
			}
		}
	}
	s.subscribeTabs(c, "s", "main")
	await(`"title":"tests"`)
	for i := range maxFrames + 1 {
		name := fmt.Sprintf("name-%d", i)
		f.set(func(st *model.State) { st.Workspaces[1].Name = name })
		await(fmt.Sprintf(`"title":%q`, name))
	}
	// Replace a session without a writer; the previous session must not
	// have a frame waiting ahead of the replacement's initial list.
	s.subscribeTabs(c, "s2", "side")
	await(`"name":"side"`)
	if strings.Contains(string(queuedTabs()), `"name":"main"`) {
		t.Fatal("a replaced subscription retained its previous session")
	}
	s.control(c, "unsubscribeTabs", "u", []byte(`{"type":"unsubscribeTabs","id":"u"}`))
	if frame := queuedTabs(); frame != nil {
		t.Fatalf("unsubscribe left tabs queued: %s", frame)
	}
}

// Replacement must release a subscriber blocked on bytes while small
// command results can still fit. Repeating this must not retain watches.
func TestControlTabsCancelAdmission(t *testing.T) {
	f := &fakeControl{st: twoSessions()}
	s := &Server{Control: f}
	c := newConn(nil, nil, true)
	c.push(out{frame: make([]byte, maxFrameBytes-4096)})
	f.set(func(st *model.State) { st.Workspaces[0].Name = strings.Repeat("x", 8192) })
	t.Cleanup(func() { s.endControl(c); close(c.dead) })
	for i := range 5 {
		s.subscribeTabs(c, fmt.Sprint(i), "main")
	}
	s.control(c, "unsubscribeTabs", "u", []byte(`{"type":"unsubscribeTabs","id":"u"}`))
	for end := time.Now().Add(time.Second); f.watching() != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("canceled subscriptions retained %d watches without a queue drain", f.watching())
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.q {
		if o.tabs {
			t.Fatal("a canceled subscription admitted a tabs frame")
		}
	}
}

func TestControlTabsReplaceSlowReader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs a loopback socket that blocks on a 2 MiB frame; the daemon serves control on Linux only")
	}
	f := &fakeControl{st: twoSessions()}
	f.st.Workspaces[1].Name = strings.Repeat("x", 2<<20)
	s := &Server{Control: f}
	c := dialLines(t, s)
	c.send(t, linesHello(1))
	c.json(t)
	c.line(t, `{"type":"subscribeTabs","id":"first","session":"main"}`)
	if r := c.json(t); r["id"] != "first" || r["ok"] != true {
		t.Fatal(r)
	}
	// The real writer holds wmu while its large tabs frame is blocked
	// on this client's small socket buffers.
	serverConn := onlyConn(s)
	for end := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if !serverConn.wmu.TryLock() {
			break
		}
		serverConn.wmu.Unlock()
		if time.Now().After(end) {
			t.Fatal("the large tabs frame never blocked the writer")
		}
	}
	c.line(t, `{"type":"subscribeTabs","id":"replace","session":"side"}`)
	// Resume reading with room for the large frame after exercising
	// backpressure; small TCP windows needlessly slow this test's drain.
	if err := c.nc.(*net.TCPConn).SetReadBuffer(4 << 20); err != nil {
		t.Fatal(err)
	}
	for {
		r := c.json(t)
		if r["id"] == "replace" {
			if r["ok"] != true {
				t.Fatal(r)
			}
			break
		}
		if r["type"] != "tabs" {
			t.Fatalf("before the replacement acknowledgement: %.200v", r)
		}
	}
	if r := c.json(t); r["type"] != "tabs" || r["session"].(map[string]any)["name"] != "side" {
		t.Fatalf("old session after replacement acknowledgement: %.200v", r)
	}
	if r := ask(t, c, `{"type":"unsubscribeTabs","id":"stop"}`); r["ok"] != true {
		t.Fatal(r)
	}
}

// fakeControl is a daemon's state for the control commands; set and exit
// change it as the daemon does, telling the watches.
type fakeControl struct {
	mu       sync.Mutex
	st       model.State
	text     string // what Read and Scrollback give
	reply    string // what Read gives without screen, "" for none kept
	watches  map[*[2]any]bool
	settings config.Settings
	reqs     []any      // what Do, OpenPane and NewTab were asked
	envs     [][]string // OpenPane's env, in order
	fail     error      // what Do, OpenPane and NewTab answer
	onDo     func(req any)
}

func (f *fakeControl) Settings() config.Settings { f.mu.Lock(); defer f.mu.Unlock(); return f.settings }

// edit changes the fake's fields under its lock.
func (f *fakeControl) edit(change func(f *fakeControl)) { f.mu.Lock(); defer f.mu.Unlock(); change(f) }

// Do records req; a NewSession opens a tab with its ID and pane n+ID,
// once, as the daemon's start ids do.
func (f *fakeControl) Do(ctx context.Context, req any) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	err, onDo := f.fail, f.onDo
	f.mu.Unlock()
	if m, isNew := req.(proto.NewSession); isNew && err == nil {
		f.set(func(st *model.State) {
			if st.Workspace(m.ID) == nil && !slices.ContainsFunc(st.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == m.ID }) {
				st.Workspaces = append(st.Workspaces, model.Workspace{ID: m.ID, SessionID: m.SessionID, Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "n" + m.ID}}}})
				st.Panes = append(st.Panes, model.Pane{ID: "n" + m.ID, WorkspaceID: m.ID, Env: m.Env})
			}
		})
	}
	if onDo != nil {
		onDo(req)
	}
	return err
}

func (f *fakeControl) OpenPane(m proto.OpenPane, env []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs, f.envs = append(f.reqs, m), append(f.envs, env)
	return "new", f.fail
}

func (f *fakeControl) NewTab(ctx context.Context, m proto.NewTab) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, m)
	return "tab", f.fail
}

// asked is the last request Do, OpenPane or NewTab got, nil for none.
func (f *fakeControl) asked() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return nil
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeControl) State() model.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.st
	st.Panes = slices.Clone(st.Panes)
	st.Activities = slices.Clone(st.Activities)
	st.Workspaces = slices.Clone(st.Workspaces)
	st.Sessions = slices.Clone(st.Sessions)
	return st
}

func (f *fakeControl) Read(pane string, screen bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !screen && f.reply == "" {
		return "", errors.New("no reply kept")
	}
	if !screen {
		return f.reply, nil
	}
	return f.text, nil
}

func (f *fakeControl) Scrollback(pane string, lines int) (string, error) { return f.text, nil }

func (f *fakeControl) Watch(changed func(), exited func(string, int)) func() {
	w := &[2]any{changed, exited}
	f.mu.Lock()
	if f.watches == nil {
		f.watches = map[*[2]any]bool{}
	}
	f.watches[w] = true
	f.mu.Unlock()
	return func() { f.mu.Lock(); delete(f.watches, w); f.mu.Unlock() }
}

// set changes the state under the lock and tells the watches; with exit,
// pane exited with code first, as the daemon's order is.
func (f *fakeControl) set(edit func(*model.State), exit ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(&f.st)
	for w := range f.watches {
		w[0].(func())()
		if exit != nil {
			w[1].(func(string, int))(exit[0].(string), exit[1].(int))
		}
	}
}

func (f *fakeControl) watching() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.watches) }

// twoSessions has session "main" with tabs build (#1, pane p1, a working
// claude) and tests (#2, pane p2, a shell), session "side" with tab docs
// (#1, pane p3), and a worker on box that build started.
func twoSessions() model.State {
	tab := func(id, session, name, pane string) model.Workspace {
		return model.Workspace{ID: id, SessionID: session, Name: name, NameSet: true, Tabs: []model.Tab{{ID: "t" + id, Layout: &layout.Node{Pane: pane}}}}
	}
	return model.State{
		Sessions: []model.Session{
			{ID: "m1", Name: "main", Order: []string{"w1", "w2"}, UsedAt: time.Unix(2, 0), Workers: []model.RemoteWorker{{ID: "r9", Host: "box", Parent: "w1"}}},
			{ID: "s1", Name: "side", Order: []string{"w3"}, UsedAt: time.Unix(1, 0)},
		},
		Workspaces: []model.Workspace{tab("w1", "m1", "build", "p1"), tab("w2", "m1", "tests", "p2"), tab("w3", "s1", "docs", "p3")},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "w1", Cmd: []string{"claude"}, Provider: model.ProviderClaude, Env: []string{"SECRET=1"}},
			{ID: "p2", WorkspaceID: "w2", Cmd: []string{"sh", "-c", "make"}},
			{ID: "p3", WorkspaceID: "w3"},
		},
		Activities: []model.Activity{{PaneID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: time.Unix(5, 0)}},
	}
}

func controlServer(t *testing.T) (*fakeControl, *lineClient) {
	t.Helper()
	f := &fakeControl{st: twoSessions(), text: "screen\n"}
	c := dialLines(t, &Server{Control: f})
	c.send(t, linesHello(1))
	w := c.json(t)
	caps := fmt.Sprint(w["capabilities"])
	for _, typ := range controlCommands {
		if !strings.Contains(caps, typ) {
			t.Fatalf("welcome capabilities %s lack %s", caps, typ)
		}
	}
	return f, c
}

// ask sends a request and returns its result, checked against the schema.
func ask(t *testing.T, c *lineClient, req string) map[string]any {
	t.Helper()
	c.line(t, req)
	r := c.json(t)
	checkFrame(t, r)
	return r
}

func checkFrame(t *testing.T, frame map[string]any) {
	t.Helper()
	root := loadSchema(t)
	for _, e := range validate(root, root, frame, "frame") {
		t.Error(e)
	}
}

func data(r map[string]any) map[string]any { d, _ := r["data"].(map[string]any); return d }

func ids(r map[string]any) string {
	var out []string
	for _, row := range data(r)["tabs"].([]any) {
		m := row.(map[string]any)
		out = append(out, fmt.Sprintf("%s=%s", m["addr"], m["id"]))
	}
	return strings.Join(out, " ")
}

// TestControlRequests: each command's result, by schema and by value.
func TestControlRequests(t *testing.T) {
	_, c := controlServer(t)
	for _, tc := range []struct{ req, code, want string }{
		{`{"type":"listTabs","id":"1"}`, "", "#1=w1 #2=w2 @r9=r9"},
		{`{"type":"listTabs","id":"1","session":"side"}`, "", "#1=w3"},
		{`{"type":"listTabs","id":"1","session":"s1"}`, "", "#1=w3"},
		{`{"type":"listTabs","id":"1","children":"p1"}`, "", "@r9=r9"},
		{`{"type":"listTabs","id":"1","children":"p2"}`, "", ""},
		{`{"type":"listTabs","id":"1","session":"nope"}`, "not-found", ""},
		{`{"type":"listTabs","id":"1","children":"p1","orphans":true}`, "invalid", ""},
		{`{"type":"listTabs","id":"1","env":["PITWALL_GUARD=0"]}`, "invalid", ""},
		{`{"type":"readPane","id":"1","pane":"#1","session":"main","screen":true}`, "", ""},
		{`{"type":"readPane","id":"1","pane":"#1"}`, "invalid", ""}, // two sessions: #1 is which?
		{`{"type":"readPane","id":"1","pane":"build"}`, "invalid", ""},
		{`{"type":"readPane","id":"1","pane":"@p3","screen":true}`, "", ""},
		{`{"type":"readPane","id":"1","pane":"#1","session":"main"}`, "not-found", ""}, // no reply kept
		{`{"type":"readPane","id":"1","pane":"#9","session":"main"}`, "not-found", ""},
		{`{"type":"readPane","id":"1","pane":"","session":"main"}`, "invalid", ""},
		{`{"type":"readPane","id":"1","pane":"@r9"}`, "remote", ""},
		{`{"type":"readPane","id":"1","pane":"box:2","session":"main"}`, "remote", ""},
		{`{"type":"readPane","id":"1","pane":"#1","session":"s"}`, "not-found", ""},
		{`{"type":"scrollback","id":"1","pane":"@p2","lines":10000}`, "", ""},
		{`{"type":"scrollback","id":"1","pane":"@p2","lines":10001}`, "invalid", ""},
		{`{"type":"scrollback","id":"1","pane":"@p2","lines":-1}`, "invalid", ""},
		{`{"type":"waitPane","id":"1","pane":"@p2","until":"later"}`, "invalid", ""},
		{`{"type":"waitPane","id":"1","pane":"@p2","until":"exit","timeoutMs":-5}`, "invalid", ""},
		{`{"type":"unsubscribeTabs","id":"1"}`, "", ""},
	} {
		r := ask(t, c, tc.req)
		if code, _ := r["code"].(string); r["id"] != "1" || code != tc.code {
			t.Errorf("%s: %v, want code %q", tc.req, r, tc.code)
			continue
		}
		if tc.want != "" && ids(r) != tc.want {
			t.Errorf("%s: tabs %s, want %s", tc.req, ids(r), tc.want)
		}
		if strings.Contains(tc.req, "readPane") && tc.code == "" && data(r)["text"] != "screen\n" {
			t.Errorf("%s: %v", tc.req, r)
		}
	}
	r := ask(t, c, `{"type":"listTabs","id":"1"}`)
	if s := data(r)["session"].(map[string]any); s["name"] != "main" || s["id"] != "m1" {
		t.Errorf("session %v, want main", s)
	}
	stale := data(r)["tabs"].([]any)[2].(map[string]any)
	if stale["stale"] != true || stale["state"] != "unknown" || stale["host"] != "box" || stale["parent"] != "w1" || len(stale["pane_list"].([]any)) != 0 {
		t.Errorf("stale row %v", stale)
	}
}

// TestControlSessions: #1 is a different tab for clients that name
// different sessions, and one session needs none.
func TestControlSessions(t *testing.T) {
	f, a := controlServer(t)
	b := dialLines(t, &Server{Control: f})
	b.send(t, linesHello(1))
	b.json(t)
	ra := ask(t, a, `{"type":"waitPane","id":"a","pane":"#1","session":"main","until":"blocked","timeoutMs":1}`)
	rb := ask(t, b, `{"type":"listTabs","id":"b","session":"side"}`)
	if ra["code"] != "timeout" || ids(rb) != "#1=w3" {
		t.Fatalf("main #1 %v, side %v", ra, rb)
	}
	f.set(func(st *model.State) { st.Sessions = st.Sessions[:1] })
	if r := ask(t, a, `{"type":"scrollback","id":"1","pane":"#2"}`); r["ok"] != true {
		t.Fatalf("one session, no session named: %v", r)
	}
}

// TestControlNoProcessFields: no control result carries a command line,
// an environment, a process or the monitor.
func TestControlNoProcessFields(t *testing.T) {
	f, c := controlServer(t)
	forbidden := regexp.MustCompile(`(?i)^(cmd|command|env|environ|args|argv|pid|process|processes|monitor|resume)`)
	var frames []map[string]any
	for _, req := range []string{
		`{"type":"listTabs","id":"1"}`,
		`{"type":"listTabs","id":"1","children":"p1"}`,
		`{"type":"listTabs","id":"1","orphans":true}`,
		`{"type":"readPane","id":"1","pane":"@p1","screen":true}`,
		`{"type":"scrollback","id":"1","pane":"@p1"}`,
		`{"type":"waitPane","id":"1","pane":"@p1","until":"exit"}`,
		`{"type":"newTab","id":"1","dir":"/src","cmd":["claude"],"session":"main"}`,
		`{"type":"splitPane","id":"1","tab":"@w1","cmd":["claude"]}`,
		`{"type":"tabNew","id":"1","pane":"@p2"}`,
		`{"type":"setStatus","id":"1","pane":"@p2","text":"ok"}`,
		`{"type":"sendPane","id":"1","pane":"@p3","text":"ok"}`,
	} {
		if strings.Contains(req, "waitPane") {
			c.line(t, req)
			f.set(func(st *model.State) { st.Panes[0].Exited, st.Panes[0].ExitCode = true, 0 })
			frames = append(frames, c.json(t))
			continue
		}
		frames = append(frames, ask(t, c, req))
	}
	frames = append(frames, ask(t, c, `{"type":"subscribeTabs","id":"1"}`), c.json(t))
	var walk func(v any, at string)
	walk = func(v any, at string) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if forbidden.MatchString(k) {
					t.Errorf("%s.%s is in a control result", at, k)
				}
				walk(e, at+"."+k)
			}
		case []any:
			for _, e := range x {
				walk(e, at+"[]")
			}
		case string:
			if strings.Contains(x, "SECRET") || strings.Contains(x, "make") {
				t.Errorf("%s holds a pane's environment or command: %q", at, x)
			}
		}
	}
	for _, fr := range frames {
		if fr["ok"] == false {
			t.Errorf("%v", fr)
		}
		checkFrame(t, fr)
		walk(fr, fmt.Sprint(fr["type"]))
	}
}

// TestControlWait: waitPane's outcomes, the program's own exit code
// apart from the wait's, the timeout, and the cap on pending waits.
func TestControlWait(t *testing.T) {
	f, c := controlServer(t)
	await := func(req string, change func(*model.State), exit ...any) map[string]any {
		t.Helper()
		c.line(t, req)
		for f.watching() == 0 {
			time.Sleep(time.Millisecond)
		}
		f.set(change, exit...)
		r := c.json(t)
		checkFrame(t, r)
		for f.watching() != 0 {
			time.Sleep(time.Millisecond)
		}
		return r
	}
	gone := func(st *model.State) {
		st.Panes = slices.DeleteFunc(st.Panes, func(p model.Pane) bool { return p.ID == "p2" })
	}
	for _, code := range []int{0, 2, 3, 124} {
		r := await(`{"type":"waitPane","id":"w","pane":"@p2","until":"exit"}`, gone, "p2", code)
		if e := data(r)["exit"].(map[string]any); data(r)["outcome"] != "exited" || e["code"] != float64(code) || e["unknown"] != false {
			t.Fatalf("exit %d: %v", code, r)
		}
		f.set(func(st *model.State) { *st = twoSessions() })
	}
	r := await(`{"type":"waitPane","id":"w","pane":"@p2","until":"done"}`, gone) // closed with no exit code
	if data(r)["outcome"] != "closed" {
		t.Fatalf("closed: %v", r)
	}
	f.set(func(st *model.State) { *st = twoSessions() })
	r = await(`{"type":"waitPane","id":"w","pane":"@p1","until":"done"}`, func(st *model.State) { st.Activities[0].State = model.StateCompleted })
	if data(r)["outcome"] != "reached" || data(r)["state"] != "done" {
		t.Fatalf("done: %v", r)
	}
	f.set(func(st *model.State) { *st = twoSessions() })
	r = await(`{"type":"waitPane","id":"w","pane":"@p1","until":"done"}`, func(st *model.State) {
		st.Activities[0].State, st.Activities[0].Detail = model.StatePendingApproval, "Run make?"
	})
	if data(r)["outcome"] != "blocked" || data(r)["question"] != "Run make?" {
		t.Fatalf("blocked: %v", r)
	}

	for i := range maxWaits {
		c.line(t, fmt.Sprintf(`{"type":"waitPane","id":"%d","pane":"@p3","until":"done"}`, i))
	}
	if r := ask(t, c, `{"type":"waitPane","id":"x","pane":"@p3","until":"done","timeoutMs":1}`); r["code"] != "busy" {
		t.Fatalf("past the cap: %v", r)
	}
	if r := ask(t, c, `{"type":"listTabs","id":"y"}`); r["ok"] != true {
		t.Fatalf("other commands past the cap: %v", r)
	}
	c.nc.Close()
	for f.watching() != 0 { // the pending waits end with the connection
		time.Sleep(time.Millisecond)
	}
}

// TestControlTabs: subscribeTabs pushes the list now and on each change,
// none for a change that leaves the rows as they were, and none after
// unsubscribeTabs.
func TestControlTabs(t *testing.T) {
	f, c := controlServer(t)
	if r := ask(t, c, `{"type":"subscribeTabs","id":"s","session":"main"}`); r["ok"] != true {
		t.Fatal(r)
	}
	first := c.json(t)
	checkFrame(t, first)
	if first["type"] != "tabs" || first["session"].(map[string]any)["name"] != "main" || len(first["tabs"].([]any)) != 3 {
		t.Fatalf("first push %v", first)
	}
	f.set(func(st *model.State) { st.Workspaces[2].Name = "notes" }) // another session's tab: no push
	f.set(func(st *model.State) { st.Workspaces[1].Name = "lint" })
	if m := c.json(t); m["type"] != "tabs" || !strings.Contains(mustString(m), `"title":"lint"`) {
		t.Fatalf("after a rename %v", m)
	}
	if r := ask(t, c, `{"type":"unsubscribeTabs","id":"u"}`); r["ok"] != true {
		t.Fatal(r)
	}
	f.set(func(st *model.State) { st.Workspaces[1].Name = "vet" })
	if r := ask(t, c, `{"type":"listTabs","id":"l"}`); r["type"] != "result" {
		t.Fatalf("a push after unsubscribeTabs: %v", r)
	}
}

// TestControlTooLarge: a pane's text over 4 MiB is tooLarge, and a
// client that sends large reads and reads nothing is held back.
func TestControlTooLarge(t *testing.T) {
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) { f.text = strings.Repeat("x", maxResultBytes) })
	if r := ask(t, c, `{"type":"readPane","id":"1","pane":"@p1","screen":true}`); r["code"] != "tooLarge" {
		t.Fatalf("%.200v", r)
	}
	f.edit(func(f *fakeControl) { f.text = strings.Repeat("x", 1<<20) })
	req := []byte(`{"type":"readPane","id":"1","pane":"@p1",` + strings.Repeat(" ", 10_000) + `"screen":true}` + "\n") // 10 MB in all
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for range 1000 {
			if _, err := c.nc.Write(req); err != nil {
				return
			}
		}
	}()
	select {
	case <-wrote:
		t.Fatal("the server read 1000 reads of 1 MiB from a client that reads nothing")
	case <-time.After(500 * time.Millisecond):
	}
}

// TestNoControlOnWebSocket: a WebSocket server, as on the [web] and
// [remote] listeners, has no Control: welcome lists no control command
// and each one is unsupported after a valid hello.
func TestNoControlOnWebSocket(t *testing.T) {
	s := &Server{Token: fixed("tok"), Chats: newFakeChats(t)}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	c, _ := dial(t, hs, "", "")
	c.send(t, hi("tok", 1))
	w := c.json(t)
	if w["type"] != "welcome" {
		t.Fatal(w)
	}
	for _, typ := range controlCommands {
		if slices.Contains(w["capabilities"].([]any), any(typ)) {
			t.Errorf("welcome lists %s", typ)
		}
		c.send(t, map[string]any{"type": typ, "id": typ, "pane": "#1", "until": "exit"})
		if r := c.json(t); r["code"] != "unsupported" {
			t.Errorf("%s: %v", typ, r)
		}
	}
}

// TestControlDrive: each command that starts or drives a tab asks the
// daemon what the CLI command asks, after the same checks.
func TestControlDrive(t *testing.T) {
	type tc struct {
		req, code string
		want      any // the request the daemon gets; nil for none
	}
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) {
		f.settings.Presets = []config.AgentPreset{{Name: "safe", Agent: "claude", Args: []string{"--x"}, Env: []string{"A=1"}, Guard: true}}
	})
	for _, tc := range []tc{
		{`{"type":"newTab","id":"1","dir":"/src","cmd":["claude"],"session":"main","startId":"00000000000000AA","name":"fix"}`, "",
			proto.NewSession{Name: "fix", Cwd: "/src", SessionID: "m1", Cmd: []string{"claude"}, ID: "00000000000000aa"}},
		{`{"type":"newTab","id":"1","dir":"/src","preset":"safe","model":"opus","parentPane":"p3","startId":"00000000000000ab"}`, "",
			proto.NewSession{Cwd: "/src", SessionID: "s1", Cmd: []string{"claude", "--model", "opus", "--x"}, Env: []string{"A=1", "PITWALL_GUARD=1"}, ParentPane: "p3", ID: "00000000000000ab"}},
		{`{"type":"newTab","id":"1","dir":"src"}`, "invalid", nil},
		{`{"type":"newTab","id":"1","dir":"/src","startId":"xyz"}`, "invalid", nil},
		{`{"type":"newTab","id":"1","dir":"/src","guard":true}`, "invalid", nil},
		{`{"type":"newTab","id":"1","dir":"/src","preset":"nope"}`, "invalid", nil},
		{`{"type":"newTab","id":"1","dir":"/src","parentPane":"p9"}`, "not-found", nil},
		{`{"type":"newTab","id":"1","dir":"/src","cmd":["sh"],"env":["PITWALL_GUARD=0"]}`, "invalid", nil},
		{`{"type":"newTab","id":"1","dir":"/src","pane":"#1"}`, "invalid", nil},
		{`{"type":"splitPane","id":"1","tab":"#2","session":"main","down":true,"cmd":["make"]}`, "",
			proto.OpenPane{WorkspaceID: "w2", Dir: layout.Vertical, Cmd: []string{"make"}}},
		{`{"type":"splitPane","id":"1","tab":"@r9"}`, "remote", nil},
		{`{"type":"sendPane","id":"1","pane":"@p1","text":"hi","fromPane":"p2"}`, "", proto.Send{Pane: "p1", From: "p2", Text: "hi"}},
		{`{"type":"sendPane","id":"1","pane":"@p1","text":" "}`, "invalid", nil},
		{`{"type":"sendPane","id":"1","pane":"@p1","text":"hi","fromPane":"p9"}`, "not-found", nil},
		{`{"type":"killTab","id":"1","tab":"docs","session":"side"}`, "", proto.KillSession{WorkspaceID: "w3"}},
		{`{"type":"killTab","id":"1","tab":"docs","session":"side","title":"x"}`, "invalid", nil},
		{`{"type":"killTab","id":"1","tab":"#1"}`, "invalid", nil}, // two sessions
		{`{"type":"renameTab","id":"1","tab":"@w1","title":"ship"}`, "", proto.RenameWorkspace{WorkspaceID: "w1", Name: "ship"}},
		{`{"type":"renameTab","id":"1","tab":"@w1","title":""}`, "invalid", nil},
		{`{"type":"detachTab","id":"1","tab":"@w1"}`, "", proto.DetachSession{WorkspaceID: "w1", Detached: true}},
		{`{"type":"focusTab","id":"1","tab":"@w1"}`, "", proto.FocusSession{WorkspaceID: "w1"}},
		{`{"type":"tabNew","id":"1","pane":"@p2"}`, "", proto.NewTab{FromPane: "p2"}},
		{`{"type":"tabClose","id":"1","pane":"@p2"}`, "", proto.CloseTab{WorkspaceID: "w2"}},
		{`{"type":"setStatus","id":"1","pane":"@p2","text":"deploying"}`, "", proto.SetStatus{WorkspaceID: "w2", Text: "deploying"}},
		{`{"type":"setStatus","id":"1","pane":"@p2","state":"working","progress":40,"app":"make"}`, "",
			proto.AgentEvent{Pane: "p2", Provider: "report", Payload: []byte(`{"name":"","program":"` + progstatus.Encode(progstatus.Report{State: progstatus.Working, App: "make", Progress: 40}) + `"}`)}},
		{`{"type":"setStatus","id":"1","pane":"@p2","state":"done","progress":40}`, "invalid", nil},
		{`{"type":"setStatus","id":"1","pane":"@p2","state":"later"}`, "invalid", nil},
	} {
		f.edit(func(f *fakeControl) { f.reqs = nil })
		r := ask(t, c, tc.req)
		if code, _ := r["code"].(string); code != tc.code {
			t.Errorf("%s: %v, want code %q", tc.req, r, tc.code)
			continue
		}
		got := f.asked()
		if tc.want == nil && got != nil || tc.want != nil && fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", tc.want) {
			if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", tc.want) {
				t.Errorf("%s: asked %#v, want %#v", tc.req, got, tc.want)
			}
		}
	}
	r := ask(t, c, `{"type":"newTab","id":"1","dir":"/src","session":"main","startId":"00000000000000aa"}`)
	if d := data(r); d["tab"] != "00000000000000aa" || d["pane"] != "n00000000000000aa" || d["closed"] != nil {
		t.Errorf("newTab data %v", r)
	}
	f.set(func(st *model.State) {
		st.Workspaces = slices.DeleteFunc(st.Workspaces, func(w model.Workspace) bool { return w.ID == "00000000000000aa" })
		st.Closed = append(st.Closed, model.ClosedTab{Tab: model.Workspace{ID: "00000000000000aa"}})
	})
	if r := ask(t, c, `{"type":"newTab","id":"1","dir":"/src","session":"main","startId":"00000000000000aa"}`); data(r)["closed"] != true {
		t.Errorf("a retry of a closed tab: %v", r)
	}
	f.edit(func(f *fakeControl) { f.fail = ControlError("refused", "the guard") })
	if r := ask(t, c, `{"type":"splitPane","id":"1","tab":"@w1"}`); r["code"] != "refused" {
		t.Errorf("a refused split: %v", r)
	}
}

// TestControlGuardedTab: a split of a guarded tab carries the guard, and
// tabNew there is refused; an unguarded tab's split carries nothing.
func TestControlGuardedTab(t *testing.T) {
	f, c := controlServer(t)
	f.set(func(st *model.State) { st.Panes[0].Env = []string{"PITWALL_GUARD=1"} })
	ask(t, c, `{"type":"splitPane","id":"1","tab":"@w1","cmd":["claude"]}`)
	ask(t, c, `{"type":"splitPane","id":"2","tab":"@w2"}`)
	if r := ask(t, c, `{"type":"tabNew","id":"3","pane":"@p1"}`); r["code"] != "refused" {
		t.Errorf("tabNew in a guarded tab: %v", r)
	}
	f.edit(func(f *fakeControl) {
		if fmt.Sprint(f.envs) != "[[PITWALL_GUARD=1] []]" || len(f.reqs) != 2 {
			t.Errorf("split envs %q, requests %v", f.envs, f.reqs)
		}
	})
}

// TestControlWorkerGuard: [agents] guard_workers guards an agent a pane
// starts, not a shell, and not a tab opened from outside a pane.
func TestControlWorkerGuard(t *testing.T) {
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) { f.settings.GuardWorkers = true })
	for _, tc := range []struct{ req, env string }{
		{`{"type":"newTab","id":"1","dir":"/src","cmd":["claude"],"parentPane":"p1"}`, "[PITWALL_GUARD=1]"},
		{`{"type":"newTab","id":"1","dir":"/src","parentPane":"p1"}`, "[]"},
		{`{"type":"newTab","id":"1","dir":"/src","cmd":["claude"],"session":"main"}`, "[]"},
	} {
		ask(t, c, tc.req)
		if env := fmt.Sprint(f.asked().(proto.NewSession).Env); env != tc.env {
			t.Errorf("%s: env %s, want %s", tc.req, env, tc.env)
		}
	}
}

// TestControlPrompt: promptPane types once the agent's turn ended, and
// with wait answers with the reply of the turn it started.
func TestControlPrompt(t *testing.T) {
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) { f.reply = "all green" })
	act := func(state model.AgentState) {
		f.set(func(st *model.State) {
			st.Activities[0].State, st.Activities[0].UpdatedAt = state, time.Now().Add(time.Millisecond)
		})
	}
	onDo := func(req any) {
		if _, typed := req.(proto.Prompt); typed {
			go func() {
				act(model.StateWorking)
				act(model.StateCompleted)
			}()
		}
	}
	f.edit(func(f *fakeControl) { f.onDo = onDo })
	c.line(t, `{"type":"promptPane","id":"1","pane":"@p1","text":"run the tests","wait":true,"timeoutMs":20000}`)
	for start := time.Now(); f.watching() == 0; time.Sleep(time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("promptPane does not watch")
		}
	}
	if f.asked() != nil {
		t.Fatal("typed while the agent's turn ran")
	}
	act(model.StateCompleted) // the last turn ends
	r := c.json(t)
	checkFrame(t, r)
	if d := data(r); d["outcome"] != "reached" || d["reply"] != "all green" || fmt.Sprint(f.asked()) != fmt.Sprint(proto.Prompt{Pane: "p1", Text: "run the tests"}) {
		t.Fatalf("promptPane %v, asked %v", r, f.asked())
	}
	f.edit(func(f *fakeControl) { f.onDo = nil })
	if r := ask(t, c, `{"type":"promptPane","id":"2","pane":"@p1","text":"again"}`); r["ok"] != true || r["data"] != nil {
		t.Errorf("promptPane without wait: %v", r)
	}
	if r := ask(t, c, `{"type":"promptPane","id":"3","pane":"@p2","text":"hi"}`); r["code"] != "invalid" {
		t.Errorf("a shell: %v", r)
	}
	f.set(func(st *model.State) {
		st.Activities[0].State, st.Activities[0].Detail = model.StateAwaitingInput, "which branch?"
	})
	if r := ask(t, c, `{"type":"promptPane","id":"4","pane":"@p1","text":"hi"}`); data(r)["outcome"] != "blocked" || data(r)["question"] != "which branch?" {
		t.Errorf("a blocked agent: %v", r)
	}
	f.set(func(st *model.State) { st.Activities[0].State = model.StateCompleted })
	old := promptStartWait.Swap(int64(10 * time.Millisecond))
	defer promptStartWait.Store(old)
	if r := ask(t, c, `{"type":"promptPane","id":"5","pane":"@p1","text":"hi","wait":true}`); r["code"] != "timeout" {
		t.Errorf("a turn that never starts: %v", r)
	}
}
