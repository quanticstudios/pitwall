package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	chat "github.com/quanticstudios/pitwall/internal/session"
)

// fakeChats is one chat and a session list, as the daemon serves them: it
// numbers deltas, keeps the last ring of them for Read, and pushes each
// batch to its sinks.
type fakeChats struct {
	mu     sync.Mutex
	id     string
	snap   chat.Snapshot
	seq    int64
	recent []chat.Delta
	ring   int
	sinks  map[Sink]bool
	chats  []model.Chat
	lists  map[Sink]bool
	gone   bool
}

func newFakeChats(t *testing.T) *fakeChats {
	f := &fakeChats{id: "chat-1", ring: 1024, sinks: map[Sink]bool{}, lists: map[Sink]bool{}}
	f.push(t, []chat.Delta{{Type: chat.DeltaSnapshot, Snapshot: &chat.Snapshot{
		SessionID: f.id, PaneID: f.id, WorkspaceID: "w", Provider: model.ProviderClaude,
		Content: []chat.Item{}, Turns: []chat.TurnSummary{}, PendingApprovals: []chat.Approval{}, PendingUserInputs: []chat.InputRequest{},
		SessionState: chat.StateReady, PermissionMode: "default", InteractionMode: "build",
	}}})
	return f
}

// say adds text to the reply, as one batch of the deltas Reduce makes.
func (f *fakeChats) say(t *testing.T, text string) {
	f.mu.Lock()
	ds := chat.Reduce(f.snap, chat.Event{Type: chat.EventContent, CreatedAt: chat.Stamp(time.Now()), ItemID: "reply", Kind: chat.KindAssistantText, Text: text})
	f.mu.Unlock()
	f.push(t, ds)
}

// item adds an item of its own with text, which costs Apply no copy of
// the text before it.
func (f *fakeChats) item(t *testing.T, text string) {
	f.mu.Lock()
	ds := chat.Reduce(f.snap, chat.Event{Type: chat.EventContent, CreatedAt: chat.Stamp(time.Now()), ItemID: fmt.Sprint("item-", f.seq), Kind: chat.KindAssistantText, Text: text})
	f.mu.Unlock()
	f.push(t, ds)
}

func (f *fakeChats) push(t *testing.T, ds []chat.Delta) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := proto.ChatDeltas{Chat: f.id, Seq: f.seq}
	for _, d := range ds {
		d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = chat.DeltaVersion, f.id, f.seq+1, f.seq+1, chat.Stamp(time.Now())
		n, err := chat.Apply(&f.snap, f.seq, d)
		if err != nil {
			t.Fatal(err)
		}
		f.snap, f.seq = n, d.Seq
		f.recent = append(f.recent, d)
		b, _ := json.Marshal(d)
		out.Deltas = append(out.Deltas, b)
	}
	for s := range f.sinks {
		s.Deltas(out)
	}
}

func (f *fakeChats) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap.Content[0].Text
}

func (f *fakeChats) Subscribe(id string, s Sink) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id || f.gone {
		return codeErr{"not-found"}
	}
	f.sinks[s] = true
	return nil
}

func (f *fakeChats) Unsubscribe(_ string, s Sink) {
	f.mu.Lock()
	delete(f.sinks, s)
	f.mu.Unlock()
}

func (f *fakeChats) Read(id string, after int64) (proto.ChatDeltas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id || f.gone {
		return proto.ChatDeltas{}, codeErr{"not-found"}
	}
	kept := f.recent[max(0, len(f.recent)-f.ring):]
	first := f.seq - int64(len(kept))
	out := proto.ChatDeltas{Chat: f.id, Seq: f.seq}
	if after > 0 && after >= first && after <= f.seq {
		out.Seq = after
		for _, d := range kept[after-first:] {
			b, _ := json.Marshal(d)
			out.Deltas = append(out.Deltas, b)
		}
		return out, nil
	}
	out.Snapshot, _ = json.Marshal(f.snap)
	return out, nil
}

