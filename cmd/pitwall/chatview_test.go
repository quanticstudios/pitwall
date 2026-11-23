//go:build !nogui

package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// The chat view's timers stay off in tests, which read the backend
// unlocked and call chatRead themselves.
func init() { chatAfter = func(time.Duration, func()) {} }

// TestChatCache: a read's snapshot and the deltas after it build the
// transcript; another chat's reply, an empty one and a gap change
// nothing, and a gap makes the next read ask for a snapshot.
func TestChatCache(t *testing.T) {
	snap, _ := json.Marshal(session.Snapshot{SessionID: "c", Content: []session.Item{}})
	add := func(seq int64, id string) []byte {
		b, _ := json.Marshal(session.Delta{Version: session.DeltaVersion, SessionID: "c", Seq: seq, Revision: seq, Type: session.DeltaItemAdd,
			Item: &session.Item{ID: id, Kind: session.KindAssistantText, Text: id}})
		return b
	}
	c := chatCache{id: "c", out: true}
	if c.take(proto.ChatDeltas{Chat: "other", Snapshot: snap, Seq: 1}) || c.snap != nil || !c.out {
		t.Fatal("took another chat's reply")
	}
	if !c.take(proto.ChatDeltas{Chat: "c", Snapshot: snap, Seq: 1, Deltas: [][]byte{add(2, "a")}}) || c.seq != 2 || c.out {
		t.Fatalf("first read: seq %d, out %v", c.seq, c.out)
	}
	if c.take(proto.ChatDeltas{Chat: "c", Seq: 2}) {
		t.Fatal("an empty read changed the transcript")
	}
	if !c.take(proto.ChatDeltas{Chat: "c", Seq: 2, Deltas: [][]byte{add(3, "b"), add(4, "c")}}) || c.seq != 4 || len(c.snap.Content) != 3 {
		t.Fatalf("deltas: seq %d, %d items", c.seq, len(c.snap.Content))
	}
	if c.take(proto.ChatDeltas{Chat: "c", Seq: 4, Deltas: [][]byte{add(6, "gap")}}) || c.seq != 0 || len(c.snap.Content) != 3 {
		t.Fatalf("gap: seq %d, %d items", c.seq, len(c.snap.Content))
	}
}

// TestChatResults: each reply answers the chat's oldest request, so a
// refusal names the request it refuses, a ChatSend's success is kept, and
// other successes are dropped.
func TestChatResults(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	set, send, rm := proto.ChatSet{Chat: "c", Mode: "plan"}, proto.ChatSend{Chat: "c", Text: "hi"}, proto.ChatQueue{Chat: "c", Action: "remove", ID: "q1"}
	for _, m := range []any{proto.ChatRead{Chat: "c"}, set, send, proto.ChatInterrupt{Chat: "c"}, rm, proto.ChatNew{}} {
		if id, ok := chatOf(m); ok {
			b.chatReq[id] = append(b.chatReq[id], m)
		}
	}
	b.chatReply("c") // the read's ChatDeltas
	b.chatAnswered(proto.ChatResult{Chat: "c", Code: "unsupported", Message: "no plan mode"})
	b.chatAnswered(proto.ChatResult{Chat: "c"})
	b.chatAnswered(proto.ChatResult{Chat: "c"})
	b.chatAnswered(proto.ChatResult{Chat: "c", Code: "invalid", Message: "no queued message q1"})
	want := []chatin.Result{{Req: set, Message: "no plan mode"}, {Req: send}, {Req: rm, Message: "no queued message q1"}}
	if got := b.ChatResults("c"); !reflect.DeepEqual(got, want) {
		t.Fatalf("results %+v, want %+v", got, want)
	}
	if b.ChatResults("c") != nil || len(b.chatReq) != 0 {
		t.Fatalf("left %v and %v", b.chatOut, b.chatReq)
	}
}

