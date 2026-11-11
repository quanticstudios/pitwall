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
	"strings"
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

func TestParseScan(t *testing.T) {
	server, code, err := ParseScan("https://box.example.com/#pair=ABCD2345\n")
	if err != nil || server != "https://box.example.com/" || code != "ABCD2345" {
		t.Errorf("ParseScan = %q %q %v", server, code, err)
	}
	for _, text := range []string{
		"WIFI:T:WPA;S:home;P:secret;;",
		"https://example.com/",
		"https://example.com/?pair=ABCD",
		"box.example.com/#pair=ABCD", // a scan holds the whole link, never a bare host
		"pitwall://box.example.com/#pair=ABCD",
		"hello",
	} {
		if _, _, err := ParseScan(text); !errors.Is(err, ErrNotPairing) {
			t.Errorf("ParseScan(%q) = %v; want ErrNotPairing", text, err)
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
		Dir:     t.TempDir(),
		Version: "v9.9.9",
		Items:   func() []remote.Item { return nil },
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
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A"), 0o600)
	s.Folder = func(tab string) (string, string, bool) { return dir, dir, tab == "w1" }
	h := s.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			// why: Windows' clock can tick as rarely as every 15.6 ms, so a loopback answer measures no latency.
			time.Sleep(20 * time.Millisecond)
		}
		h.ServeHTTP(w, r)
	}))
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
	if e := next(); e.Name != "hello" || e.Hello.Device != "pixel" || e.Hello.Version != "v9.9.9" || e.Hello.Latency < 15*time.Millisecond || len(e.Hello.Models) == 0 || !e.Hello.Files {
		t.Fatalf("first event %+v", e)
	}
	if e := next(); e.Name != "tabs" || len(e.Tabs) != 1 || e.Tabs[0].Tab != "Fix the build" {
		t.Fatalf("tabs %+v", e)
	}
	s.poke([]remote.Tab{{Item: remote.Item{Pane: "p1", Tab: "Fix the build", Session: "main", Agent: "claude", State: "completed"}, ID: "w1", Send: true}})
	if e := next(); e.Name != "tabs" || e.Tabs[0].State != "completed" {
		t.Fatalf("changed tabs %+v", e)
	}
	if b, err := c.File(ctx, "w1", "a.md"); err != nil || string(b) != "# A" {
		t.Fatalf("File = %q %v", b, err)
	}
	if _, err := c.File(ctx, "w1", "../x.md"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("File outside: %v", err)
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
	// A managed chat's tab says so, so its row offers no Allow or Deny.
	chat := tab("f", "main", "", "pending-approval")
	chat.Chat = true
	if w := State([]remote.Tab{chat}).Workspaces[0]; w.Chat != "f-p" {
		t.Fatalf("chat tab's workspace Chat = %q", w.Chat)
	}
}

func configFor(url string) config.RemoteSettings { return config.RemoteSettings{URL: url} }

// TestFleet merges two machines' lists, each with a session named main and
// a tab and pane of the same ids, as a copied state gives, into one
// session with a group per machine, and sends a message about each
// machine's tab to that machine alone.
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
	a, b := ms[0].ID, ms[1].ID
	f.Set(b, []remote.Tab{tab("a1")})
	f.Set(a, []remote.Tab{tab("a1"), tab("a2")})
	st := f.State()
	if len(st.Sessions) != 1 || len(st.Projects) != 2 || st.Projects[0].ID == st.Projects[1].ID {
		t.Fatalf("sessions %+v, groups %+v; want one session and a group per machine", st.Sessions, st.Projects)
	}
	var order, panes []string
	for _, w := range st.Workspaces {
		order = append(order, w.ID)
	}
	for _, act := range st.Activities {
		panes = append(panes, act.PaneID)
	}
	if !slices.Equal(order, []string{NS(a, "a1"), NS(a, "a2"), NS(b, "a1")}) {
		t.Fatalf("tabs in order %v; want the first machine's first, each id with its machine", order)
	}
	if !slices.Equal(panes, []string{NS(a, "a1-p"), NS(a, "a2-p"), NS(b, "a1-p")}) {
		t.Fatalf("activities' panes %v", panes)
	}
	if n := NeedsYou(&st, st.Sessions[0].ID); n != 3 {
		t.Fatalf("NeedsYou of main = %d; want 3, both machines'", n)
	}
	if m, ok := f.Owner(st.Sessions[0].ID); !ok || m.ID != a {
		t.Fatalf("Owner(main) = %v %v; want the first machine", m.Name, ok)
	}
	for _, id := range []string{NS(b, "a1"), NS(b, "a1-p"), st.Projects[1].ID} {
		if m, ok := f.Owner(id); !ok || m.ID != b {
			t.Fatalf("Owner(%q) = %v %v; want the second machine", id, m.Name, ok)
		}
	}
	if _, ok := f.Owner("a1"); ok || f.ClientOf("a1") != nil {
		t.Fatal("an id without its machine has an owner")
	}
	if _, _, ok := f.Tab("nope"); ok {
		t.Fatal("an unknown id has a tab")
	}
	for i, id := range []string{NS(a, "a1-p"), NS(b, "a1")} {
		m, tb, ok := f.Tab(id)
		if !ok || tb.Pane != "a1-p" || tb.ID != "a1" {
			t.Fatalf("Tab(%q) = %+v %v; want the machine's own ids", id, tb, ok)
		}
		if _, err := f.Client(m.ID).Send(context.Background(), tb.Pane, "hi"); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-got[i]:
			if m != "a1-p: hi" {
				t.Fatalf("machine %d got %q", i, m)
			}
		case m := <-got[1-i]:
			t.Fatalf("the message for %s went to the other machine: %q", id, m)
		case <-time.After(5 * time.Second):
			t.Fatal("no message")
		}
	}
}

