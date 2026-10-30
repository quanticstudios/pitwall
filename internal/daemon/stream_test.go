package daemon

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
	"github.com/quanticstudios/pitwall/internal/session/fake"
	"github.com/quanticstudios/pitwall/internal/web"
)

// follower is a web.Sink that keeps a chat as a web connection does: a
// base from Read, then the pushed deltas after it.
type follower struct {
	mu      sync.Mutex
	pushed  []proto.ChatDeltas
	removed bool
}

func (f *follower) Deltas(d proto.ChatDeltas) {
	f.mu.Lock()
	f.pushed = append(f.pushed, d)
	f.mu.Unlock()
}
func (f *follower) Removed(string) { f.mu.Lock(); f.removed = true; f.mu.Unlock() }
func (f *follower) ListChanged()   {}

// follow applies the pushes after base and returns the snapshot and how
// many deltas it applied. A gap or a duplicate fails t.
func (f *follower) follow(t *testing.T, base proto.ChatDeltas) (session.Snapshot, int) {
	t.Helper()
	var snap session.Snapshot
	if err := json.Unmarshal(base.Snapshot, &snap); err != nil {
		t.Fatalf("base: %v", err)
	}
	seq, n := base.Seq, 0
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.pushed {
		if b.Seq > seq {
			t.Fatalf("pushed deltas after %d, the follower has %d", b.Seq, seq)
		}
		for i, raw := range b.Deltas {
			var d session.Delta
			if err := json.Unmarshal(raw, &d); err != nil || d.Seq != b.Seq+int64(i)+1 {
				t.Fatalf("delta %d of a push after %d: seq %d, %v", i, b.Seq, d.Seq, err)
			}
			if d.Seq <= seq {
				continue
			}
			var err error
			if snap, err = session.Apply(&snap, seq, d); err != nil {
				t.Fatalf("apply %d: %v", d.Seq, err)
			}
			seq, n = d.Seq, n+1
		}
	}
	return snap, n
}