func (f *fakeChats) remove() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone = true
	for s := range f.sinks {
		s.Removed(f.id)
	}
	f.sinks = map[Sink]bool{}
}

func (f *fakeChats) List() []model.Chat {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.chats)
}

func (f *fakeChats) WatchList(s Sink, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.lists[s] = true
	} else {
		delete(f.lists, s)
	}
}

func (f *fakeChats) Do(m any) any                  { return proto.ChatResult{Code: "unsupported"} }
func (f *fakeChats) Workspaces() []model.Workspace { return nil }
func (f *fakeChats) Archived() []model.Chat        { return nil }
func (f *fakeChats) Import(chat.Config, chat.Snapshot) (bool, error) {
	return false, codeErr{"unsupported"}
}

// setList replaces the list and tells the watchers.
func (f *fakeChats) setList(chats ...model.Chat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats = chats
	for s := range f.lists {
		s.ListChanged()
	}
}

type codeErr struct{ code string }

func (e codeErr) Error() string { return e.code }
func (e codeErr) Code() string  { return e.code }

func streamServer(t *testing.T, f *fakeChats) (*Server, *client) {
	t.Helper()
	s, hs := newServer(t, fixed(goodToken))
	s.Chats = f
	return s, greet(t, hs)
}

func greet(t *testing.T, hs *httptest.Server) *client {
	t.Helper()
	c, _ := dial(t, hs, "", "")
	c.nc.SetDeadline(time.Time{})
	c.send(t, hi(goodToken, Protocol))
	if w := c.json(t); w["type"] != "welcome" {
		t.Fatalf("welcome: %v", w)
	}
	return c
}

// viewer is what a client holds of the chat: aide's applyDelta over the
// frames it reads.
type viewer struct {
	snap *chat.Snapshot
	seq  int64
}

// next reads the next frame, applies it when it is a deltas frame, and
// returns it. Every frame must pass the schema.
func (v *viewer) next(t *testing.T, c *client) map[string]any {
	t.Helper()
	c.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, p := c.recv(t)
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		t.Fatal(err)
	}
	if errs := validate(schemaRoot(t), schemaRoot(t), m, "frame"); len(errs) > 0 {
		t.Fatalf("frame %s: %v", p, errs)
	}
	if m["type"] != "deltas" {
		return m
	}
	var f struct {
		Seq      int64          `json:"seq"`
		Snapshot *chat.Snapshot `json:"snapshot"`
		Deltas   []chat.Delta   `json:"deltas"`
	}
	if err := json.Unmarshal(p, &f); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot != nil {
		v.snap, v.seq = f.Snapshot, f.Seq
	}
	for _, d := range f.Deltas {
		n, err := chat.Apply(v.snap, v.seq, d)
		if err != nil {
			t.Fatalf("apply %d on %d: %v", d.Seq, v.seq, err)
		}
		v.snap, v.seq = &n, d.Seq
	}
	return m
}

// until reads frames until the viewer's seq reaches seq.
func (v *viewer) until(t *testing.T, c *client, seq int64) {
	t.Helper()
	for v.seq < seq {
		v.next(t, c)
	}
}

func (v *viewer) text() string {
	if v.snap == nil || len(v.snap.Content) == 0 {
		return ""
	}
	return v.snap.Content[0].Text
}

var (
	schemaOnce sync.Once
	schemaV    schema
)

// schemaRoot is the web protocol's frame schema.
func schemaRoot(t *testing.T) schema {
	schemaOnce.Do(func() { schemaV = loadSchema(t) })
	return schemaV
}

func expectResult(t *testing.T, v *viewer, c *client, id string, ok bool) map[string]any {
	t.Helper()
	m := v.next(t, c)
	if m["type"] != "result" || m["id"] != id || m["ok"] != ok {
		t.Fatalf("got %v, want result %s ok %t", m, id, ok)
	}
	return m
}

func subscribe(id string, after int64) map[string]any {
	return map[string]any{"type": "subscribe", "id": id, "sessionId": "chat-1", "after": after}
}

