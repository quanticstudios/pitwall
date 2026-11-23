package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// recSink is a subscriber that keeps what it is pushed.
type recSink struct {
	pushes  chan proto.ChatDeltas
	removed chan string
}

func newRecSink() *recSink {
	return &recSink{pushes: make(chan proto.ChatDeltas, 1024), removed: make(chan string, 8)}
}

func (r *recSink) Deltas(m proto.ChatDeltas) { r.pushes <- m }
func (r *recSink) Removed(id string)         { r.removed <- id }
func (*recSink) ListChanged()                {}

// next is r's next push, failing t after 5 seconds.
func (r *recSink) next(t *testing.T, what string) proto.ChatDeltas {
	t.Helper()
	select {
	case m := <-r.pushes:
		return m
	case <-time.After(5 * time.Second):
		t.Fatalf("no push for %s", what)
	}
	return proto.ChatDeltas{}
}

// claudeLine is a Claude Code transcript line: the user's prompt text.
func claudeLine(uuid, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"` + text + `"}}` + "\n"
}

// streamPane gives remoteDaemon's Claude pane a transcript and returns its
// path.
func streamPane(t *testing.T, d *Daemon) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(claudeLine("p1", "first")), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	return path
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// addedPrompts are the prompts in ds applied to nothing but each other: the
// item.add deltas' texts.
func addedPrompts(t *testing.T, ds [][]byte) []string {
	t.Helper()
	var out []string
	for _, b := range ds {
		var dl session.Delta
		if err := json.Unmarshal(b, &dl); err != nil {
			t.Fatal(err)
		}
		if dl.Item != nil && dl.Item.Kind == session.KindUserPrompt {
			out = append(out, dl.Item.Text)
		}
	}
	return out
}

// TestPaneStreamWakes: each thing that can change a pane chat refreshes
// its stream: a hook, an OSC 7501 report, a queue edit, a change of the
// daemon's state, a screen change, a write to the transcript (inotify on
// Linux) and the slow tick; a line written to the transcript reaches the
// subscriber with no read from it.
func TestPaneStreamWakes(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	oldTick, oldPoll := streamTick.set(time.Hour), streamPoll.set(time.Hour)
	t.Cleanup(func() { streamTick.set(oldTick); streamPoll.set(oldPoll) })
	path := streamPane(t, d)
	sink := newRecSink()
	if err := d.paneSubscribe("a", sink, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.paneUnsubscribe("a", sink) })

	// quiet waits until no refresh came for a while, so the next one is
	// the trigger's.
	quiet := func() int64 {
		n := streamRefreshes.Load()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			time.Sleep(4 * streamGap)
			if m := streamRefreshes.Load(); m == n {
				return n
			} else {
				n = m
			}
		}
		t.Fatal("the stream never went quiet")
		return 0
	}
	wakes := func(what string, trigger func()) {
		t.Helper()
		n := quiet()
		trigger()
		for deadline := time.Now().Add(2 * time.Second); streamRefreshes.Load() == n; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("%s woke no refresh", what)
				return
			}
		}
	}
	ctx := context.Background()
	wakes("a hook", func() {
		d.agentEvent(ctx, proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)})
	})
	wakes("an OSC 7501 report", func() {
		d.programReport("a", progstatus.Report{State: progstatus.Working, Progress: progstatus.NoProgress}, 1, 0)
	})
	wakes("a queue edit", func() {
		d.chatRequest(ctx, proto.ChatQueue{Chat: "a", Action: "remove", ID: "none"})
	})
	wakes("a state change", func() {
		d.mu.Lock()
		d.changed()
		d.mu.Unlock()
	})
	wakes("a screen change", func() {
		d.mu.Lock()
		p := d.panes["a"]
		d.mu.Unlock()
		d.pushFrame("a", p)
	})
	if runtime.GOOS == "linux" {
		for len(sink.pushes) > 0 {
			<-sink.pushes
		}
		wakes("a transcript write", func() { appendLine(t, path, claudeLine("p2", "second")) })
		var got []string
		for !strings.Contains(strings.Join(got, ","), "second") {
			got = append(got, addedPrompts(t, sink.next(t, "the line written").Deltas)...)
		}
	}
	streamTick.set(50 * time.Millisecond)
	streamPoll.set(50 * time.Millisecond)
	d.paneUnsubscribe("a", sink) // the next stream takes the short tick
	sink2 := newRecSink()
	if err := d.paneSubscribe("a", sink2, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.paneUnsubscribe("a", sink2) })
	n := streamRefreshes.Load()
	time.Sleep(400 * time.Millisecond)
	if got := streamRefreshes.Load() - n; got < 3 {
		t.Errorf("the tick refreshed %d times in 400 ms, want several", got)
	}
}

// TestPaneStreamResync: a subscriber that missed deltas, as when the
// transcript was rewritten or the pane started a new session, gets a push
// with none and a later seq, and its read after its last seq is a fresh
// snapshot.
func TestPaneStreamResync(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := streamPane(t, d)
	sink := newRecSink()
	if err := d.paneSubscribe("a", sink, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.paneUnsubscribe("a", sink) })
	hub := chatHub{d}
	base, err := hub.Read("a", 0)
	if err != nil || base.Snapshot == nil {
		t.Fatalf("base: %v %+v", err, base)
	}
	seq := base.Seq
	for len(sink.pushes) > 0 {
		<-sink.pushes
	}
	// Rewritten shorter: the chat reads it again from its start.
	if err := os.WriteFile(path, []byte(claudeLine("q1", "new")), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.pokePane("a")
	d.mu.Unlock()
	for {
		m := sink.next(t, "the rewrite")
		if len(m.Deltas) == 0 && m.Snapshot == nil && m.Seq > seq {
			break
		}
	}
	r, err := hub.Read("a", seq)
	if err != nil || r.Snapshot == nil {
		t.Fatalf("read after %d: %v, snapshot %v", seq, err, r.Snapshot != nil)
	}
	var s session.Snapshot
	json.Unmarshal(r.Snapshot, &s)
	if len(s.Content) == 0 || s.Content[0].Text != "new" {
		t.Fatalf("resynced content %+v", s.Content)
	}

	// A new session in the pane, /clear: another transcript.
	next := filepath.Join(filepath.Dir(path), "t.jsonl")
	os.WriteFile(next, []byte(claudeLine("r1", "cleared")), 0o600)
	d.mu.Lock()
	d.st.Panes[0].Transcript = next
	d.pokePane("a")
	d.mu.Unlock()
	for {
		m := sink.next(t, "the new session")
		if len(m.Deltas) == 0 && m.Snapshot == nil && m.Seq > r.Seq {
			break
		}
	}
}

// TestPaneStreamUnwatch: a stream watches its transcript's folder while it
// has subscribers and drops the watch when the last one leaves; the pane's
// close ends it, telling its subscribers.
func TestPaneStreamUnwatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("folders are watched on Linux only")
	}
	d, _, _ := remoteDaemon(t)
	streamPane(t, d)
	waitWatched := func(what string, ok func(int64) bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(foldersWatched.Load()); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d folders watched", what, foldersWatched.Load())
			}
		}
	}
	before := foldersWatched.Load()
	a, b := newRecSink(), newRecSink()
	d.paneSubscribe("a", a, false)
	d.paneSubscribe("a", b, true)
	waitWatched("subscribed", func(n int64) bool { return n > before })
	d.paneUnsubscribe("a", a)
	time.Sleep(100 * time.Millisecond)
	if foldersWatched.Load() == before {
		t.Fatal("the watch went while a subscriber stayed")
	}
	d.paneUnsubscribe("a", b)
	waitWatched("unsubscribed", func(n int64) bool { return n == before })
	d.mu.Lock()
	gone := d.paneStreams["a"] == nil
	d.mu.Unlock()
	if !gone {
		t.Fatal("the stream outlived its last subscriber")
	}

	// The pane closes: its stream ends and says so.
	c := newRecSink()
	d.paneSubscribe("a", c, false)
	waitWatched("subscribed again", func(n int64) bool { return n > before })
	d.mu.Lock()
	delete(d.panes, "a")
	d.pokePane("a")
	d.mu.Unlock()
	select {
	case id := <-c.removed:
		if id != "a" {
			t.Fatalf("removed %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no removal when the pane closed")
	}
	waitWatched("pane closed", func(n int64) bool { return n == before })
}

// TestPaneChatFolders: a pane chat watches its transcript's folder and
// those of the subagents still running, never more than maxFolders.
func TestPaneChatFolders(t *testing.T) {
	pc := &paneChat{path: "/h/.claude/projects/p/s.jsonl", prov: model.ProviderClaude, agents: map[string]*agentFile{
		"done": {path: "/h/brain/done/logs/t.jsonl", done: true},
		"run":  {path: "/h/brain/run/logs/t.jsonl"},
	}}
	want := []string{"/h/.claude/projects/p", "/h/.claude/projects/p/s/subagents", "/h/brain/run/logs"}
	for i := range want {
		want[i] = filepath.FromSlash(want[i]) // why: folders() goes through filepath, so Windows gets backslashes
	}
	if got := pc.folders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("folders %v, want %v", got, want)
	}
	for i := range 3 * maxFolders {
		pc.agents[string(rune('a'+i%26))+strings.Repeat("x", i)] = &agentFile{path: filepath.Join("/h/brain", strings.Repeat("y", i+1), "t.jsonl")}
	}
	if got := len(pc.folders()); got != maxFolders {
		t.Fatalf("%d folders, want the cap %d", got, maxFolders)
	}
	if (&paneChat{}).folders() != nil {
		t.Fatal("a chat with no transcript watches a folder")
	}
}

// TestPaneChatSameEntries: the phone and the window see the same entries
// of a Claude Code, a Codex and an agy pane, from the one pane chat.
func TestPaneChatSameEntries(t *testing.T) {
	for _, tc := range []struct {
		prov    model.Provider
		fixture string
	}{
		{model.ProviderClaude, "../session/claude/testdata/transcripts/tui-tasks.jsonl"},
		{model.ProviderCodex, "../session/codex/testdata/rollout.jsonl"},
		{model.ProviderAntigravity, "../session/antigravity/testdata/transcript_full.jsonl"},
	} {
		t.Run(string(tc.prov), func(t *testing.T) {
			d, _, _ := remoteDaemon(t)
			b, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			tr := filepath.Join(dir, "t.jsonl")
			os.WriteFile(tr, b, 0o600)
			d.mu.Lock()
			d.st.Panes[2].Provider, d.st.Panes[2].Transcript, d.st.Panes[2].Env = tc.prov, tr, []string{"HOME=" + dir}
			d.mu.Unlock()
			phone, _, ok := d.phoneChatEntries("c")
			if !ok || len(phone) == 0 {
				t.Fatalf("phone: ok %v, %d entries", ok, len(phone))
			}
			snap, _ := paneRead(t, d, "c", session.Snapshot{}, 0)
			window, _ := phoneEntries(snap.Content, "")
			if !reflect.DeepEqual(phone, window) {
				t.Fatalf("phone and window differ:\nphone  %+v\nwindow %+v", phone, window)
			}
			kinds := map[string]bool{}
			for _, e := range phone {
				kinds[e.Kind] = true
			}
			if !kinds[flow.EntryUser] || !kinds[flow.EntryAssistant] {
				t.Errorf("entries of kinds %v, want prompts and replies", kinds)
			}
		})
	}
}

// TestChatSubscribeSocket: a window at proto.SubscribeLevel subscribes to
// a pane chat and gets its base, then pushes as the transcript grows,
// with no read; a window below that level reads as before and is pushed
// nothing.
func TestChatSubscribeSocket(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := streamPane(t, d)
	sock := serveSocket(t, d)
	isChat := func(push bool) func(any) bool {
		return func(m any) bool { r, ok := m.(proto.ChatDeltas); return ok && r.Chat == "a" && r.Push == push }
	}

	old := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.SubscribeLevel - 1, Kind: "gui", Cwd: t.TempDir()})
	old.send(proto.ChatRead{Chat: "a"})
	if r := old.waitFor("an old window's read", isChat(false)).(proto.ChatDeltas); r.Snapshot == nil {
		t.Fatalf("old window's read: %+v", r)
	}

	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	c.send(proto.ChatSubscribe{Chat: "a"})
	base := c.waitFor("the base", isChat(false)).(proto.ChatDeltas)
	var snap session.Snapshot
	if err := json.Unmarshal(base.Snapshot, &snap); err != nil || len(snap.Content) == 0 {
		t.Fatalf("base: %v %+v", err, base)
	}
	seq := base.Seq + int64(len(base.Deltas))
	appendLine(t, path, claudeLine("p2", "pushed"))
	d.mu.Lock()
	d.pokePane("a") // inotify does this on Linux; elsewhere the tick would
	d.mu.Unlock()
	for !strings.Contains(snapText(snap), "pushed") {
		m := c.waitFor("a push", isChat(true)).(proto.ChatDeltas)
		for _, b := range m.Deltas {
			var dl session.Delta
			json.Unmarshal(b, &dl)
			if dl.Seq <= seq {
				continue
			}
			n, err := session.Apply(&snap, seq, dl)
			if err != nil {
				t.Fatalf("push after %d: %v", seq, err)
			}
			snap, seq = n, dl.Seq
		}
	}
	select {
	case m := <-old.in:
		if r, ok := m.(proto.ChatDeltas); ok && r.Push {
			t.Fatalf("pushed to a window below SubscribeLevel: %+v", r)
		}
	case <-time.After(300 * time.Millisecond):
	}
	c.send(proto.ChatSubscribe{Chat: "a", Off: true})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		d.mu.Lock()
		gone := d.paneStreams["a"] == nil
		d.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Off left the stream")
		}
	}
}

func snapText(s session.Snapshot) string {
	var b strings.Builder
	for _, it := range s.Content {
		b.WriteString(it.Text + "\n")
	}
	return b.String()
}

// serveSocket serves d on a socket for the test and returns its path.
func serveSocket(t *testing.T, d *Daemon) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return sock
}

// TestRedactDeltas: what a pane chat sends a browser goes through
// logs.Redact, every string of it, and keeps its numbers.
func TestRedactDeltas(t *testing.T) {
	snap, _ := json.Marshal(session.Snapshot{SessionID: "a", Revision: 1 << 60, Content: []session.Item{{ID: "i", Text: "export API_TOKEN=sk-abcdefghijklmnopqrstuvwxyz0123"}}})
	dl, _ := json.Marshal(session.Delta{Version: session.DeltaVersion, SessionID: "a", Seq: 7, Type: session.DeltaItemTextAppend, Text: "password=hunter22"})
	r := redactDeltas(proto.ChatDeltas{Chat: "a", Snapshot: snap, Deltas: [][]byte{dl}})
	if strings.Contains(string(r.Snapshot), "sk-abcdef") || strings.Contains(string(r.Deltas[0]), "hunter22") {
		t.Fatalf("not redacted: %s %s", r.Snapshot, r.Deltas[0])
	}
	var s session.Snapshot
	var d session.Delta
	if json.Unmarshal(r.Snapshot, &s) != nil || s.Revision != 1<<60 || json.Unmarshal(r.Deltas[0], &d) != nil || d.Seq != 7 {
		t.Fatalf("numbers lost: %s %s", r.Snapshot, r.Deltas[0])
	}
}

// TestWebPaneChat: a web protocol client subscribes to a pane chat by its
// pane's id and gets its base and its pushes, with secrets redacted.
func TestWebPaneChat(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := streamPane(t, d)
	c := dialWeb(t, webOn(t, d), "browser")
	if r := c.call(map[string]any{"type": "subscribe", "sessionId": "a", "after": 0}); r["ok"] != true {
		t.Fatalf("subscribe: %v", r)
	}
	base := c.wait(map[string]any{"type": "deltas", "sessionId": "a"})
	if base["snapshot"] == nil {
		t.Fatalf("base: %v", base)
	}
	appendLine(t, path, claudeLine("p2", "password=hunter22 pushed"))
	d.mu.Lock()
	d.pokePane("a")
	d.mu.Unlock()
	push := c.until("the push", func() map[string]any {
		for _, m := range c.pushed {
			if b, _ := json.Marshal(m); m["type"] == "deltas" && m["snapshot"] == nil && strings.Contains(string(b), "pushed") {
				return m
			}
		}
		return nil
	})
	if b, _ := json.Marshal(push); strings.Contains(string(b), "hunter22") {
		t.Fatalf("a secret reached the browser: %s", b)
	}
	if r := c.call(map[string]any{"type": "unsubscribe", "sessionId": "a"}); r["ok"] != true {
		t.Fatalf("unsubscribe: %v", r)
	}
}