// TestFleetSessions merges sessions of one name across machines: sessions
// in the order the machines were paired, each merged session's items
// each machine's in turn, MachinesOf in pairing order whatever order the
// lists came in, and a tab's parent hanging it under that machine's tab.
func TestFleetSessions(t *testing.T) {
	ms := []Machine{{ID: "m1", Name: "laptop"}, {ID: "m2", Name: "box"}}
	f := NewFleet(ms)
	tab := func(id, session, group, parent string) remote.Tab {
		return remote.Tab{Item: remote.Item{Pane: id + "-p", Tab: id, Session: session, Agent: "claude"}, ID: id, Group: group, Parent: parent}
	}
	f.Set("m2", []remote.Tab{tab("lead", "infra", "", ""), tab("w1", "infra", "", "lead"), tab("w2", "infra", "", "lead"), tab("x", "main", "", "")})
	f.Set("m1", []remote.Tab{tab("x", "main", "", ""), tab("g", "main", "api", ""), tab("y", "solo", "", "")})
	st := f.State()
	var names []string
	for _, s := range st.Sessions {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"main", "solo", "infra"}) {
		t.Fatalf("sessions %v; want laptop's in its order, then box's new ones", names)
	}
	var order []string
	for _, w := range st.Ordered("s:main") {
		order = append(order, w.ID)
	}
	if !slices.Equal(order, []string{NS("m1", "x"), NS("m1", "g"), NS("m2", "x")}) {
		t.Fatalf("main's tabs %v; want laptop's, then box's", order)
	}
	var on []string
	for _, m := range f.MachinesOf("s:main") {
		on = append(on, m.Name)
	}
	if !slices.Equal(on, []string{"laptop", "box"}) {
		t.Fatalf("MachinesOf(main) = %v", on)
	}
	if got := f.MachinesOf("s:infra"); len(got) != 1 || got[0].ID != "m2" {
		t.Fatalf("MachinesOf(infra) = %+v; want box alone", got)
	}
	kids := st.Workers("s:infra")[NS("m2", "lead")]
	if len(kids) != 2 || kids[0].ID != NS("m2", "w1") || kids[1].ID != NS("m2", "w2") {
		t.Fatalf("lead's workers %+v; want w1 and w2", kids)
	}
	if order := st.Ordered("s:infra"); len(order) != 3 || order[0].ID != NS("m2", "lead") {
		t.Fatalf("infra %+v; want lead with its workers after it", order)
	}
	// A daemon too old to send parents gives flat rows.
	f.Set("m2", []remote.Tab{tab("lead", "infra", "", ""), tab("w1", "infra", "", "")})
	st = f.State()
	if w := st.Workers("s:infra"); len(w) != 0 {
		t.Fatalf("workers without parents: %+v", w)
	}
}