// TestChatCachePages: a snapshot with a cursor reads the page before it
// once, the page goes before its items, a page asked of an older snapshot
// is dropped, an item read whole stands in for its cut copy, a snapshot
// with another cursor drops the pages, and a refused first read or one
// with no reply says why and asks again on Try again.
func TestChatCachePages(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b.link.Level = proto.Level
	snap := func(ids ...string) []byte {
		s := session.Snapshot{SessionID: "c", Content: []session.Item{}, Turns: []session.TurnSummary{{ID: "turn-" + ids[0]}}}
		for _, id := range ids {
			s.Content = append(s.Content, session.Item{ID: id, Cut: id == "b", Text: id + " cut"})
		}
		out, _ := json.Marshal(s)
		return out
	}
	ids := func(s session.Snapshot) (out []string) {
		for _, it := range s.Content {
			out = append(out, it.ID+"="+it.Text)
		}
		return out
	}
	b.Chat("c")
	b.chats["c"].take(proto.ChatDeltas{Chat: "c", Snapshot: snap("c", "d"), Seq: 1, Older: 40})
	if !b.ChatOlder("c") || !b.ChatOlder("c") {
		t.Fatal("no older history")
	}
	reads := 0
	for _, m := range b.out {
		if r, ok := m.(proto.ChatRead); ok && r.Before == 40 {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("%d page reads, want 1: %+v", reads, b.out)
	}
	if b.chats["c"].take(proto.ChatDeltas{Chat: "c", Snapshot: snap("x"), Page: 99, Older: 1}) {
		t.Fatal("took a page of another snapshot")
	}
	b.chats["c"].take(proto.ChatDeltas{Chat: "c", Snapshot: snap("a", "b"), Page: 40, Older: 0})
	s, _ := b.Chat("c")
	if got := ids(s); !reflect.DeepEqual(got, []string{"a=a cut", "b=b cut", "c=c cut", "d=d cut"}) || len(s.Turns) != 2 || b.ChatOlder("c") {
		t.Fatalf("after the page: %v, %d turns, more %v", got, len(s.Turns), b.chats["c"].more)
	}
	whole, _ := json.Marshal(session.Item{ID: "b", Text: "b whole"})
	b.ChatItem("c", "b")
	b.ChatItem("c", "b")
	b.chats["c"].take(proto.ChatDeltas{Chat: "c", Item: whole})
	if s, _ := b.Chat("c"); ids(s)[1] != "b=b whole" || len(b.chatReq["c"]) > 4 {
		t.Fatalf("whole item: %v, %d reads out", ids(s), len(b.chatReq["c"]))
	}
	b.chats["c"].take(proto.ChatDeltas{Chat: "c", Snapshot: snap("e"), Seq: 9, Older: 70})
	if s, _ := b.Chat("c"); len(s.Content) != 1 || b.chats["c"].more != 70 {
		t.Fatalf("new cursor: %v, more %d", ids(s), b.chats["c"].more)
	}

	b2 := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b2.link.Level = proto.Level
	b2.Chat("x")
	b2.chatAnswered(proto.ChatResult{Chat: "x", Code: "provider", Message: "transcript: permission denied"})
	if _, why := b2.ChatWait("x"); why != "transcript: permission denied" || len(b2.ChatResults("x")) != 0 {
		t.Fatalf("refused read: %q, results %v", why, b2.chatOut)
	}
	b2.ChatRetry("x")
	b2.chats["x"].at = time.Time{}
	b2.Chat("x")
	if !b2.chats["x"].out {
		t.Fatal("Try again sent no read")
	}
	b2.chats["x"].at = time.Now().Add(-readWait)
	b2.Chat("x")
	if since, why := b2.ChatWait("x"); why == "" || since.IsZero() {
		t.Fatalf("no reply in %v: %q", readWait, why)
	}
}

// TestChatReadPace: against a daemon that pushes no deltas, a chat whose
// turn runs is read again chatStream after the last read, an idle one
// only after app.ChatPoll; no read goes out while one is out, for another
// chat, or once the window stopped asking for it.
func TestChatReadPace(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}, outWake: make(chan struct{}, 1)}
	b.link.Level = proto.SubscribeLevel - 1
	reads := func() int {
		n := 0
		for _, m := range b.out {
			if _, ok := m.(proto.ChatRead); ok {
				n++
			}
		}
		b.out = nil
		return n
	}
	busy := &session.Snapshot{SessionID: "c", SessionState: session.StateRunning}
	idle := &session.Snapshot{SessionID: "c", SessionState: session.StateReady}
	now := time.Now()
	for _, tc := range []struct {
		what  string
		snap  *session.Snapshot
		id    string
		ago   time.Duration // since the last read went out
		shown time.Duration // since the window asked
		out   bool
		want  int
	}{
		{"streaming, due", busy, "c", chatStream, 0, false, 1},
		{"streaming, read out", busy, "c", chatStream, 0, true, 0},
		{"streaming, too soon", busy, "c", chatStream / 4, 0, false, 0},
		{"idle, a frame later", idle, "c", chatStream, 0, false, 0},
		{"idle, due", idle, "c", app.ChatPoll, 0, false, 1},
		{"another chat", busy, "x", time.Second, 0, false, 0},
		{"no longer shown", busy, "c", time.Second, 2 * time.Second, false, 0},
	} {
		b.chats = map[string]*chatCache{"c": {id: "c", snap: tc.snap, out: tc.out, at: now.Add(-tc.ago), shown: now.Add(-tc.shown)}}
		b.chatRead(tc.id)
		if got := reads(); got != tc.want {
			t.Errorf("%s: %d reads, want %d", tc.what, got, tc.want)
		}
	}

	// Without a frame, the reply schedules the next read: every reply
	// while the turn runs, and one more after a reply that changed an
	// idle chat, such as the one that ends the turn.
	for _, tc := range []struct {
		what    string
		snap    *session.Snapshot
		out     bool
		changed bool
		after   time.Duration
		ok      bool
	}{
		{"streaming", busy, false, false, chatStream, true},
		{"read out", busy, true, true, 0, false},
		{"idle, changed", idle, false, true, app.ChatPoll, true},
		{"idle, unchanged", idle, false, false, app.ChatPoll, false},
	} {
		b.chats = map[string]*chatCache{"c": {id: "c", snap: tc.snap, out: tc.out}}
		if after, ok := b.chats["c"].again("c", tc.changed); after != tc.after && tc.ok || ok != tc.ok {
			t.Errorf("%s: again %v %v, want %v %v", tc.what, after, ok, tc.after, tc.ok)
		}
	}
	if _, ok := b.chats["c"].again("x", true); ok {
		t.Error("read again for another chat")
	}
}