// TestSubscribe: a snapshot at its seq, then the deltas; a resubscribe is
// a resync; unsubscribe stops them; a removed chat says so.
func TestSubscribe(t *testing.T) {
	f := newFakeChats(t)
	f.say(t, "hello ")
	_, c := streamServer(t, f)
	var v viewer

	c.send(t, map[string]any{"type": "subscribe", "id": "x", "sessionId": "nope"})
	if m := expectResult(t, &v, c, "x", false); m["code"] != "not-found" {
		t.Fatalf("unknown chat: %v", m)
	}
	c.send(t, subscribe("1", 0))
	expectResult(t, &v, c, "1", true)
	if m := v.next(t, c); m["snapshot"] == nil || v.text() != "hello " {
		t.Fatalf("first frame %v", m)
	}
	f.say(t, "world")
	v.until(t, c, f.seq)
	if v.text() != "hello world" {
		t.Fatalf("text %q", v.text())
	}

	// Subscribing again with the seq held: nothing until the next delta.
	c.send(t, subscribe("2", v.seq))
	expectResult(t, &v, c, "2", true)
	f.say(t, "!")
	if m := v.next(t, c); m["type"] != "deltas" || m["snapshot"] != nil || v.text() != "hello world!" {
		t.Fatalf("after a resubscribe %v, text %q", m, v.text())
	}

	c.send(t, map[string]any{"type": "unsubscribe", "id": "3", "sessionId": "chat-1"})
	expectResult(t, &v, c, "3", true)
	f.say(t, " unseen")
	c.send(t, subscribe("4", v.seq)) // inside the ring: the deltas missed
	expectResult(t, &v, c, "4", true)
	if m := v.next(t, c); m["snapshot"] != nil || v.text() != "hello world! unseen" {
		t.Fatalf("resync inside the ring %v, text %q", m, v.text())
	}

	c.send(t, map[string]any{"type": "unsubscribe", "id": "5", "sessionId": "chat-1"})
	expectResult(t, &v, c, "5", true)
	f.mu.Lock()
	f.ring = 2
	f.mu.Unlock()
	for range 3 {
		f.say(t, ".")
	}
	c.send(t, subscribe("6", v.seq)) // past the ring: a snapshot
	expectResult(t, &v, c, "6", true)
	if m := v.next(t, c); m["snapshot"] == nil || v.text() != "hello world! unseen..." || v.seq != f.seq {
		t.Fatalf("resync past the ring %v, text %q", m, v.text())
	}

	f.remove()
	if m := v.next(t, c); m["type"] != "removed" || m["sessionId"] != "chat-1" {
		t.Fatalf("got %v, want removed", m)
	}
}

// TestResume: a client that drops mid-stream and subscribes again with
// its last seq ends with every word once.
func TestResume(t *testing.T) {
	f := newFakeChats(t)
	_, c := streamServer(t, f)
	var v viewer
	c.send(t, subscribe("1", 0))
	expectResult(t, &v, c, "1", true)
	v.next(t, c)

	var want strings.Builder
	say := func(from, to int) {
		for i := from; i < to; i++ {
			fmt.Fprint(&want, i, " ")
			f.say(t, fmt.Sprint(i, " "))
		}
	}
	say(0, 100)
	v.until(t, c, f.seq-40) // mid-stream, some frames still unread
	c.nc.Close()
	say(100, 200) // while it is away
	time.Sleep(50 * time.Millisecond)

	s, hs := newServer(t, fixed(goodToken))
	s.Chats = f
	c = greet(t, hs)
	c.send(t, subscribe("2", v.seq))
	expectResult(t, &v, c, "2", true)
	say(200, 300)
	v.until(t, c, f.seq)
	if v.text() != want.String() {
		t.Fatalf("text %q\nwant %q", v.text(), want.String())
	}
}

