package phone

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestParseInvite(t *testing.T) {
	for _, c := range []struct{ link, code, server, want string }{
		{"https://box.example.com/#pair=ABCD2345", "", "https://box.example.com/", "ABCD2345"},
		{"  http://10.0.2.2:7777/#pair=abcd  ", "", "http://10.0.2.2:7777/", "ABCD"},
		{"box.example.com:7777", "ab cd-27", "https://box.example.com:7777/", "ABCD27"},
		{"https://box.example.com/pitwall?x=1", "WXYZ", "https://box.example.com/pitwall/", "WXYZ"},
		{"https://box.example.com/#pair=OLD", "NEW", "https://box.example.com/", "NEW"}, // a typed code wins
	} {
		server, code, err := ParseInvite(c.link, c.code)
		if err != nil || server != c.server || code != c.want {
			t.Errorf("ParseInvite(%q, %q) = %q %q %v; want %q %q", c.link, c.code, server, code, err, c.server, c.want)
		}
	}
	for _, link := range []string{"", "https://box.example.com/", "ftp://box.example.com/#pair=AB"} {
		if _, _, err := ParseInvite(link, ""); err == nil {
			t.Errorf("ParseInvite(%q) took it", link)
		}
	}
}

func TestMachinesFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	if ms, err := LoadMachines(dir); err != nil || ms != nil {
		t.Fatalf("LoadMachines before Save = %v %v", ms, err)
	}
	ms, box := AddMachine(nil, Creds{Server: "https://box.example.com/", Token: "0123456789abcdef.secret", Device: "phone", Pin: "AA:BB"})
	ms, box2 := AddMachine(ms, Creds{Server: "https://box.tail1234.example.com/", Token: "t2"})
	ms, ip := AddMachine(ms, Creds{Server: "http://10.0.2.2:7793/", Token: "t3"})
	if box.Name != "box" || box2.Name != "box-2" || ip.Name != "10.0.2.2" || box.ID == box2.ID {
		t.Fatalf("names %q %q %q, ids %q %q", box.Name, box2.Name, ip.Name, box.ID, box2.ID)
	}
	again, same := AddMachine(ms, Creds{Server: "https://box.example.com/", Token: "new"})
	if len(again) != 3 || same.ID != box.ID || same.Name != "box" || again[0].Token != "new" || ms[0].Token == "new" {
		t.Fatalf("pairing a server again: %+v %+v", again, same)
	}
	if n := UniqueName(ms, "Box", box.ID); n != "Box" {
		t.Fatalf("renaming box to Box = %q", n)
	}
	if err := SaveMachines(dir, ms); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadMachines(dir); err != nil || !slices.Equal(got, ms) {
		t.Fatalf("LoadMachines = %+v %v; want %+v", got, err, ms)
	}
	if fi, err := os.Stat(filepath.Join(dir, machinesFile)); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v; want 0600", fi.Mode(), err)
	}
}

// server is a real phone page with one tab, whose list changes on poke.
type server struct {
	*remote.Server
	mu      sync.Mutex
	tabs    []remote.Tab
	changed chan struct{}
}