// TestChatCacheSideBySide: two chats drawn in one frame, as two pane
// chats of a split, each keep their transcript and read a snapshot once,
// by subscribing or, from an older daemon, by ChatRead; a chat new to the
// window drops the ones no longer shown, and their subscriptions.
func TestChatCacheSideBySide(t *testing.T) {
	for _, level := range []int{proto.SubscribeLevel - 1, proto.Level} {
		b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
		b.link.Level = level
		snaps := 0
		for range 3 {
			b.Chat("a")
			b.Chat("b")
			for _, m := range b.out {
				id := ""
				switch r := m.(type) {
				case proto.ChatRead:
					if r.After == 0 {
						id = r.Chat
					}
				case proto.ChatSubscribe:
					id = r.Chat
				}
				if id != "" {
					snaps++
					s, _ := json.Marshal(session.Snapshot{SessionID: id, Content: []session.Item{{ID: id + "1"}}})
					b.chatReply(id)
					b.chatTook(proto.ChatDeltas{Chat: id, Snapshot: s, Seq: 1})
				}
			}
			b.out = nil
		}
		for _, id := range []string{"a", "b"} {
			if s, ok := b.Chat(id); !ok || len(s.Content) != 1 || s.Content[0].ID != id+"1" {
				t.Fatalf("level %d, chat %s: %v %+v", level, id, ok, s.Content)
			}
		}
		if snaps != 2 {
			t.Fatalf("level %d: %d snapshot reads, want one per chat", level, snaps)
		}
		b.out = nil
		b.chats["b"].shown = time.Now().Add(-2 * chatShown)
		b.Chat("c")
		if _, kept := b.chats["b"]; kept || b.chats["a"] == nil {
			t.Fatalf("level %d: kept %v", level, b.chats)
		}
		off := slices.ContainsFunc(b.out, func(m any) bool { s, ok := m.(proto.ChatSubscribe); return ok && s.Chat == "b" && s.Off })
		if want := level >= proto.SubscribeLevel; off != want {
			t.Fatalf("level %d: unsubscribed b %v, want %v (%v)", level, off, want, b.out)
		}
	}
}