func mustRead(t *testing.T, h chatHub, id string, after int64) proto.ChatDeltas {
	t.Helper()
	d, err := h.Read(id, after)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func chatProvider(o fake.Options) func(*Options) {
	return func(opt *Options) {
		opt.Providers = map[model.Provider]session.Provider{model.ProviderClaude: fake.New(o)}
	}
}

// waitTurns waits until the chat has n turns, all of them done.
func waitTurns(t *testing.T, h chatHub, id string, n int) {
	t.Helper()
	waitUntil(t, fmt.Sprint(n, " turns done"), func() bool {
		var s session.Snapshot
		json.Unmarshal(mustRead(t, h, id, 0).Snapshot, &s)
		return len(s.Turns) == n && !slices.ContainsFunc(s.Turns, func(t session.TurnSummary) bool { return t.State == session.TurnRunning })
	})
}

// TestChatStream streams a fake provider's turn to five subscribers, two
// of them from the middle of it: each sees every delta once, in order,
// and ends with the chat's own snapshot. Archive tells them.
func TestChatStream(t *testing.T) {
	appendEvent := []session.Event{{Type: session.EventContent, ItemID: "reply", Kind: session.KindAssistantText, Text: "${index} "}}
	script := fake.Script{RuntimeEvents: []fake.Step{
		{Repeat: 200, DelayMS: 2, Events: appendEvent},
		{Repeat: 200, Events: appendEvent}, // a burst, which coalesces
		{Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}},
	}}
	d, sock, stop := runDaemon(t, &fakes{statsCalls: map[string]int{}}, chatProvider(fake.Options{Script: &script}))
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir()})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	h, id := chatHub{d}, r.Chat

	fs := make([]*follower, 5)
	bases := make([]proto.ChatDeltas, 5)
	subscribe := func(i int) {
		fs[i] = &follower{}
		if err := h.Subscribe(id, fs[i]); err != nil {
			t.Fatal(err)
		}
		bases[i] = mustRead(t, h, id, 0)
	}
	for i := range 3 {
		subscribe(i)
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: id, Text: "count"}); r.Code != "" {
		t.Fatalf("send: %+v", r)
	}
	waitUntil(t, "half the text", func() bool {
		var s session.Snapshot
		json.Unmarshal(mustRead(t, h, id, 0).Snapshot, &s)
		return slices.ContainsFunc(s.Content, func(it session.Item) bool { return strings.Contains(it.Text, "100 ") })
	})
	subscribe(3)
	subscribe(4)
	waitTurns(t, h, id, 1)

	want := mustRead(t, h, id, 0) // publishes what the loop held back
	var wantSnap session.Snapshot
	json.Unmarshal(want.Snapshot, &wantSnap)
	var text strings.Builder
	for i := range 400 {
		fmt.Fprint(&text, i%200, " ") // each repeat counts from 0
	}
	if i := slices.IndexFunc(wantSnap.Content, func(it session.Item) bool { return it.Kind == session.KindAssistantText }); i < 0 || wantSnap.Content[i].Text != text.String() {
		t.Fatalf("transcript %+v", wantSnap.Content)
	}
	for i, f := range fs {
		got, n := f.follow(t, bases[i])
		if g, w := mustJSON(t, got), mustJSON(t, wantSnap); g != w {
			t.Fatalf("follower %d ends with\n%s\nwant\n%s", i, g, w)
		}
		if i == 0 && n >= 400 {
			t.Errorf("%d deltas for 400 appends: nothing coalesced", n)
		}
	}

	if r := chatCall[proto.ChatResult](gui, proto.ChatArchive{Chat: id}); r.Code != "" {
		t.Fatalf("archive: %+v", r)
	}
	waitUntil(t, "removed", func() bool {
		return !slices.ContainsFunc(fs, func(f *follower) bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.removed })
	})
	if _, err := h.Read(id, 0); chatCode(err) != "not-found" {
		t.Fatalf("read an archived chat: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestChatResync reads after a seq inside the ring of recent deltas, at
// its edge, past it, at the latest seq and above it.
func TestChatResync(t *testing.T) {
	d, sock, stop := runDaemon(t, &fakes{statsCalls: map[string]int{}}, chatProvider(fake.Options{}))
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "long transcript"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	h := chatHub{d}
	waitTurns(t, h, r.Chat, 1)
	latest := mustRead(t, h, r.Chat, 0).Seq
	if latest < recentDeltas+10 {
		t.Fatalf("only %d deltas", latest)
	}
	for _, c := range []struct {
		after    int64
		snapshot bool
		deltas   int
	}{
		{latest - 10, false, 10},
		{latest - recentDeltas, false, recentDeltas},
		{latest - recentDeltas - 1, true, 0},
		{1, true, 0},
		{latest, false, 0},
		{latest + 1, true, 0},
	} {
		got := mustRead(t, h, r.Chat, c.after)
		if (got.Snapshot != nil) != c.snapshot || len(got.Deltas) != c.deltas {
			t.Errorf("after %d: snapshot %t, %d deltas; want %t, %d", c.after, got.Snapshot != nil, len(got.Deltas), c.snapshot, c.deltas)
		}
		if c.snapshot && got.Seq != latest || !c.snapshot && got.Seq != c.after {
			t.Errorf("after %d: seq %d", c.after, got.Seq)
		}
	}
}

// TestCoalesce merges appends to one item within coalesce into one delta,
// which publish holds back until its time is up or another delta comes.
// The log gets the same deltas, so the chat reloads with all the text.
func TestCoalesce(t *testing.T) {
	dir := t.TempDir()
	l, _, _, _, err := eventlog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &chat{id: "c", flush: time.NewTimer(time.Hour), log: l}
	c.flush.Stop()
	f := &follower{}
	c.subs = map[web.Sink]bool{f: true}
	stamp := session.Stamp(time.Now())
	c.commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &session.Snapshot{SessionID: "c"}}}, stamp)
	text := func(s string) {
		c.reduce(session.Event{Type: session.EventContent, ItemID: "a", Kind: session.KindAssistantText, Text: s})
	}
	types := func() (ts []string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, b := range f.pushed {
			for _, raw := range b.Deltas {
				var d session.Delta
				json.Unmarshal(raw, &d)
				ts = append(ts, fmt.Sprintf("%d %s %s", d.Seq, d.Type, d.Text))
			}
		}
		return ts
	}

	text("a") // item.add
	c.publish()
	n := len(types())
	tail := func(want ...string) {
		t.Helper()
		got := types()
		if len(got) < n+len(want) || !slices.Equal(got[n:n+len(want)], want) || len(want) == 0 && len(got) > n {
			t.Fatalf("published %q, want %q next after the first %d", got, want, n)
		}
		n = len(got)
	}
	text("b") // append
	text("c") // joins it
	c.publish()
	tail()                                                          // held
	c.reduce(session.Event{Type: session.EventError, Message: "x"}) // another delta: the append goes first
	c.publish()
	tail(fmt.Sprint(n+1, " item.text.append bc")) // then the error's deltas

	text("d")
	time.Sleep(2 * coalesce)
	text("e") // too late to join
	c.publish()
	tail(fmt.Sprint(n+1, " item.text.append d"))
	time.Sleep(2 * coalesce)
	c.publish()
	tail(fmt.Sprint(n+1, " item.text.append e"))
	if c.snap.Content[0].Text != "abcde" {
		t.Fatalf("text %q", c.snap.Content[0].Text)
	}
	l.Close()
	l, _, snap, seq, err := eventlog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if seq != c.seq || mustJSON(t, snap) != mustJSON(t, c.snap) {
		t.Fatalf("reloaded at %d, want %d:\n%s\nwant\n%s", seq, c.seq, mustJSON(t, snap), mustJSON(t, c.snap))
	}
}