// TestFiveClients: five clients, subscribed at different points, end with
// the same transcript as the chat.
func TestFiveClients(t *testing.T) {
	f := newFakeChats(t)
	s, hs := newServer(t, fixed(goodToken))
	s.Chats = f
	cs := make([]*client, 5)
	vs := make([]viewer, 5)
	for i := range cs {
		cs[i] = greet(t, hs)
		cs[i].send(t, subscribe("1", 0))
		expectResult(t, &vs[i], cs[i], "1", true)
		for j := range 50 {
			f.say(t, fmt.Sprint(i, ".", j, " "))
		}
	}
	want := f.seq
	for i := range vs {
		vs[i].until(t, cs[i], want)
		if g, w := mustJSON(vs[i].snap), mustJSON(f.snap); string(g) != string(w) {
			t.Fatalf("client %d ends with\n%s\nwant\n%s", i, g, w)
		}
	}
}

// TestSlowClient: a client that does not read never makes its queue pass
// the limits; when it reads again it gets a snapshot and then deltas, and
// ends up current.
func TestSlowClient(t *testing.T) {
	defer func(n, b int) { maxQueue, maxQueueBytes = n, b }(maxQueue, maxQueueBytes)
	maxQueue, maxQueueBytes = 32, 256<<10
	f := newFakeChats(t)
	s, c := streamServer(t, f)
	c.send(t, map[string]any{"type": "subscribeSessions", "id": "0"})
	var v viewer
	expectResult(t, &v, c, "0", true)
	if m := v.next(t, c); m["type"] != "sessions" {
		t.Fatalf("got %v, want sessions", m)
	}
	c.send(t, subscribe("1", 0))
	expectResult(t, &v, c, "1", true)
	v.next(t, c)

	conn := onlyConn(s)
	chunk := strings.Repeat("x", 16<<10)
	overflowed := false
	for i := range 1000 { // 16 MiB, past what loopback buffers
		f.item(t, chunk)
		f.setList(model.Chat{ID: "chat-1", Provider: model.ProviderClaude, State: "running", Turns: i % 3})
		conn.mu.Lock()
		held, bytes, q := conn.held, conn.bytes, len(conn.q)
		overflowed = overflowed || slices.ContainsFunc(conn.q, func(o out) bool { return o.base != "" })
		conn.mu.Unlock()
		if held > maxQueue || bytes > maxQueueBytes || q > maxQueue+2 {
			t.Fatalf("queue at %d batches, %d bytes, %d entries", held, bytes, q)
		}
	}
	if !overflowed {
		t.Fatal("the queue never overflowed")
	}
	snapshots := 0
	for v.seq < f.seq {
		if m := v.next(t, c); m["type"] == "deltas" && m["snapshot"] != nil {
			snapshots++
		}
	}
	if g, w := mustJSON(v.snap), mustJSON(f.snap); snapshots == 0 || string(g) != string(w) {
		t.Fatalf("%d snapshots; the client's transcript is %d bytes, the chat's %d", snapshots, len(g), len(w))
	}
}

func onlyConn(s *Server) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		return c
	}
	return nil
}

// TestTooSlow: a client whose writes block past writeTimeout is closed.
func TestTooSlow(t *testing.T) {
	old := writeTimeout
	t.Cleanup(func() { writeTimeout = old }) // after the server's cleanup
	writeTimeout = 100 * time.Millisecond
	f := newFakeChats(t)
	s, c := streamServer(t, f)
	var v viewer
	c.send(t, subscribe("1", 0))
	expectResult(t, &v, c, "1", true)
	chunk := strings.Repeat("x", 64<<10)
	for deadline := time.Now().Add(10 * time.Second); onlyConn(s) != nil; f.item(t, chunk) {
		if time.Now().After(deadline) {
			t.Fatal("a client that does not read is still connected")
		}
	}
}