// TestChatItemUpdated: an item read whole that grows after, as a long
// reply streams or a big Write lands, is read whole again at its new
// UpdatedAt instead of going back to its cut copy for good.
func TestChatItemUpdated(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b.link.Level = proto.Level
	b.Chat("c")
	s, _ := json.Marshal(session.Snapshot{SessionID: "c", Content: []session.Item{{ID: "r", Kind: session.KindAssistantText, Text: "cut", Cut: true, UpdatedAt: "1"}}})
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: s, Seq: 1})
	itemReads := func() (n int) {
		for _, m := range b.out {
			if r, ok := m.(proto.ChatRead); ok && r.Item == "r" {
				n++
			}
		}
		b.out = nil
		return n
	}
	whole := func(text, at string) {
		w, _ := json.Marshal(session.Item{ID: "r", Kind: session.KindAssistantText, Text: text, UpdatedAt: at})
		b.chatTook(proto.ChatDeltas{Chat: "c", Item: w})
	}
	b.ChatItem("c", "r")
	b.ChatItem("c", "r")
	whole("whole", "1")
	if s, _ := b.Chat("c"); s.Content[0].Text != "whole" || itemReads() != 1 {
		t.Fatalf("first read: %q", s.Content[0].Text)
	}
	d, _ := json.Marshal(session.Delta{Version: session.DeltaVersion, SessionID: "c", Seq: 2, Revision: 2, Type: session.DeltaItemTextAppend,
		ItemID: "r", Text: " more", ItemUpdatedAt: "2"})
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 1, Deltas: [][]byte{d}})
	b.ChatItem("c", "r")
	b.ChatItem("c", "r")
	if n := itemReads(); n != 1 {
		t.Fatalf("%d reads of the grown item, want 1", n)
	}
	whole("whole more", "2")
	if s, _ := b.Chat("c"); s.Content[0].Text != "whole more" {
		t.Fatalf("after it grew: %q", s.Content[0].Text)
	}
}

// TestChatRewindDelta: a rewind's snapshot delta holds the whole chat, so
// the pages read before it go and no item shows twice, with all pages
// read or some; when it does not line up with them, the next read asks
// for a snapshot, whose cursor pages on.
func TestChatRewindDelta(t *testing.T) {
	items := func(ids ...string) []session.Item {
		out := []session.Item{}
		for _, id := range ids {
			out = append(out, session.Item{ID: id, Kind: session.KindAssistantText, Text: id})
		}
		return out
	}
	wire := func(ids ...string) []byte {
		b, _ := json.Marshal(session.Snapshot{SessionID: "c", Content: items(ids...)})
		return b
	}
	for _, tc := range []struct {
		what   string
		page   []string
		older  int64 // the page's cursor
		reread bool
	}{
		{"every page read", []string{"a", "b"}, 0, false},
		{"some pages read", []string{"b"}, 1, true},
	} {
		b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
		b.link.Level = proto.Level
		b.Chat("c")
		b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: wire("c", "d"), Seq: 1, Older: 2})
		b.ChatOlder("c")
		b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: wire(tc.page...), Page: 2, Older: tc.older})
		rewound := session.Snapshot{SessionID: "c", Content: items("a", "b", "c")}
		d, _ := json.Marshal(session.Delta{Version: session.DeltaVersion, SessionID: "c", Seq: 2, Revision: 2, Type: session.DeltaSnapshot, Snapshot: &rewound})
		b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 1, Deltas: [][]byte{d}})
		s, _ := b.Chat("c")
		var got []string
		for _, it := range s.Content {
			got = append(got, it.ID)
		}
		if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Errorf("%s: %v", tc.what, got)
		}
		if b.ChatOlder("c") {
			t.Errorf("%s: pages on before the whole chat", tc.what)
		}
		if reread := b.chats["c"].seq == 0; reread != tc.reread {
			t.Errorf("%s: asks for a snapshot %v, want %v", tc.what, reread, tc.reread)
		}
	}
}

// TestChatSnapshotKeepsPages: a managed chat's cursor is an index that
// moves as items come, so a snapshot after a gap comes with another one;
// the pages read stay when its first item lines up with them, and the
// view keeps its history.
func TestChatSnapshotKeepsPages(t *testing.T) {
	wire := func(ids ...string) []byte {
		s := session.Snapshot{SessionID: "c", Content: []session.Item{}}
		for _, id := range ids {
			s.Content = append(s.Content, session.Item{ID: id, Kind: session.KindAssistantText, Text: id})
		}
		b, _ := json.Marshal(s)
		return b
	}
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b.link.Level = proto.Level
	b.Chat("c")
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: wire("c", "d"), Seq: 1, Older: 2})
	b.ChatOlder("c")
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: wire("a", "b"), Page: 2, Older: 0})
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: wire("d", "e"), Seq: 9, Older: 3})
	s, _ := b.Chat("c")
	var got []string
	for _, it := range s.Content {
		got = append(got, it.ID)
	}
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e"}) || b.ChatOlder("c") {
		t.Fatalf("after the snapshot: %v, more %d", got, b.chats["c"].more)
	}
}

