//go:build !nogui

package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

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
	b.chat.take(proto.ChatDeltas{Chat: "c", Snapshot: snap("c", "d"), Seq: 1, Older: 40})
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
	if b.chat.take(proto.ChatDeltas{Chat: "c", Snapshot: snap("x"), Page: 99, Older: 1}) {
		t.Fatal("took a page of another snapshot")
	}
	b.chat.take(proto.ChatDeltas{Chat: "c", Snapshot: snap("a", "b"), Page: 40, Older: 0})
	s, _ := b.Chat("c")
	if got := ids(s); !reflect.DeepEqual(got, []string{"a=a cut", "b=b cut", "c=c cut", "d=d cut"}) || len(s.Turns) != 2 || b.ChatOlder("c") {
		t.Fatalf("after the page: %v, %d turns, more %v", got, len(s.Turns), b.chat.more)
	}
	whole, _ := json.Marshal(session.Item{ID: "b", Text: "b whole"})
	b.ChatItem("c", "b")
	b.ChatItem("c", "b")
	b.chat.take(proto.ChatDeltas{Chat: "c", Item: whole})
	if s, _ := b.Chat("c"); ids(s)[1] != "b=b whole" || len(b.chatReq["c"]) > 4 {
		t.Fatalf("whole item: %v, %d reads out", ids(s), len(b.chatReq["c"]))
	}
	b.chat.take(proto.ChatDeltas{Chat: "c", Snapshot: snap("e"), Seq: 9, Older: 70})
	if s, _ := b.Chat("c"); len(s.Content) != 1 || b.chat.more != 70 {
		t.Fatalf("new cursor: %v, more %d", ids(s), b.chat.more)
	}

	b2 := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}}
	b2.link.Level = proto.Level
	b2.Chat("x")
	b2.chatAnswered(proto.ChatResult{Chat: "x", Code: "provider", Message: "transcript: permission denied"})
	if _, why := b2.ChatWait("x"); why != "transcript: permission denied" || len(b2.ChatResults("x")) != 0 {
		t.Fatalf("refused read: %q, results %v", why, b2.chatOut)
	}
	b2.ChatRetry("x")
	b2.chat.at = time.Time{}
	b2.Chat("x")
	if !b2.chat.out {
		t.Fatal("Try again sent no read")
	}
	b2.chat.at = time.Now().Add(-readWait)
	b2.Chat("x")
	if since, why := b2.ChatWait("x"); why == "" || since.IsZero() {
		t.Fatalf("no reply in %v: %q", readWait, why)
	}
}

// TestChatReadPace: a chat whose turn runs is read again chatStream
// after the last read, an idle one only after app.ChatPoll; no read goes
// out while one is out, for another chat, or once the window stopped
// asking for it.
func TestChatReadPace(t *testing.T) {
	b := &backend{chatReq: map[string][]any{}, chatOut: map[string][]chatin.Result{}, outWake: make(chan struct{}, 1)}
	b.link.Level = proto.Level
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
		b.chat = chatCache{id: "c", snap: tc.snap, out: tc.out, at: now.Add(-tc.ago), shown: now.Add(-tc.shown)}
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
		b.chat = chatCache{id: "c", snap: tc.snap, out: tc.out}
		if after, ok := b.chat.again("c", tc.changed); after != tc.after && tc.ok || ok != tc.ok {
			t.Errorf("%s: again %v %v, want %v %v", tc.what, after, ok, tc.after, tc.ok)
		}
	}
	if _, ok := b.chat.again("x", true); ok {
		t.Error("read again for another chat")
	}
}
