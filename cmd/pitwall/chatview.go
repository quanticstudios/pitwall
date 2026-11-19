//go:build !nogui

package main

import (
	"encoding/json"
	"log"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// chatCache is the transcript of the one chat the window shows, kept up
// to date with proto.ChatRead.
//
// shortcut: the window polls ChatRead while it draws the chat, every
// app.ChatPoll, and every chatStream while a turn runs; a ChatSubscribe
// that pushes deltas would replace the polling.
type chatCache struct {
	id    string
	snap  *session.Snapshot
	seq   int64     // snap's seq; 0 asks for a new snapshot
	out   bool      // a ChatRead is waiting for its reply
	at    time.Time // when the last ChatRead went out
	shown time.Time // when the window last asked for the transcript
}

// chatStream is how often the window reads a chat while its turn runs:
// once a frame, so a reply grows as it streams. A read goes out only
// after the last one's reply, so a slow link reads as often as it
// answers, and a reply that changes nothing draws no frame.
const chatStream = time.Second / 60

// Chat implements app.Chatter: the chat's transcript as last read, and a
// ChatRead for what came since (chatRead). Asking for another chat drops
// the one kept.
func (b *backend) Chat(id string) (session.Snapshot, bool) {
	b.mu.Lock()
	c := &b.chat
	if c.id != id {
		*c = chatCache{id: id}
	}
	c.shown = time.Now()
	var s session.Snapshot
	ok := c.snap != nil
	if ok {
		s = *c.snap
	}
	b.mu.Unlock()
	b.chatRead(id)
	return s, ok
}

// chatRead sends a ChatRead for what chat id gained when the window still
// shows it, none is out, and the last went out app.ChatPoll ago, or
// chatStream while a turn runs.
func (b *backend) chatRead(id string) {
	b.mu.Lock()
	c := &b.chat
	now := time.Now()
	every := app.ChatPoll
	if c.snap != nil && c.snap.Busy() {
		every = chatStream
	}
	if c.id != id || c.out || now.Sub(c.at) < every || now.Sub(c.shown) > time.Second {
		b.mu.Unlock()
		return
	}
	c.out, c.at = true, now
	read := proto.ChatRead{Chat: id, After: c.seq}
	b.mu.Unlock()
	if err := b.Send(read); err != nil {
		b.mu.Lock()
		if b.chat.id == id {
			b.chat.out = false
		}
		b.mu.Unlock()
	}
}

// again is when to read chat id next without waiting for a frame:
// chatStream while its turn runs, and app.ChatPoll after a reply that
// changed it, so what lands as a turn ends shows though the window draws
// no frame for a chat that went idle. Callers hold b.mu.
func (c *chatCache) again(id string, changed bool) (time.Duration, bool) {
	if c.id != id || c.out || c.snap == nil {
		return 0, false
	}
	if c.snap.Busy() {
		return chatStream, true
	}
	return app.ChatPoll, changed
}

// ChatResults implements app.ChatResulter.
func (b *backend) ChatResults(id string) []chatin.Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.chatOut[id]
	delete(b.chatOut, id)
	return r
}

// take applies a ChatRead's reply and reports whether the transcript
// changed. A delta that does not apply makes the next read ask for a
// snapshot, and the old transcript stays on screen until it comes.
func (c *chatCache) take(m proto.ChatDeltas) bool {
	if m.Chat != c.id {
		return false
	}
	c.out = false
	changed := false
	if m.Snapshot != nil {
		var s session.Snapshot
		if err := json.Unmarshal(m.Snapshot, &s); err != nil {
			log.Printf("chat %s: snapshot: %q", c.id, err)
			c.seq = 0
			return false
		}
		c.snap, c.seq, changed = &s, m.Seq, true
	}
	if c.snap == nil || c.seq != m.Seq {
		c.seq = 0
		return changed
	}
	for _, raw := range m.Deltas {
		var d session.Delta
		err := json.Unmarshal(raw, &d)
		var n session.Snapshot
		if err == nil {
			n, err = session.Apply(c.snap, c.seq, d)
		}
		if err != nil {
			log.Printf("chat %s: delta after %d: %q", c.id, c.seq, err)
			c.seq = 0
			return changed
		}
		c.snap, c.seq, changed = &n, d.Seq, true
	}
	return changed
}