// TestChatSubscribe: a daemon at proto.SubscribeLevel gets one
// ChatSubscribe and no polling reads; its base and pushes build the
// transcript, a push the base already holds is skipped, and a push past
// it, as a gap or a new session, reads a snapshot again once.
func TestChatSubscribe(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}, outWake: make(chan struct{}, 1)}
	b.link.Level = proto.Level
	sent := func() (subs, reads int) {
		for _, m := range b.out {
			switch m.(type) {
			case proto.ChatSubscribe:
				subs++
			case proto.ChatRead:
				reads++
			}
		}
		b.out = nil
		return subs, reads
	}
	delta := func(seq int64, text string) []byte {
		raw, _ := json.Marshal(session.Delta{Version: session.DeltaVersion, Type: session.DeltaItemTextAppend, SessionID: "c", ItemID: "r", Seq: seq, Text: text})
		return raw
	}
	b.Chat("c")
	if subs, reads := sent(); subs != 1 || reads != 0 {
		t.Fatalf("first draw: %d subscribes, %d reads", subs, reads)
	}
	s, _ := json.Marshal(session.Snapshot{SessionID: "c", Content: []session.Item{{ID: "r", Kind: session.KindAssistantText, Text: "a"}}})
	b.chatReply("c")
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: s, Seq: 4})
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 3, Push: true, Deltas: [][]byte{delta(4, "old"), delta(5, "b")}})
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 5, Push: true, Deltas: [][]byte{delta(6, "c")}})
	if got, _ := b.Chat("c"); got.Content[0].Text != "abc" {
		t.Fatalf("pushed text %q, want abc", got.Content[0].Text)
	}
	for range 3 {
		b.chats["c"].at = time.Time{}
		b.Chat("c")
	}
	if subs, reads := sent(); subs != 0 || reads != 0 {
		t.Fatalf("subscribed: %d subscribes, %d reads", subs, reads)
	}
	if after, ok := b.chats["c"].again("c", true); ok {
		t.Fatalf("subscribed chat polls again in %v", after)
	}

	// A push past the transcript: it waits for a snapshot and reads one.
	b.chats["c"].at = time.Now() // the read the gap schedules goes from here
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 9, Push: true})
	if _, ok := b.chats["c"].again("c", false); !ok {
		t.Fatal("a gap schedules no read")
	}
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 9, Push: true, Deltas: [][]byte{delta(10, "x")}})
	if got := b.chats["c"].merged(); got.Content[0].Text != "abc" {
		t.Fatalf("after a gap the text is %q, want abc until the snapshot", got.Content[0].Text)
	}
	b.chats["c"].at = time.Time{}
	b.chatRead("c")
	var read proto.ChatRead
	for _, m := range b.out {
		if r, ok := m.(proto.ChatRead); ok {
			read = r
		}
	}
	b.out = nil
	if read.Chat != "c" || read.After != 0 {
		t.Fatalf("resync read %+v, want a snapshot of c", read)
	}
	s, _ = json.Marshal(session.Snapshot{SessionID: "c", Content: []session.Item{{ID: "r", Kind: session.KindAssistantText, Text: "new"}}})
	b.chatReply("c")
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: s, Seq: 10})
	b.chatTook(proto.ChatDeltas{Chat: "c", Seq: 10, Push: true, Deltas: [][]byte{delta(11, "!")}})
	if got, _ := b.Chat("c"); got.Content[0].Text != "new!" {
		t.Fatalf("after the resync the text is %q, want new!", got.Content[0].Text)
	}
}

// TestChatItemGone: an item read whole that the daemon answers not-found
// for stands as its cut copy, no longer cut, so the window says its image
// is gone instead of waiting; the subscription stays.
func TestChatItemGone(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b.link.Level = proto.Level
	b.Chat("c")
	b.chatRead("c")
	s, _ := json.Marshal(session.Snapshot{SessionID: "c", Content: []session.Item{{ID: "p", Kind: session.KindUserPrompt, Cut: true, UpdatedAt: "1",
		Attachments: []session.Attachment{{ID: "image-1", Kind: "image", Name: "image-1.png"}}}}})
	b.mu.Lock()
	b.chatReply("c")
	b.chatTook(proto.ChatDeltas{Chat: "c", Snapshot: s, Seq: 1})
	b.mu.Unlock()
	b.ChatItem("c", "p")
	b.mu.Lock()
	b.chatAnswered(proto.ChatResult{Chat: "c", Code: "not-found", Message: "item p is gone from the chat"})
	sub := b.chats["c"].sub
	b.mu.Unlock()
	got, _ := b.Chat("c")
	if got.Content[0].Cut || !sub || len(b.ChatResults("c")) != 0 {
		t.Fatalf("after not-found: cut %v, subscribed %v", got.Content[0].Cut, sub)
	}
}