// TestSessionList: subscribeSessions sends the whole list, then what
// changed; a change of seq alone is not one.
func TestSessionList(t *testing.T) {
	f := newFakeChats(t)
	a := model.Chat{ID: "a", WorkspaceID: "w1", Provider: model.ProviderClaude, State: "ready", Seq: 1}
	b := model.Chat{ID: "b", WorkspaceID: "w2", Provider: model.ProviderCodex, State: "running", Seq: 7}
	f.setList(a, b)
	_, c := streamServer(t, f)
	var v viewer
	c.send(t, map[string]any{"type": "subscribeSessions", "id": "1"})
	expectResult(t, &v, c, "1", true)
	if m := v.next(t, c); m["type"] != "sessions" || len(m["chats"].([]any)) != 2 {
		t.Fatalf("got %v, want the whole list", m)
	}

	a.Seq = 5
	f.setList(a, b) // seq alone
	b.State = "waiting"
	f.setList(a, b)
	m := v.next(t, c)
	if changed := m["changed"].([]any); m["type"] != "sessionsChanged" || len(changed) != 1 || changed[0].(map[string]any)["id"] != "b" || len(m["removed"].([]any)) != 0 {
		t.Fatalf("got %v, want b changed", m)
	}
	f.setList(b)
	if m := v.next(t, c); m["type"] != "sessionsChanged" || len(m["changed"].([]any)) != 0 || m["removed"].([]any)[0] != "a" {
		t.Fatalf("got %v, want a removed", m)
	}

	c.send(t, map[string]any{"type": "unsubscribeSessions", "id": "2"})
	expectResult(t, &v, c, "2", true)
	f.setList()
	c.send(t, map[string]any{"type": "unsubscribe", "id": "3", "sessionId": "chat-1"})
	expectResult(t, &v, c, "3", true) // and no sessionsChanged before it
}

// TestManyImages: a chat holding 30 MB of images, as 10 MB prompts
// (attach.go's cap for one message), reaches a client whole: as the
// subscribe snapshot, as a prompt pushed live past the queue's byte
// limit, and as a read. Outgoing frames have no cap; aide's
// DaemonTransport reads them with the browser's WebSocket (ENG-2936).
func TestManyImages(t *testing.T) {
	f := newFakeChats(t)
	image := strings.Repeat("A", 10<<20*4/3) // 10 MB, as base64
	prompt := func(i int) chat.Item {
		id := fmt.Sprint("prompt-", i)
		return chat.Item{ID: id, Kind: chat.KindUserPrompt, Text: "look", UpdatedAt: chat.Stamp(time.Now()),
			Attachments: []chat.Attachment{{ID: id, Kind: "image", Data: image, MimeType: "image/png", Name: "a.png", SizeBytes: 10 << 20}}}
	}
	withPrompts := func(n int) []chat.Delta {
		s := f.snap
		s.Content = nil
		for i := range n {
			s.Content = append(s.Content, prompt(i))
		}
		return []chat.Delta{{Type: chat.DeltaSnapshot, Snapshot: &s}}
	}
	images := func(s *chat.Snapshot) int {
		n := 0
		for _, it := range s.Content {
			for _, a := range it.Attachments {
				if a.Data == image {
					n++
				}
			}
		}
		return n
	}
	f.push(t, withPrompts(3))
	_, c := streamServer(t, f)
	var v viewer

	c.send(t, subscribe("1", 0))
	expectResult(t, &v, c, "1", true)
	if m := v.next(t, c); m["snapshot"] == nil || images(v.snap) != 3 {
		t.Fatalf("subscribe: %d images, want 3", images(v.snap))
	}
	f.push(t, withPrompts(4))
	v.until(t, c, f.seq)
	if images(v.snap) != 4 {
		t.Fatalf("live: %d images, want 4", images(v.snap))
	}

	c.send(t, map[string]any{"type": "read", "id": "2", "sessionId": "chat-1", "after": 0})
	m := expectResult(t, &v, c, "2", true)
	var r struct {
		Snapshot chat.Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(mustJSON(m["data"]), &r); err != nil || images(&r.Snapshot) != 4 {
		t.Fatalf("read: %d images, %v", images(&r.Snapshot), err)
	}
}