// TestRedirect: a redirect off the paired address, to http on the same
// host or to another host, never carries the token or the body there.
func TestRedirect(t *testing.T) {
	var leaked atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.ContentLength > 0 {
			leaked.Add(1)
		}
		w.Write([]byte(`{"id":1}`))
	}))
	defer plain.Close()
	s := &remote.Server{Dir: t.TempDir(), Items: func() []remote.Item { return nil }}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/send" {
			http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer tls.Close()
	c := NewClient(Creds{Server: tls.URL + "/", Token: "secret", Pin: Fingerprint(tls.Certificate().Raw)})
	if _, err := c.Send(context.Background(), "p1", "the body"); err == nil {
		t.Error("a send followed a redirect to http")
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("the http server got the token or body %d times", n)
	}
	// A redirect on the paired address is fine.
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer same.Close()
	if err := NewClient(Creds{Server: same.URL + "/"}).call(context.Background(), "GET", "old", nil, nil); err != nil {
		t.Fatalf("a redirect on the same address: %v", err)
	}
}

// TestChatReplay: a queued message is not settled by an old prompt of the
// same words that a reconnect replays, only by a later one.
func TestChatReplay(t *testing.T) {
	var c Chat
	c.Reset(Entries{Entries: []flow.Entry{{Seq: 5, Kind: flow.EntryUser, Text: "continue"}}})
	p := c.Send("continue")
	c.Sent(p, 9, remote.Tab{Item: remote.Item{State: "working"}, Queued: 1}, time.Now())
	c.Reset(Entries{Entries: []flow.Entry{{Seq: 5, Kind: flow.EntryUser, Text: "continue"}}})
	if !c.Has(p) {
		t.Fatal("the replayed old prompt settled the queued message")
	}
	c.Update(remote.Tab{Dropped: []uint64{9}}, time.Now())
	if p.Err == "" {
		t.Fatal("a drop after the replay was not reported")
	}
	q := c.Send("continue")
	c.Add([]flow.Entry{{Seq: 6, Kind: flow.EntryUser, Text: "continue"}})
	if c.Has(q) {
		t.Fatal("a new prompt did not settle the message on its way")
	}
}

// TestStateIcon gives a tab with an icon the folder IconDir names, which
// ParseIconDir reads back, and a tab from an old daemon, with none, no
// folder: its row stays as it was.
func TestStateIcon(t *testing.T) {
	st := stateOf("m/1", []remote.Tab{
		{Item: remote.Item{Pane: "p1", Tab: "a", Session: "main"}, ID: "w1", Repo: "api", Icon: "abc"},
		{Item: remote.Item{Pane: "p2", Tab: "b", Session: "main"}, ID: "w2", Repo: "api"},
	})
	dir := st.LivePath(st.Workspaces[0])
	if m, hash, ok := ParseIconDir(dir); !ok || m != "m/1" || hash != "abc" || filepath.Base(dir) != "api" || st.Workspaces[0].RepoRoot != dir {
		t.Fatalf("tab with an icon: folder %q (%q %q %v), repo root %q", dir, m, hash, ok, st.Workspaces[0].RepoRoot)
	}
	if dir := st.LivePath(st.Workspaces[1]); dir != "" || st.Workspaces[1].RepoRoot != "" {
		t.Fatalf("tab from an old daemon: folder %q, repo root %q; want none", dir, st.Workspaces[1].RepoRoot)
	}
	if _, _, ok := ParseIconDir("/srv/src/api"); ok {
		t.Fatal("a real folder parsed as an icon folder")
	}
}

func TestCapturedQueuedPromptSettlesPending(t *testing.T) {
	log := flow.NewLog(model.ProviderClaude, "../flow/testdata/claude/queued-command.jsonl")
	log.Poll()
	c := Chat{}
	prompt := "Also run Bash exactly as echo queued-qa after the sleep. Keep this message in the conversation."
	c.Send(prompt)
	c.Add(log.Last(100))
	if len(c.Pending) != 0 {
		t.Fatalf("pending: %#v", c.Pending)
	}
}

func TestExitedTabState(t *testing.T) {
	st := State([]remote.Tab{{Item: remote.Item{Pane: "p", Agent: "claude", State: "exited", Label: "Exited 1", Session: "s"}, ID: "w", Exited: true, ExitCode: 1}})
	if len(st.Panes) != 1 || !st.Panes[0].Exited || st.Panes[0].ExitCode != 1 {
		t.Fatalf("panes: %#v", st.Panes)
	}
}