func newServer(t *testing.T) (*server, *httptest.Server) {
	s := &server{changed: make(chan struct{}), tabs: []remote.Tab{{Item: remote.Item{Pane: "p1", Tab: "Fix the build", Session: "main", Agent: "claude", State: "working"}, ID: "w1", Send: true}}}
	s.Server = &remote.Server{
		Dir:   t.TempDir(),
		Items: func() []remote.Item { return nil },
		Tabs: func() []remote.Tab {
			s.mu.Lock()
			defer s.mu.Unlock()
			return slices.Clone(s.tabs)
		},
		Changed: func() <-chan struct{} {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.changed
		},
		Send: func(pane, text string) (uint64, error) { return 7, nil },
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func (s *server) poke(tabs []remote.Tab) {
	s.mu.Lock()
	s.tabs = tabs
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func pair(t *testing.T, s *server, url string) Creds {
	t.Helper()
	code, err := remote.Pair(s.Dir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server, code, err := ParseInvite(remote.PairURL(configFor(url), code), "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Pair(context.Background(), server, code, "")
	if err != nil || c.Token == "" || c.Device != "pixel" {
		t.Fatalf("Pair = %+v %v", c, err)
	}
	if _, err := Pair(context.Background(), server, code, ""); err == nil {
		t.Fatal("a used code paired again")
	}
	return c
}

// TestPairStreamSend pairs with the real server, follows the stream as the
// list changes, sends a message, and sees a revoke end the stream.
func TestPairStreamSend(t *testing.T) {
	s, ts := newServer(t)
	c := NewClient(pair(t, s, ts.URL))
	events := make(chan Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Stream(ctx, "", func(e Event) { events <- e }) }()
	next := func() Event {
		select {
		case e := <-events:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return Event{}
	}
	if e := next(); e.Name != "hello" || e.Hello.Device != "pixel" || len(e.Hello.Models) == 0 {
		t.Fatalf("first event %+v", e)
	}
	if e := next(); e.Name != "tabs" || len(e.Tabs) != 1 || e.Tabs[0].Tab != "Fix the build" {
		t.Fatalf("tabs %+v", e)
	}
	s.poke([]remote.Tab{{Item: remote.Item{Pane: "p1", Tab: "Fix the build", Session: "main", Agent: "claude", State: "completed"}, ID: "w1", Send: true}})
	if e := next(); e.Name != "tabs" || e.Tabs[0].State != "completed" {
		t.Fatalf("changed tabs %+v", e)
	}
	if id, err := c.Send(ctx, "p1", "run it again"); err != nil || id != 7 {
		t.Fatalf("Send = %d %v", id, err)
	}
	if _, err := remote.Revoke(s.Dir, "pixel"); err != nil {
		t.Fatal(err)
	}
	s.poke(nil)
	if err := <-done; err == nil {
		t.Fatal("the stream went on after a revoke")
	}
	if _, err := c.Send(ctx, "p1", "again"); !errors.Is(err, ErrUnpaired) {
		t.Fatalf("Send after revoke: %v; want ErrUnpaired", err)
	}
}

// TestPin refuses a certificate no authority signed, names its SHA-256,
// and takes it once pinned.
func TestPin(t *testing.T) {
	s := &remote.Server{Dir: t.TempDir(), Items: func() []remote.Item { return nil }}
	ts := httptest.NewTLSServer(s.Handler())
	defer ts.Close()
	code, err := remote.Pair(s.Dir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Pair(context.Background(), ts.URL+"/", code, "")
	var ue *UntrustedError
	if !errors.As(err, &ue) || ue.Fingerprint != Fingerprint(ts.Certificate().Raw) {
		t.Fatalf("Pair over an unknown certificate: %v", err)
	}
	if _, err := Pair(context.Background(), ts.URL+"/", code, "00:11"); err == nil {
		t.Fatal("a wrong pin paired")
	}
	if c, err := Pair(context.Background(), ts.URL+"/", code, ue.Fingerprint); err != nil || c.Pin != ue.Fingerprint {
		t.Fatalf("Pair with the pin = %+v %v", c, err)
	}
}

func TestBackoff(t *testing.T) {
	b := Backoff{Min: time.Second, Max: 15 * time.Second}
	var got []time.Duration
	for range 6 {
		got = append(got, b.Next())
	}
	want := []time.Duration{1, 2, 4, 8, 15, 15}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(got, want) {
		t.Fatalf("waits %v; want %v", got, want)
	}
	b.Reset()
	if d := b.Next(); d != time.Second {
		t.Fatalf("after Reset %v", d)
	}
}

// TestConnReconnects goes offline while the server fails, online once it
// answers, and to a new chat at once on Watch.
func TestConnReconnects(t *testing.T) {
	s, ts := newServer(t)
	c := NewClient(pair(t, s, ts.URL))
	var fails atomic.Int32
	fails.Store(2)
	panes := make(chan string, 8)
	h := s.Handler()
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			if fails.Add(-1) >= 0 {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			panes <- r.URL.Query().Get("pane")
		}
		h.ServeHTTP(w, r)
	}))
	defer flaky.Close()
	c.Server = flaky.URL + "/"
	status := make(chan bool, 16)
	k := &Conn{Client: c, Wait: Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond},
		On:     func(Event) {},
		Status: func(online bool, err error, _ time.Time) { status <- online }}
	k.Start("")
	defer k.Stop()
	var seen []bool
	for len(seen) < 3 {
		select {
		case on := <-status:
			seen = append(seen, on)
		case <-time.After(5 * time.Second):
			t.Fatalf("states %v", seen)
		}
	}
	if !slices.Equal(seen, []bool{false, false, true}) {
		t.Fatalf("states %v; want offline twice, then online", seen)
	}
	if p := <-panes; p != "" {
		t.Fatalf("first stream for pane %q", p)
	}
	k.Watch("p1")
	select {
	case p := <-panes:
		if p != "p1" {
			t.Fatalf("Watch opened pane %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch opened no stream")
	}
}

func TestChat(t *testing.T) {
	var c Chat
	c.Reset(Entries{Entries: []flow.Entry{
		{Seq: 10, Kind: flow.EntryUser, Text: "fix it"},
		{Seq: 11, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test ./..."},
	}, Model: "claude-opus-5-5", More: true})
	tab := remote.Tab{Item: remote.Item{State: "working"}}
	if w, ok := c.Working(tab); !ok || w != "Working: Bash go test ./..." {
		t.Fatalf("Working = %q %v", w, ok)
	}
	// The call finishes, and a reply comes.
	if !c.Add([]flow.Entry{{Seq: 11, Kind: flow.EntryTool, Tool: "Bash", Arg: "go test ./...", Done: true}, {Seq: 12, Kind: flow.EntryAssistant, Text: "Done."}}) {
		t.Fatal("a new entry was not fresh")
	}
	if len(c.Entries) != 3 || !c.Entries[1].Done {
		t.Fatalf("entries %+v", c.Entries)
	}
	if w, _ := c.Working(tab); w != "Working" {
		t.Fatalf("Working with no call running = %q", w)
	}
	// Older entries go in front, once each.
	if n := c.Older([]flow.Entry{{Seq: 8, Kind: flow.EntryUser, Text: "a"}, {Seq: 9, Kind: flow.EntryAssistant, Text: "b"}, {Seq: 10}}, false); n != 2 || c.First() != 8 || c.More {
		t.Fatalf("Older added %d, first %d, more %v", n, c.First(), c.More)
	}
	if !slices.IsSortedFunc(c.Entries, func(a, b flow.Entry) int { return cmpSeq(a.Seq, b.Seq) }) {
		t.Fatalf("out of order: %+v", c.Entries)
	}

	// A message: queued while the agent works, sent once nothing waits,
	// gone once the transcript shows it.
	now := time.Now()
	p := &Pending{Text: "and  the docs"}
	c.Pending = append(c.Pending, p)
	if p.Label() != "Sending" {
		t.Fatal(p.Label())
	}
	c.Sent(p, 3, remote.Tab{Item: remote.Item{State: "working"}, Queued: 1}, now)
	if p.Label() != "Queued" {
		t.Fatal(p.Label())
	}
	c.Update(remote.Tab{Item: remote.Item{State: "working"}}, now)
	if p.Label() != "Sent" {
		t.Fatal(p.Label())
	}
	c.Add([]flow.Entry{{Seq: 13, Kind: flow.EntryUser, Text: "and the docs"}})
	if len(c.Pending) != 0 {
		t.Fatalf("pending %+v after the transcript showed it", c.Pending)
	}
	// One the agent left before it went in.
	q := &Pending{Text: "later"}
	c.Pending = append(c.Pending, q)
	c.Sent(q, 4, remote.Tab{}, now)
	c.Update(remote.Tab{Dropped: []uint64{4}}, now)
	if q.Label() != "Not sent: the agent exited" {
		t.Fatal(q.Label())
	}
	// Reset keeps the model when the event has none.
	c.Reset(Entries{None: true})
	if len(c.Entries) != 0 || !c.None || ShortModel(c.Model) != "opus-5-5" {
		t.Fatalf("after reset %+v", c)
	}
}

func TestState(t *testing.T) {
	tab := func(id, session, group, state string) remote.Tab {
		return remote.Tab{Item: remote.Item{Pane: id + "-p", Tab: "Tab " + id, Session: session, Agent: "claude", State: state, At: 1}, ID: id, Group: group}
	}
	st := State([]remote.Tab{
		tab("a", "main", "", "working"),
		tab("b", "main", "api", "pending-approval"),
		tab("c", "main", "api", ""),
		tab("d", "main", "", "completed"),
		tab("e", "infra", "", "error"),
	})
	if len(st.Sessions) != 2 || st.Sessions[0].Name != "main" || st.Sessions[1].Name != "infra" {
		t.Fatalf("sessions %+v", st.Sessions)
	}
	var ids []string
	for _, w := range st.Ordered("s:main") {
		ids = append(ids, w.ID)
	}
	if !slices.Equal(ids, []string{"a", "b", "c", "d"}) {
		t.Fatalf("order %v", ids)
	}
	if len(st.Projects) != 1 || st.Projects[0].Name != "api" || st.Projects[0].Kind != model.ProjectGroup {
		t.Fatalf("groups %+v", st.Projects)
	}
	if len(st.Activities) != 4 || len(st.Panes) != 5 {
		t.Fatalf("%d activities, %d panes", len(st.Activities), len(st.Panes))
	}
	if aps := st.AgentPanes(st.Workspaces[0]); len(aps) != 1 || aps[0].Pane.ID != "a-p" {
		t.Fatalf("agent panes of a: %+v", aps)
	}
	if n := NeedsYou(&st, "s:main"); n != 1 {
		t.Fatalf("NeedsYou = %d; want 1, the approval", n)
	}
}

func configFor(url string) config.RemoteSettings { return config.RemoteSettings{URL: url} }

// TestFleet merges two machines' lists, each with a session named main,
// and sends a message about each machine's tab to that machine alone.
func TestFleet(t *testing.T) {
	var ms []Machine
	var got [2]chan string
	for i := range got {
		s, ts := newServer(t)
		got[i] = make(chan string, 4)
		ch := got[i]
		s.Send = func(pane, text string) (uint64, error) { ch <- pane + ": " + text; return 1, nil }
		ms, _ = AddMachine(ms, pair(t, s, ts.URL))
	}
	f := NewFleet(ms)
	tab := func(id string) remote.Tab {
		return remote.Tab{Item: remote.Item{Pane: id + "-p", Tab: "Tab " + id, Session: "main", Agent: "claude", State: "pending-approval"}, ID: id, Group: "api"}
	}
	f.Set(ms[1].ID, []remote.Tab{tab("b1")})
	f.Set(ms[0].ID, []remote.Tab{tab("a1"), tab("a2")})
	st := f.State()
	if len(st.Sessions) != 2 || st.Sessions[0].ID == st.Sessions[1].ID || len(st.Projects) != 2 {
		t.Fatalf("sessions %+v, groups %+v; want one of each per machine", st.Sessions, st.Projects)
	}
	var order []string
	for _, w := range st.Workspaces {
		order = append(order, w.ID)
	}
	if !slices.Equal(order, []string{"a1", "a2", "b1"}) || len(f.Tabs()) != 3 {
		t.Fatalf("tabs in order %v; want the first machine's first", order)
	}
	if n := NeedsYou(&st, st.Sessions[0].ID); n != 2 {
		t.Fatalf("NeedsYou of the first machine's main = %d; want 2", n)
	}
	for _, id := range []string{"b1", "b1-p", st.Sessions[1].ID, st.Projects[1].ID} {
		if m, ok := f.Owner(id); !ok || m.ID != ms[1].ID {
			t.Fatalf("Owner(%q) = %v %v; want the second machine", id, m.Name, ok)
		}
	}
	if _, ok := f.Owner("nope"); ok || f.ClientOf("nope") != nil {
		t.Fatal("an unknown id has an owner")
	}
	for i, pane := range []string{"a2-p", "b1-p"} {
		if _, err := f.ClientOf(pane).Send(context.Background(), pane, "hi"); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-got[i]:
			if m != pane+": hi" {
				t.Fatalf("machine %d got %q", i, m)
			}
		case m := <-got[1-i]:
			t.Fatalf("the message for %s went to the other machine: %q", pane, m)
		case <-time.After(5 * time.Second):
			t.Fatal("no message")
		}
	}
}
