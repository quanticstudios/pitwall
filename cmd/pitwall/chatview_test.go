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
		if b.chat.streaming("c") != (tc.snap == busy && !tc.out) {
			t.Errorf("%s: streaming %v", tc.what, b.chat.streaming("c"))
		}
		b.chatRead(tc.id)
		if got := reads(); got != tc.want {
			t.Errorf("%s: %d reads, want %d", tc.what, got, tc.want)
		}
	}
}
