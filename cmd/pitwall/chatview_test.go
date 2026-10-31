package main

import (
	"encoding/json"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
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
